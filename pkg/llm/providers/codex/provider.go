package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

// RPCError is an error response returned by Codex App Server.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("codex: JSON-RPC error %d: %s", e.Code, e.Message)
}

type wireMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

// Provider owns one Codex App Server process and supports concurrent threads.
// The process is started lazily by the first request.
type Provider struct {
	config config

	startMu   sync.Mutex
	transport transport
	started   bool
	closed    bool
	readDone  chan struct{}
	readErr   error

	mu        sync.Mutex
	pending   map[int64]chan rpcResult
	turns     map[string]*turnState
	active    map[string]*turnState
	loaded    map[string]bool
	metadata  map[string]llmprovider.ModelMetadata
	nextID    atomic.Int64
	closeOnce sync.Once

	conversationMu sync.Mutex
}

func New(opts ...Option) *Provider {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.conversationCache == nil {
		cfg.conversationCache = NewMemoryConversationCache(defaultConversationCacheMaxEntries)
	}
	return &Provider{
		config:   cfg,
		readDone: make(chan struct{}),
		pending:  make(map[int64]chan rpcResult),
		turns:    make(map[string]*turnState),
		active:   make(map[string]*turnState),
		loaded:   make(map[string]bool),
		metadata: make(map[string]llmprovider.ModelMetadata),
	}
}

// ListModels discovers the picker-visible models from Codex App Server.
func (p *Provider) ListModels(ctx context.Context) ([]llmprovider.Model, error) {
	type appServerModel struct {
		ID                        string `json:"id"`
		Model                     string `json:"model"`
		ContextLength             int64  `json:"contextLength"`
		ContextWindow             int64  `json:"contextWindow"`
		ContextWindowTokens       int64  `json:"contextWindowTokens"`
		MaxContextLength          int64  `json:"maxContextLength"`
		MaxModelLength            int64  `json:"maxModelLen"`
		DefaultReasoningEffort    string `json:"defaultReasoningEffort"`
		SupportedReasoningEfforts []struct {
			ReasoningEffort string `json:"reasoningEffort"`
		} `json:"supportedReasoningEfforts"`
	}
	models := make([]llmprovider.Model, 0)
	var cursor string
	for {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var response struct {
			Data       []appServerModel `json:"data"`
			NextCursor *string          `json:"nextCursor"`
		}
		if err := p.Call(ctx, "model/list", params, &response); err != nil {
			return nil, err
		}
		for _, model := range response.Data {
			id := firstNonEmpty(model.Model, model.ID)
			if id != "" {
				reasoningEfforts := make([]string, 0, len(model.SupportedReasoningEfforts))
				for _, option := range model.SupportedReasoningEfforts {
					if option.ReasoningEffort != "" {
						reasoningEfforts = append(reasoningEfforts, option.ReasoningEffort)
					}
				}
				contextLength := model.ContextLength
				if contextLength == 0 {
					contextLength = model.ContextWindow
				}
				if contextLength == 0 {
					contextLength = model.ContextWindowTokens
				}
				if contextLength == 0 {
					contextLength = model.MaxContextLength
				}
				if contextLength == 0 {
					contextLength = model.MaxModelLength
				}
				if runtime := p.modelMetadata(id); runtime.ContextLength > 0 {
					contextLength = runtime.ContextLength
				}
				if contextLength <= 0 {
					contextLength = fallbackContextLength(id)
				}
				var capabilities *llmprovider.ModelCapabilities
				if len(reasoningEfforts) > 0 || model.DefaultReasoningEffort != "" {
					capabilities = &llmprovider.ModelCapabilities{Reasoning: &llmprovider.ReasoningCapabilities{
						Supported: true, Control: llmprovider.ReasoningControlEffort,
						SupportedEfforts: reasoningEfforts, DefaultEffort: model.DefaultReasoningEffort,
					}}
				}
				models = append(models, llmprovider.Model{
					ID: id, Object: "model", OwnedBy: "codex", ContextLength: contextLength,
					Capabilities: capabilities,
				})
			}
		}
		if response.NextCursor == nil || *response.NextCursor == "" {
			break
		}
		cursor = *response.NextCursor
	}
	return models, nil
}

// model/list does not currently advertise context windows. Use conservative
// defaults until thread/tokenUsage/updated supplies the effective window.
func fallbackContextLength(model string) int64 {
	if strings.Contains(strings.ToLower(model), "luna") {
		return 128000
	}
	return 256000
}

func (p *Provider) modelMetadata(model string) llmprovider.ModelMetadata {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.metadata[model]
}

func (p *Provider) rememberModelContextLength(model string, contextLength int64) {
	if model == "" || contextLength <= 0 {
		return
	}
	p.mu.Lock()
	metadata := p.metadata[model]
	metadata.ContextLength = contextLength
	p.metadata[model] = metadata
	p.mu.Unlock()
}

func (p *Provider) Chat(ctx context.Context, request llmprovider.ChatRequest) (*llmprovider.ChatResponse, error) {
	stream, err := p.ChatStream(ctx, request)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	response := &llmprovider.ChatResponse{
		Object: "chat.completion",
		Model:  firstNonEmpty(request.Model, p.config.model),
		Choices: []llmprovider.Choice{{
			Index:   0,
			Message: llmprovider.Message{Role: llmprovider.RoleAssistant},
		}},
	}
	var allContent, finalContent string
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if response.ID == "" {
			response.ID = chunk.ID
		}
		response.ConversationID = chunk.ConversationID
		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]
			if choice.Delta != nil {
				allContent += choice.Delta.Content
				if choice.Phase == "final_answer" {
					finalContent += choice.Delta.Content
				}
				response.Choices[0].Message.ToolCalls = append(
					response.Choices[0].Message.ToolCalls,
					choice.Delta.ToolCalls...,
				)
			}
			if choice.FinishReason != "" {
				response.Choices[0].FinishReason = choice.FinishReason
			}
		}
		if chunk.Usage != nil {
			response.Usage = *chunk.Usage
		}
	}
	response.Choices[0].Message.Content = allContent
	if finalContent != "" {
		response.Choices[0].Message.Content = finalContent
	}
	return response, nil
}

func (p *Provider) ChatStream(ctx context.Context, request llmprovider.ChatRequest) (llmprovider.Stream, error) {
	lastUser := lastUserMessage(request.Messages)
	hasToolResults := containsToolResult(request.Messages)
	// Historical tool results belong to earlier turns. A trailing user message
	// starts a new turn on the existing thread, not a delegated-tool continuation.
	toolResultContinuation := hasToolResults && request.ToolHandler == nil && lastUser != len(request.Messages)-1
	if lastUser < 0 && !toolResultContinuation {
		return nil, errors.New("codex: at least one user message is required")
	}
	if !toolResultContinuation && lastUser != len(request.Messages)-1 {
		return nil, errors.New("codex: the last message must have the user role")
	}
	var input []map[string]any
	if lastUser >= 0 {
		var err error
		input, err = turnInput(request.Messages[lastUser])
		if err != nil {
			return nil, err
		}
	}
	if err := p.ensureStarted(ctx); err != nil {
		return nil, err
	}

	// Selecting and reserving a thread is serialized so two requests extending
	// the same inferred checkpoint cannot both start on one Codex thread.
	p.conversationMu.Lock()
	defer p.conversationMu.Unlock()
	resolution := p.inferConversation(ctx, request)
	if resolution.inferred {
		request.ConversationID = p.resolveInferredThread(ctx, resolution, toolResultContinuation)
	}
	if toolResultContinuation && request.ConversationID != "" {
		if stream, resumed, err := p.resumeDelegatedTurn(ctx, request); resumed || err != nil {
			if stream != nil {
				stream = newConversationStream(p, request, stream)
			}
			return stream, err
		}
	}

	threadRequest := request
	if toolResultContinuation {
		// The previous App Server turn was interrupted while its dynamic-tool
		// callback was delegated. Rebuild from the caller's canonical OpenAI
		// message history so the interrupted callback result cannot conflict
		// with the externally supplied tool output.
		threadRequest.ConversationID = ""
	}
	threadID, isNew, err := p.prepareThread(ctx, threadRequest)
	if err != nil && resolution.inferred && threadRequest.ConversationID != "" {
		// Inferred cache entries can be stale (especially when an ephemeral App
		// Server was restarted). Fail open by rebuilding a fresh thread.
		threadRequest.ConversationID = ""
		threadID, isNew, err = p.prepareThread(ctx, threadRequest)
	}
	if err != nil {
		return nil, err
	}
	var history []llmprovider.Message
	if lastUser >= 0 {
		history = request.Messages[:lastUser]
	}
	if toolResultContinuation {
		history = request.Messages
		input = []map[string]any{{"type": "text", "text": "Continue the response using the supplied function result."}}
	}
	if isNew {
		if err := p.injectHistory(ctx, threadID, history); err != nil {
			return nil, err
		}
	}

	parallelTools := request.ParallelToolCalls == nil || *request.ParallelToolCalls
	state := newTurnState(
		ctx, p, threadID, firstNonEmpty(request.Model, p.config.model), request.ToolHandler, parallelTools,
	)
	p.mu.Lock()
	if previous := p.active[threadID]; previous != nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("codex: thread %s already has an active turn", threadID)
	}
	p.active[threadID] = state
	p.mu.Unlock()

	params := map[string]any{
		"threadId": threadID,
		"input":    input,
	}
	if model := firstNonEmpty(request.Model, p.config.model); model != "" {
		params["model"] = model
	}
	if cwd := firstNonEmpty(request.WorkingDirectory, p.config.cwd); cwd != "" {
		params["cwd"] = cwd
	}
	if request.ReasoningEffort != "" {
		params["effort"] = request.ReasoningEffort
	}
	if request.OutputSchema != nil {
		params["outputSchema"] = request.OutputSchema
	}

	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := p.Call(ctx, "turn/start", params, &response); err != nil {
		p.removeTurn(state)
		state.finish(err)
		return nil, err
	}
	if response.Turn.ID == "" {
		err := errors.New("codex: turn/start returned an empty turn ID")
		p.removeTurn(state)
		state.finish(err)
		return nil, err
	}
	state.setTurnID(response.Turn.ID)
	p.mu.Lock()
	p.turns[response.Turn.ID] = state
	p.mu.Unlock()
	return newConversationStream(p, request, &codexStream{state: state, ctx: ctx}), nil
}

func (p *Provider) resumeDelegatedTurn(ctx context.Context, request llmprovider.ChatRequest) (llmprovider.Stream, bool, error) {
	p.mu.Lock()
	state := p.active[request.ConversationID]
	p.mu.Unlock()
	if state == nil || !state.isAwaitingTools() {
		return nil, false, nil
	}
	pending, results, err := state.matchDelegatedToolResults(request.Messages)
	if err != nil {
		return nil, true, err
	}
	for index, item := range pending {
		if err := p.respondToServer(item.requestID, map[string]any{
			"success": true,
			"contentItems": []map[string]any{{
				"type": "inputText", "text": results[index].TextContent(),
			}},
		}, nil); err != nil {
			return nil, true, err
		}
		if !state.acknowledgeDelegatedTool(item) {
			return nil, true, fmt.Errorf("codex: delegated tool call %q is no longer pending", item.call.ID)
		}
	}
	return &codexStream{state: state, ctx: ctx}, true, nil
}

func (p *Provider) prepareThread(ctx context.Context, request llmprovider.ChatRequest) (string, bool, error) {
	dynamicTools, includeTools, err := p.dynamicTools(request)
	if err != nil {
		return "", false, err
	}
	if request.ConversationID != "" {
		p.mu.Lock()
		loaded := p.loaded[request.ConversationID]
		p.mu.Unlock()
		// thread/start and thread/resume subscribe this connection to the thread.
		// A loaded thread needs only turn/start, including for ephemeral threads
		// that have no rollout to reopen. Dynamic tools are registered at start;
		// thread/resume does not support updating their catalog.
		if !loaded {
			params := map[string]any{"threadId": request.ConversationID, "excludeTurns": true}
			if p.config.minimal {
				serverNames, err := p.configuredMCPServerNames(ctx, firstNonEmpty(request.WorkingDirectory, p.config.cwd))
				if err != nil {
					return "", false, err
				}
				config := minimalThreadConfig(serverNames)
				if overrides, ok := p.config.threadStartParams["config"].(map[string]any); ok {
					for key, value := range overrides {
						config[key] = cloneValue(value)
					}
				}
				params["config"] = config
			}
			var response threadResponse
			if err := p.Call(ctx, "thread/resume", params, &response); err != nil {
				return "", false, err
			}
			if response.Thread.ID == "" {
				return "", false, errors.New("codex: thread/resume returned an empty thread ID")
			}
			p.mu.Lock()
			p.loaded[response.Thread.ID] = true
			p.mu.Unlock()
		}
		return request.ConversationID, false, nil
	}

	var serverNames []string
	if p.config.minimal {
		serverNames, err = p.configuredMCPServerNames(ctx, firstNonEmpty(request.WorkingDirectory, p.config.cwd))
		if err != nil {
			return "", false, err
		}
	}
	params := p.newThreadStartParams(request, dynamicTools, includeTools, serverNames)
	var response threadResponse
	if err := p.Call(ctx, "thread/start", params, &response); err != nil {
		return "", false, err
	}
	if response.Thread.ID == "" {
		return "", false, errors.New("codex: thread/start returned an empty thread ID")
	}
	p.mu.Lock()
	p.loaded[response.Thread.ID] = true
	p.mu.Unlock()
	return response.Thread.ID, true, nil
}

func (p *Provider) newThreadStartParams(
	request llmprovider.ChatRequest,
	dynamicTools []map[string]any,
	includeTools bool,
	mcpServerNames []string,
) map[string]any {
	params := map[string]any{
		"approvalPolicy": string(p.config.approvalPolicy),
		"sandbox":        string(p.config.sandbox),
		"ephemeral":      p.config.ephemeral,
	}
	if p.config.minimal {
		params["baseInstructions"] = ""
		params["config"] = minimalThreadConfig(mcpServerNames)
		if p.config.experimentalAPI {
			params["environments"] = []any{}
		}
	}
	if p.config.baseInstructionsSet {
		params["baseInstructions"] = p.config.baseInstructions
	}
	if p.config.serviceName != "" {
		params["serviceName"] = p.config.serviceName
	}
	mergeThreadStartParams(params, p.config.threadStartParams)
	if model := firstNonEmpty(request.Model, p.config.model); model != "" {
		params["model"] = model
	}
	if cwd := firstNonEmpty(request.WorkingDirectory, p.config.cwd); cwd != "" {
		params["cwd"] = cwd
	}
	if instructions := developerInstructions(request.Messages); instructions != "" {
		params["developerInstructions"] = instructions
	}
	if includeTools {
		params["dynamicTools"] = dynamicTools
	}
	return params
}

func minimalThreadConfig(mcpServerNames []string) map[string]any {
	config := map[string]any{
		"include_permissions_instructions":        false,
		"include_apps_instructions":               false,
		"include_collaboration_mode_instructions": false,
		"include_environment_context":             false,
		"project_doc_max_bytes":                   0,
		"skills.include_instructions":             false,
		"features.plugins":                        false,
		"features.apps":                           false,
		"features.personality":                    false,
		"features.multi_agent":                    false,
		"features.multi_agent_v2":                 false,
		"features.tool_suggest":                   false,
		"features.goals":                          false,
		"features.browser_use":                    false,
		"features.browser_use_external":           false,
		"features.browser_use_full_cdp_access":    false,
		"features.computer_use":                   false,
		"features.image_generation":               false,
		"features.in_app_browser":                 false,
		"features.skill_search":                   false,
		"features.shell_tool":                     false,
		"features.unified_exec":                   false,
		"web_search":                              "disabled",
	}
	for _, name := range mcpServerNames {
		if name != "" {
			config["mcp_servers."+tomlPathSegment(name)+".enabled"] = false
		}
	}
	return config
}

// configuredMCPServerNames reads the effective Codex configuration for this
// workspace. Minimal threads must not inherit MCP tools from the user's Codex
// installation because those calls bypass the caller's dynamic tool runtime.
func (p *Provider) configuredMCPServerNames(ctx context.Context, cwd string) ([]string, error) {
	params := map[string]any{}
	if cwd != "" {
		params["cwd"] = cwd
	}
	var response struct {
		Config struct {
			MCPServers map[string]json.RawMessage `json:"mcp_servers"`
		} `json:"config"`
	}
	if err := p.Call(ctx, "config/read", params, &response); err != nil {
		return nil, fmt.Errorf("codex: read MCP configuration for minimal thread: %w", err)
	}
	names := make([]string, 0, len(response.Config.MCPServers))
	for name := range response.Config.MCPServers {
		names = append(names, name)
	}
	return names, nil
}

func tomlPathSegment(name string) string {
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return strconv.Quote(name)
	}
	return name
}

func mergeThreadStartParams(params, overrides map[string]any) {
	for key, value := range overrides {
		if key == "config" {
			baseConfig, baseOK := params[key].(map[string]any)
			overrideConfig, overrideOK := value.(map[string]any)
			if baseOK && overrideOK {
				merged := cloneMap(baseConfig)
				for configKey, configValue := range overrideConfig {
					merged[configKey] = cloneValue(configValue)
				}
				params[key] = merged
				continue
			}
		}
		params[key] = cloneValue(value)
	}
}

func cloneMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func cloneValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneMap(value)
	case []any:
		cloned := make([]any, len(value))
		for index, item := range value {
			cloned[index] = cloneValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), value...)
	default:
		return value
	}
}

func (p *Provider) dynamicTools(request llmprovider.ChatRequest) ([]map[string]any, bool, error) {
	if len(request.Tools) == 0 {
		return nil, false, nil
	}
	if !p.config.experimentalAPI {
		return nil, false, errors.New("codex: dynamic tools require the experimental API capability")
	}
	var namedChoice string
	switch choice := request.ToolChoice.(type) {
	case nil:
	case llmprovider.ToolChoiceMode:
		if choice == llmprovider.ToolChoiceNone {
			return []map[string]any{}, true, nil
		}
		if choice != "" && choice != llmprovider.ToolChoiceAuto && choice != llmprovider.ToolChoiceRequired {
			return nil, false, fmt.Errorf("codex: tool choice %q is not supported", choice)
		}
	case string:
		if choice == string(llmprovider.ToolChoiceNone) {
			return []map[string]any{}, true, nil
		}
		if choice != "" && choice != string(llmprovider.ToolChoiceAuto) &&
			choice != string(llmprovider.ToolChoiceRequired) {
			return nil, false, fmt.Errorf("codex: tool choice %q is not supported", choice)
		}
	case map[string]any:
		name, err := namedToolChoice(choice)
		if err != nil {
			return nil, false, err
		}
		namedChoice = name
	default:
		return nil, false, errors.New("codex: invalid structured tool choice")
	}
	tools := make([]map[string]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		if tool.Type != llmprovider.ToolTypeFunction {
			return nil, false, fmt.Errorf("codex: unsupported tool type %q", tool.Type)
		}
		if tool.Function.Name == "" {
			return nil, false, errors.New("codex: tool function name is required")
		}
		if namedChoice != "" && tool.Function.Name != namedChoice {
			continue
		}
		description := tool.Function.Description
		if description == "" {
			description = tool.Function.Name
		}
		inputSchema := any(tool.Function.Parameters)
		if inputSchema == nil {
			inputSchema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, map[string]any{
			"type": "function", "name": tool.Function.Name,
			"description": description, "inputSchema": inputSchema,
		})
	}
	if namedChoice != "" && len(tools) == 0 {
		return nil, false, fmt.Errorf("codex: named tool %q is not present in tools", namedChoice)
	}
	return tools, true, nil
}

func namedToolChoice(choice map[string]any) (string, error) {
	if choice["type"] != "function" {
		return "", errors.New("codex: structured tool choice must have type function")
	}
	var name string
	switch function := choice["function"].(type) {
	case map[string]any:
		name, _ = function["name"].(string)
	case map[string]string:
		name = function["name"]
	}
	if name == "" {
		return "", errors.New("codex: structured tool choice requires function.name")
	}
	return name, nil
}

type threadResponse struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

func (p *Provider) injectHistory(ctx context.Context, threadID string, messages []llmprovider.Message) error {
	items := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		if message.Role == llmprovider.RoleSystem || message.Role == llmprovider.RoleDeveloper {
			continue
		}
		if message.Role == llmprovider.RoleTool {
			items = append(items, map[string]any{
				"type": "function_call_output", "call_id": message.ToolCallID, "output": message.TextContent(),
			})
			continue
		}
		role := string(message.Role)
		if message.Role != llmprovider.RoleAssistant && message.Role != llmprovider.RoleUser {
			role = "user"
		}
		content, err := historyContent(message)
		if err != nil {
			return err
		}
		if len(content) > 0 {
			items = append(items, map[string]any{
				"type":    "message",
				"role":    role,
				"content": content,
			})
		}
		for _, call := range message.ToolCalls {
			items = append(items, map[string]any{
				"type": "function_call", "call_id": call.ID,
				"name": call.Function.Name, "arguments": call.Function.Arguments,
			})
		}
	}
	if len(items) == 0 {
		return nil
	}
	return p.Call(ctx, "thread/inject_items", map[string]any{"threadId": threadID, "items": items}, nil)
}

// Call invokes any App Server JSON-RPC method. A nil result discards the
// result. This is the escape hatch for APIs beyond the common chat surface.
func (p *Provider) Call(ctx context.Context, method string, params, result any) error {
	if err := p.ensureStarted(ctx); err != nil {
		return err
	}
	raw, err := p.callStarted(ctx, method, params)
	if err != nil {
		return err
	}
	if result != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("codex: decode %s result: %w", method, err)
		}
	}
	return nil
}

// Notify sends a JSON-RPC notification after ensuring App Server is ready.
func (p *Provider) Notify(ctx context.Context, method string, params any) error {
	if err := p.ensureStarted(ctx); err != nil {
		return err
	}
	return p.notifyStarted(method, params)
}

func (p *Provider) ensureStarted(ctx context.Context) error {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if p.closed {
		return errors.New("codex: provider is closed")
	}
	if p.started {
		return nil
	}
	factory := p.config.transportFactoryForTest
	if factory == nil {
		factory = func() (transport, error) { return startProcessTransport(p.config) }
	}
	transport, err := factory()
	if err != nil {
		return err
	}
	p.transport = transport
	p.started = true
	go p.readLoop()

	params := map[string]any{"clientInfo": map[string]string{
		"name": p.config.clientName, "title": p.config.clientTitle, "version": p.config.clientVersion,
	}}
	if p.config.experimentalAPI {
		params["capabilities"] = map[string]any{"experimentalApi": true}
	}
	if _, err := p.callStarted(ctx, "initialize", params); err != nil {
		_ = p.transport.Close()
		return err
	}
	if err := p.notifyStarted("initialized", map[string]any{}); err != nil {
		_ = p.transport.Close()
		return err
	}
	return nil
}

func (p *Provider) callStarted(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := p.nextID.Add(1)
	message, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return nil, fmt.Errorf("codex: encode %s request: %w", method, err)
	}
	response := make(chan rpcResult, 1)
	p.mu.Lock()
	p.pending[id] = response
	p.mu.Unlock()
	if err := p.transport.WriteMessage(message); err != nil {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return nil, fmt.Errorf("codex: write %s request: %w", method, err)
	}
	select {
	case value := <-response:
		return value.result, value.err
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return nil, ctx.Err()
	case <-p.readDone:
		return nil, p.connectionError()
	}
}

func (p *Provider) notifyStarted(method string, params any) error {
	message, err := json.Marshal(map[string]any{"method": method, "params": params})
	if err != nil {
		return fmt.Errorf("codex: encode %s notification: %w", method, err)
	}
	if err := p.transport.WriteMessage(message); err != nil {
		return fmt.Errorf("codex: write %s notification: %w", method, err)
	}
	return nil
}

func (p *Provider) readLoop() {
	for {
		data, err := p.transport.ReadMessage()
		if err != nil {
			p.failConnection(err)
			return
		}
		var message wireMessage
		if err := json.Unmarshal(data, &message); err != nil {
			p.failConnection(fmt.Errorf("decode server message: %w", err))
			return
		}
		if len(message.ID) > 0 && message.Method != "" {
			if message.Method == "item/tool/call" {
				// Register dynamic callbacks in wire order. Executing a configured
				// in-process handler is still dispatched asynchronously below.
				p.handleServerRequest(message)
			} else {
				go p.handleServerRequest(message)
			}
			continue
		}
		if len(message.ID) > 0 {
			var id int64
			if err := json.Unmarshal(message.ID, &id); err != nil {
				continue
			}
			p.mu.Lock()
			pending := p.pending[id]
			delete(p.pending, id)
			p.mu.Unlock()
			if pending != nil {
				if message.Error != nil {
					pending <- rpcResult{err: message.Error}
				} else {
					pending <- rpcResult{result: message.Result}
				}
			}
			continue
		}
		if message.Method != "" {
			p.handleNotification(message.Method, message.Params)
		}
	}
}

func (p *Provider) handleServerRequest(message wireMessage) {
	if message.Method == "item/tool/call" {
		p.handleDynamicToolCall(message)
		return
	}
	var result any
	var handlerErr error
	if p.config.requestHandler == nil {
		handlerErr = fmt.Errorf("no handler for server request %s", message.Method)
	} else {
		result, handlerErr = p.config.requestHandler(message.Method, message.Params)
	}
	p.respondToServer(message.ID, result, handlerErr)
}

func (p *Provider) handleDynamicToolCall(message wireMessage) {
	var params struct {
		Arguments json.RawMessage `json:"arguments"`
		CallID    string          `json:"callId"`
		ThreadID  string          `json:"threadId"`
		Tool      string          `json:"tool"`
		TurnID    string          `json:"turnId"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		p.respondToServer(message.ID, nil, fmt.Errorf("invalid dynamic tool call: %w", err))
		return
	}
	p.mu.Lock()
	state := p.turns[params.TurnID]
	if state == nil {
		state = p.active[params.ThreadID]
	}
	p.mu.Unlock()
	result := llmprovider.ToolResult{IsError: true, Content: "no handler for dynamic tool call"}
	call := llmprovider.ToolCall{
		ID: params.CallID, Type: llmprovider.ToolTypeFunction,
		Function: llmprovider.FunctionCall{Name: params.Tool, Arguments: string(params.Arguments)},
	}
	if state != nil && state.toolHandler == nil {
		state.setTurnIDIfEmpty(params.TurnID)
		if state.addDelegatedTool(call, message.ID) {
			return
		}
	}
	if state != nil && state.toolHandler != nil {
		go func() {
			value, err := state.toolHandler(state.ctx, call)
			if err != nil {
				value = llmprovider.ToolResult{IsError: true, Content: err.Error()}
			}
			_ = p.respondToServer(message.ID, map[string]any{
				"success":      !value.IsError,
				"contentItems": []map[string]any{{"type": "inputText", "text": value.Content}},
			}, nil)
		}()
		return
	}
	_ = p.respondToServer(message.ID, map[string]any{
		"success":      !result.IsError,
		"contentItems": []map[string]any{{"type": "inputText", "text": result.Content}},
	}, nil)
}

func (p *Provider) respondToServer(id json.RawMessage, result any, responseErr error) error {
	response := map[string]any{"id": json.RawMessage(id)}
	if responseErr != nil {
		response["error"] = map[string]any{"code": -32601, "message": responseErr.Error()}
	} else {
		response["result"] = result
	}
	data, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("codex: encode server response: %w", err)
	}
	if err := p.transport.WriteMessage(data); err != nil {
		return fmt.Errorf("codex: write server response: %w", err)
	}
	return nil
}

func (p *Provider) handleNotification(method string, params json.RawMessage) {
	var ids struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	_ = json.Unmarshal(params, &ids)
	p.mu.Lock()
	if method == "thread/closed" {
		delete(p.loaded, ids.ThreadID)
	}
	state := p.turns[ids.TurnID]
	if state == nil {
		state = p.active[ids.ThreadID]
	}
	p.mu.Unlock()
	if state == nil {
		return
	}
	if ids.TurnID != "" {
		state.setTurnIDIfEmpty(ids.TurnID)
	}

	switch method {
	case "thread/closed":
		p.removeTurn(state)
		state.finish(errors.New("codex: thread closed during an active turn"))
	case "item/agentMessage/delta":
		var event struct {
			Delta  string `json:"delta"`
			ItemID string `json:"itemId"`
		}
		if json.Unmarshal(params, &event) == nil {
			state.markDelta(event.ItemID)
			phase := state.itemPhase(event.ItemID)
			state.enqueue(&llmprovider.ChatChunk{
				ID: state.id(), Object: "chat.completion.chunk", Model: state.model,
				ConversationID: state.threadID,
				Choices:        []llmprovider.Choice{{Index: 0, Delta: &llmprovider.Message{Role: llmprovider.RoleAssistant, Content: event.Delta}, Phase: phase}},
			})
		}
	case "item/started":
		var event struct {
			Item struct {
				ID    string `json:"id"`
				Type  string `json:"type"`
				Phase string `json:"phase"`
			} `json:"item"`
		}
		if json.Unmarshal(params, &event) == nil && event.Item.Type == "agentMessage" {
			state.setItemPhase(event.Item.ID, event.Item.Phase)
		}
	case "item/completed":
		var event struct {
			Item struct {
				ID    string `json:"id"`
				Type  string `json:"type"`
				Text  string `json:"text"`
				Phase string `json:"phase"`
			} `json:"item"`
		}
		if json.Unmarshal(params, &event) == nil && event.Item.Type == "agentMessage" && !state.hasDelta(event.Item.ID) && event.Item.Text != "" {
			state.setItemPhase(event.Item.ID, event.Item.Phase)
			state.enqueue(&llmprovider.ChatChunk{
				ID: state.id(), Object: "chat.completion.chunk", Model: state.model,
				ConversationID: state.threadID,
				Choices:        []llmprovider.Choice{{Index: 0, Delta: &llmprovider.Message{Role: llmprovider.RoleAssistant, Content: event.Item.Text}, Phase: event.Item.Phase}},
			})
		}
	case "thread/tokenUsage/updated":
		var event struct {
			TokenUsage struct {
				ModelContextWindow int64 `json:"modelContextWindow"`
				Last               struct {
					InputTokens           int `json:"inputTokens"`
					CachedInputTokens     int `json:"cachedInputTokens"`
					CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
					OutputTokens          int `json:"outputTokens"`
					TotalTokens           int `json:"totalTokens"`
				} `json:"last"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(params, &event) == nil {
			p.rememberModelContextLength(state.model, event.TokenUsage.ModelContextWindow)
			last := event.TokenUsage.Last
			state.setUsage(llmprovider.Usage{
				PromptTokens: last.InputTokens, CompletionTokens: last.OutputTokens, TotalTokens: last.TotalTokens,
				PromptDetails: &llmprovider.TokenDetails{
					CachedTokens: last.CachedInputTokens, CacheWriteTokens: last.CacheWriteInputTokens,
				},
			})
		}
	case "error":
		var event struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(params, &event) == nil && event.Error.Message != "" {
			state.setTurnError(errors.New("codex: " + event.Error.Message))
		}
	case "turn/completed":
		var event struct {
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		if json.Unmarshal(params, &event) != nil {
			return
		}
		finishReason := "stop"
		var turnErr error
		switch event.Turn.Status {
		case "interrupted":
			finishReason = "cancelled"
		case "failed":
			if event.Turn.Error != nil && event.Turn.Error.Message != "" {
				turnErr = errors.New("codex: " + event.Turn.Error.Message)
			} else {
				turnErr = state.turnError()
			}
			if turnErr == nil {
				turnErr = errors.New("codex: turn failed")
			}
		}
		if turnErr == nil {
			usage := state.usageValue()
			state.enqueue(&llmprovider.ChatChunk{
				ID: state.id(), Object: "chat.completion.chunk", Model: state.model,
				ConversationID: state.threadID, Usage: &usage,
				Choices: []llmprovider.Choice{{Index: 0, Delta: &llmprovider.Message{}, FinishReason: finishReason}},
			})
		}
		p.removeTurn(state)
		state.finish(turnErr)
	}
}

func (p *Provider) removeTurn(state *turnState) {
	p.mu.Lock()
	delete(p.active, state.threadID)
	if id := state.id(); id != "" {
		delete(p.turns, id)
	}
	p.mu.Unlock()
}

func (p *Provider) interrupt(threadID, turnID string) {
	if turnID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.Call(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
}

func (p *Provider) failConnection(err error) {
	p.mu.Lock()
	p.readErr = err
	for id, pending := range p.pending {
		pending <- rpcResult{err: fmt.Errorf("codex: app-server connection: %w", err)}
		delete(p.pending, id)
	}
	states := make(map[*turnState]struct{})
	for _, state := range p.active {
		states[state] = struct{}{}
	}
	p.mu.Unlock()
	for state := range states {
		state.finish(fmt.Errorf("codex: app-server connection: %w", err))
	}
	close(p.readDone)
}

func (p *Provider) connectionError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.readErr == nil {
		return errors.New("codex: app-server connection closed")
	}
	return fmt.Errorf("codex: app-server connection: %w", p.readErr)
}

func (p *Provider) Close() error {
	var closeErr error
	p.closeOnce.Do(func() {
		p.startMu.Lock()
		p.closed = true
		if p.transport != nil {
			closeErr = p.transport.Close()
		}
		p.startMu.Unlock()
		if p.config.conversationCache != nil {
			if err := p.config.conversationCache.Close(); closeErr == nil {
				closeErr = err
			}
		}
	})
	return closeErr
}

func developerInstructions(messages []llmprovider.Message) string {
	var instructions []string
	for _, message := range messages {
		if message.Role == llmprovider.RoleSystem || message.Role == llmprovider.RoleDeveloper {
			instructions = append(instructions, message.TextContent())
		}
	}
	return strings.Join(instructions, "\n\n")
}

func lastUserMessage(messages []llmprovider.Message) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == llmprovider.RoleUser {
			return index
		}
	}
	return -1
}

func containsToolResult(messages []llmprovider.Message) bool {
	for _, message := range messages {
		if message.Role == llmprovider.RoleTool {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

var _ llmprovider.Provider = (*Provider)(nil)
var _ llmprovider.ModelLister = (*Provider)(nil)
