package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestDefaultCommandStartsCodexAppServer(t *testing.T) {
	cfg := defaultConfig()
	if cfg.command != "codex" || !slices.Equal(cfg.args, []string{"app-server", "--listen", "stdio://"}) {
		t.Fatalf("unexpected command: %q %#v", cfg.command, cfg.args)
	}
	if !cfg.minimal {
		t.Fatal("minimal prompt mode must be enabled by default")
	}
}

func TestFullPromptIsExplicitOptOut(t *testing.T) {
	cfg := defaultConfig()
	WithFullPrompt()(&cfg)
	if cfg.minimal {
		t.Fatal("full prompt option did not disable minimal mode")
	}
}

func TestMinimalThreadStartParamsCanBeOverridden(t *testing.T) {
	overrides := map[string]any{
		"sandbox":               "danger-full-access",
		"model":                 "configured-model",
		"developerInstructions": "configured developer instructions",
		"config": map[string]any{
			"include_environment_context":             true,
			"mcp_servers.example.enabled":             false,
			"include_collaboration_mode_instructions": true,
		},
	}
	provider := New(
		WithMinimal(),
		WithBaseInstructions("Compact base instructions."),
		WithThreadStartParams(overrides),
	)
	overrides["sandbox"] = "workspace-write"
	overrides["config"].(map[string]any)["include_environment_context"] = false

	dynamicTools := []map[string]any{{"name": "lookup"}}
	request := llmprovider.ChatRequest{
		Model:            "request-model",
		WorkingDirectory: `C:\repo`,
		Messages: []llmprovider.Message{
			{Role: llmprovider.RoleSystem, Content: "Request developer instructions."},
			{Role: llmprovider.RoleUser, Content: "Hello"},
		},
	}
	params := provider.newThreadStartParams(request, dynamicTools, true, nil)

	if params["baseInstructions"] != "Compact base instructions." {
		t.Fatalf("baseInstructions = %#v", params["baseInstructions"])
	}
	if params["sandbox"] != "danger-full-access" {
		t.Fatalf("sandbox = %#v", params["sandbox"])
	}
	if params["model"] != "request-model" || params["cwd"] != `C:\repo` {
		t.Fatalf("request overrides = %#v", params)
	}
	if params["developerInstructions"] != "Request developer instructions." {
		t.Fatalf("developerInstructions = %#v", params["developerInstructions"])
	}
	if !slices.Equal(params["environments"].([]any), []any{}) {
		t.Fatalf("environments = %#v", params["environments"])
	}
	config := params["config"].(map[string]any)
	if config["include_environment_context"] != true ||
		config["include_collaboration_mode_instructions"] != true ||
		config["skills.include_instructions"] != false ||
		config["features.plugins"] != false ||
		config["features.personality"] != false ||
		config["project_doc_max_bytes"] != 0 ||
		config["features.shell_tool"] != false ||
		config["features.unified_exec"] != false ||
		config["web_search"] != "disabled" ||
		config["mcp_servers.example.enabled"] != false {
		t.Fatalf("config = %#v", config)
	}
	if got := params["dynamicTools"].([]map[string]any); len(got) != 1 || got[0]["name"] != "lookup" {
		t.Fatalf("dynamicTools = %#v", params["dynamicTools"])
	}

	config["features.plugins"] = true
	second := provider.newThreadStartParams(request, dynamicTools, true, nil)
	if second["config"].(map[string]any)["features.plugins"] != false {
		t.Fatal("thread/start config was mutated through returned params")
	}
}

func TestEmptyBaseInstructionsAreForwarded(t *testing.T) {
	provider := New(WithBaseInstructions(""))
	params := provider.newThreadStartParams(llmprovider.ChatRequest{}, nil, false, nil)
	value, exists := params["baseInstructions"]
	if !exists || value != "" {
		t.Fatalf("baseInstructions = %#v, exists = %v", value, exists)
	}
}

func TestMinimalThreadConfigDisablesConfiguredMCPServers(t *testing.T) {
	config := minimalThreadConfig([]string{"node_repl", "custom.server"})
	if config["mcp_servers.node_repl.enabled"] != false ||
		config[`mcp_servers."custom.server".enabled`] != false {
		t.Fatalf("minimal MCP overrides = %#v", config)
	}
	provider := New(WithThreadStartParams(map[string]any{"config": map[string]any{
		"mcp_servers.node_repl.enabled": true,
	}}))
	params := provider.newThreadStartParams(llmprovider.ChatRequest{}, nil, false, []string{"node_repl", "other"})
	got := params["config"].(map[string]any)
	if got["mcp_servers.node_repl.enabled"] != true || got["mcp_servers.other.enabled"] != false {
		t.Fatalf("explicit MCP override was not preserved: %#v", got)
	}
}

func TestMinimalThreadDisablesInheritedMCPOnStartAndResume(t *testing.T) {
	for _, test := range []struct {
		name           string
		conversationID string
		method         string
	}{
		{name: "start", method: "thread/start"},
		{name: "resume", conversationID: "thread_1", method: "thread/resume"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeTransport()
			fake.mcpServerNames = []string{"node_repl", "openaiDeveloperDocs"}
			provider := New(func(cfg *config) {
				cfg.transportFactoryForTest = func() (transport, error) { return fake, nil }
			})
			defer provider.Close()
			serverErr := make(chan error, 1)
			go func() {
				for data := range fake.writes {
					var request struct {
						ID     int64          `json:"id"`
						Method string         `json:"method"`
						Params map[string]any `json:"params"`
					}
					if err := json.Unmarshal(data, &request); err != nil {
						serverErr <- err
						return
					}
					switch request.Method {
					case "initialize":
						fake.send(map[string]any{"id": request.ID, "result": map[string]any{}})
					case "initialized":
					case test.method:
						settings, ok := request.Params["config"].(map[string]any)
						if !ok || settings["mcp_servers.node_repl.enabled"] != false ||
							settings["mcp_servers.openaiDeveloperDocs.enabled"] != false {
							serverErr <- fmt.Errorf("inherited MCP servers were not disabled: %#v", request.Params["config"])
							return
						}
						fake.send(map[string]any{"id": request.ID, "result": map[string]any{"thread": map[string]any{"id": "thread_1"}}})
						serverErr <- nil
						return
					default:
						serverErr <- fmt.Errorf("unexpected method %q", request.Method)
						return
					}
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if _, _, err := provider.prepareThread(ctx, llmprovider.ChatRequest{Model: "test-model", ConversationID: test.conversationID}); err != nil {
				t.Fatal(err)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestListModelsPreservesContextLength(t *testing.T) {
	fake := newFakeTransport()
	provider := New(func(config *config) {
		config.transportFactoryForTest = func() (transport, error) { return fake, nil }
	})
	defer provider.Close()
	provider.rememberModelContextLength("codex-test", 258400)

	go func() {
		for data := range fake.writes {
			var request struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(data, &request)
			switch request.Method {
			case "initialize":
				fake.send(map[string]any{"id": request.ID, "result": map[string]any{}})
			case "model/list":
				fake.send(map[string]any{"id": request.ID, "result": map[string]any{
					"data": []map[string]any{{
						"id": "picker-id", "model": "codex-test", "contextWindow": 400000,
						"defaultReasoningEffort": "medium",
						"supportedReasoningEfforts": []map[string]any{
							{"reasoningEffort": "low", "description": "Fast"},
							{"reasoningEffort": "medium", "description": "Balanced"},
							{"reasoningEffort": "high", "description": "Deep"},
						},
					}},
				}})
				return
			}
		}
	}()

	models, err := provider.ListModels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reasoning := models[0].Capabilities.Reasoning
	if len(models) != 1 || models[0].ID != "codex-test" || models[0].ContextLength != 258400 ||
		reasoning.Control != llmprovider.ReasoningControlEffort || reasoning.DefaultEffort != "medium" ||
		!slices.Equal(reasoning.SupportedEfforts, []string{"low", "medium", "high"}) {
		t.Fatalf("models = %#v", models)
	}
}

type fakeTransport struct {
	reads          chan []byte
	writes         chan []byte
	closed         chan struct{}
	closeOnce      sync.Once
	mcpServerNames []string
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		reads:  make(chan []byte, 32),
		writes: make(chan []byte, 32),
		closed: make(chan struct{}),
	}
}

func (t *fakeTransport) ReadMessage() ([]byte, error) {
	select {
	case message := <-t.reads:
		return message, nil
	case <-t.closed:
		return nil, io.EOF
	}
}

func (t *fakeTransport) WriteMessage(message []byte) error {
	var request struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
	}
	if json.Unmarshal(message, &request) == nil && request.Method == "config/read" {
		servers := make(map[string]any, len(t.mcpServerNames))
		for _, name := range t.mcpServerNames {
			servers[name] = map[string]any{}
		}
		t.send(map[string]any{"id": request.ID, "result": map[string]any{
			"config": map[string]any{"mcp_servers": servers},
		}})
		return nil
	}
	select {
	case t.writes <- append([]byte(nil), message...):
		return nil
	case <-t.closed:
		return io.ErrClosedPipe
	}
}

func (t *fakeTransport) Close() error {
	t.closeOnce.Do(func() { close(t.closed) })
	return nil
}

func (t *fakeTransport) send(message any) {
	data, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	t.reads <- data
}

func TestChatAdaptsCodexProtocol(t *testing.T) {
	fake := newFakeTransport()
	provider := New(func(config *config) {
		config.transportFactoryForTest = func() (transport, error) { return fake, nil }
	})
	defer provider.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- serveChat(fake)
	}()

	client := llmprovider.NewWithProvider(provider)
	response, err := client.Chat(context.Background(), llmprovider.ChatRequest{
		Model: "test-model",
		Messages: []llmprovider.Message{
			{Role: llmprovider.RoleSystem, Content: "Be concise."},
			{Role: llmprovider.RoleUser, Content: "Earlier question"},
			{Role: llmprovider.RoleAssistant, Content: "Earlier answer"},
			{Role: llmprovider.RoleUser, Content: "Current question"},
		},
		ReasoningEffort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Choices[0].Message.Content; got != "Hello from Codex" {
		t.Fatalf("content = %q", got)
	}
	if response.ConversationID != "thread_1" || response.ID != "turn_1" {
		t.Fatalf("ids = %q, %q", response.ConversationID, response.ID)
	}
	if response.Usage.TotalTokens != 15 || response.Choices[0].FinishReason != "stop" {
		t.Fatalf("response = %#v", response)
	}
	if metadata := provider.modelMetadata("test-model"); metadata.ContextLength != 258400 {
		t.Fatalf("model metadata = %#v", metadata)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestChatForwardsImagesToCodex(t *testing.T) {
	const imageURL = "data:image/png;base64,aW1hZ2U="
	tests := []struct {
		name        string
		messages    []llmprovider.Message
		wantHistory []any
		wantInput   []any
	}{
		{
			name: "mixed content and history",
			messages: []llmprovider.Message{
				{Role: llmprovider.RoleUser, ContentParts: []llmprovider.MessageContentPart{
					{"type": "text", "text": "earlier "},
					{"type": "image_url", "image_url": map[string]any{"url": imageURL}},
					{"type": "text", "text": " question"},
				}},
				{Role: llmprovider.RoleAssistant, Content: "answer"},
				{Role: llmprovider.RoleUser, ContentParts: []llmprovider.MessageContentPart{
					{"type": "text", "text": "first "},
					{"type": "image_url", "image_url": map[string]any{"url": imageURL, "detail": "high"}},
					{"type": "text", "text": " second"},
					{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/image.png"}},
				}},
			},
			wantHistory: []any{
				map[string]any{"type": "message", "role": "user", "content": []any{
					map[string]any{"type": "input_text", "text": "earlier "},
					map[string]any{"type": "input_image", "image_url": imageURL},
					map[string]any{"type": "input_text", "text": " question"},
				}},
				map[string]any{"type": "message", "role": "assistant", "content": []any{
					map[string]any{"type": "output_text", "text": "answer"},
				}},
			},
			wantInput: []any{
				map[string]any{"type": "text", "text": "first "},
				map[string]any{"type": "image", "url": imageURL, "detail": "high"},
				map[string]any{"type": "text", "text": " second"},
				map[string]any{"type": "image", "url": "https://example.com/image.png"},
			},
		},
		{
			name: "image only",
			messages: []llmprovider.Message{{Role: llmprovider.RoleUser, ContentParts: []llmprovider.MessageContentPart{
				{"type": "input_image", "image_url": imageURL},
			}}},
			wantInput: []any{map[string]any{"type": "image", "url": imageURL}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeTransport()
			provider := New(func(config *config) {
				config.transportFactoryForTest = func() (transport, error) { return fake, nil }
			})
			defer provider.Close()
			serverErr := make(chan error, 1)
			go func() {
				var history, input []any
				for {
					select {
					case data := <-fake.writes:
						var request struct {
							ID     int64          `json:"id"`
							Method string         `json:"method"`
							Params map[string]any `json:"params"`
						}
						if err := json.Unmarshal(data, &request); err != nil {
							serverErr <- err
							return
						}
						switch request.Method {
						case "initialize":
							fake.send(map[string]any{"id": request.ID, "result": map[string]any{}})
						case "initialized":
						case "thread/start":
							fake.send(map[string]any{"id": request.ID, "result": map[string]any{"thread": map[string]any{"id": "thread_1"}}})
						case "thread/inject_items":
							history, _ = request.Params["items"].([]any)
							fake.send(map[string]any{"id": request.ID, "result": map[string]any{}})
						case "turn/start":
							input, _ = request.Params["input"].([]any)
							fake.send(map[string]any{"id": request.ID, "result": map[string]any{"turn": map[string]any{"id": "turn_1"}}})
							fake.send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread_1", "turn": map[string]any{"id": "turn_1", "status": "completed"}}})
							if !reflect.DeepEqual(history, test.wantHistory) || !reflect.DeepEqual(input, test.wantInput) {
								serverErr <- fmt.Errorf("history = %#v, input = %#v", history, input)
							} else {
								serverErr <- nil
							}
							return
						default:
							serverErr <- fmt.Errorf("unexpected method %q", request.Method)
							return
						}
					case <-time.After(5 * time.Second):
						serverErr <- errors.New("timed out waiting for Codex request")
						return
					}
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err := llmprovider.NewWithProvider(provider).Chat(ctx, llmprovider.ChatRequest{
				Model: "test-model", Messages: test.messages,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func serveChat(transport *fakeTransport) error {
	for step := 0; step < 5; {
		select {
		case data := <-transport.writes:
			var request struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal(data, &request); err != nil {
				return err
			}
			switch request.Method {
			case "initialize":
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{"userAgent": "test"}})
				step++
			case "initialized":
				step++
			case "thread/start":
				if request.Params["sandbox"] != "read-only" || request.Params["approvalPolicy"] != "never" || request.Params["ephemeral"] != true {
					return errors.New("unexpected safe defaults")
				}
				if request.Params["developerInstructions"] != "Be concise." {
					return errors.New("system message was not mapped to developer instructions")
				}
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{"thread": map[string]any{"id": "thread_1"}}})
				step++
			case "thread/inject_items":
				items, ok := request.Params["items"].([]any)
				if !ok || len(items) != 2 {
					return errors.New("chat history was not injected")
				}
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{}})
				step++
			case "turn/start":
				if request.Params["effort"] != "medium" {
					return errors.New("reasoning effort was not forwarded")
				}
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{"turn": map[string]any{"id": "turn_1", "status": "inProgress"}}})
				transport.send(map[string]any{"method": "item/started", "params": map[string]any{"threadId": "thread_1", "turnId": "turn_1", "item": map[string]any{"id": "item_0", "type": "agentMessage", "text": "", "phase": "commentary"}}})
				transport.send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": "thread_1", "turnId": "turn_1", "itemId": "item_0", "delta": "Working..."}})
				transport.send(map[string]any{"method": "item/started", "params": map[string]any{"threadId": "thread_1", "turnId": "turn_1", "item": map[string]any{"id": "item_1", "type": "agentMessage", "text": "", "phase": "final_answer"}}})
				transport.send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": "thread_1", "turnId": "turn_1", "itemId": "item_1", "delta": "Hello "}})
				transport.send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": "thread_1", "turnId": "turn_1", "itemId": "item_1", "delta": "from Codex"}})
				transport.send(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": "thread_1", "turnId": "turn_1", "tokenUsage": map[string]any{"modelContextWindow": 258400, "last": map[string]any{"inputTokens": 10, "outputTokens": 5, "totalTokens": 15}}}})
				transport.send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread_1", "turn": map[string]any{"id": "turn_1", "status": "completed"}}})
				step++
			default:
				return errors.New("unexpected method: " + request.Method)
			}
		case <-time.After(5 * time.Second):
			return errors.New("timed out waiting for client request")
		}
	}
	return nil
}

func TestCallReturnsRPCError(t *testing.T) {
	fake := newFakeTransport()
	provider := New(func(config *config) {
		config.transportFactoryForTest = func() (transport, error) { return fake, nil }
	})
	defer provider.Close()

	go func() {
		for data := range fake.writes {
			var request struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(data, &request)
			if request.Method == "initialize" {
				fake.send(map[string]any{"id": request.ID, "result": map[string]any{}})
			} else if request.Method == "model/list" {
				fake.send(map[string]any{"id": request.ID, "error": map[string]any{"code": -32602, "message": "bad params"}})
				return
			}
		}
	}()

	var result any
	err := provider.Call(context.Background(), "model/list", map[string]any{}, &result)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32602 {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestChatExecutesDynamicTool(t *testing.T) {
	fake := newFakeTransport()
	provider := New(WithBaseInstructions("Use actual tools."), func(config *config) {
		config.transportFactoryForTest = func() (transport, error) { return fake, nil }
	})
	defer provider.Close()
	serverErr := make(chan error, 1)
	go func() { serverErr <- serveDynamicToolChat(fake) }()

	client := llmprovider.New(provider)
	response, err := client.Chat(context.Background(), llmprovider.ChatRequest{
		Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "Seoul weather?"}},
		Tools: []llmprovider.Tool{{Type: llmprovider.ToolTypeFunction, Function: llmprovider.FunctionDefinition{
			Name: "get_weather", Description: "Get weather",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
		}}},
		ToolHandler: func(ctx context.Context, call llmprovider.ToolCall) (llmprovider.ToolResult, error) {
			if call.ID != "call_1" || call.Function.Name != "get_weather" || call.Function.Arguments != `{"city":"Seoul"}` {
				return llmprovider.ToolResult{}, errors.New("unexpected tool call")
			}
			return llmprovider.ToolResult{Content: `{"weather":"sunny"}`}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Choices[0].Message.Content; got != "Weather is sunny" {
		t.Fatalf("content = %q", got)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func serveDynamicToolChat(transport *fakeTransport) error {
	for {
		select {
		case data := <-transport.writes:
			var request struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
				Result map[string]any `json:"result"`
			}
			if err := json.Unmarshal(data, &request); err != nil {
				return err
			}
			switch request.Method {
			case "initialize":
				caps, _ := request.Params["capabilities"].(map[string]any)
				if caps["experimentalApi"] != true {
					return errors.New("experimental API was not enabled")
				}
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{}})
			case "initialized":
			case "thread/start":
				if request.Params["baseInstructions"] != "Use actual tools." {
					return errors.New("base instructions were not forwarded")
				}
				tools, ok := request.Params["dynamicTools"].([]any)
				if !ok || len(tools) != 1 {
					return errors.New("dynamic tools were not forwarded")
				}
				tool := tools[0].(map[string]any)
				if tool["name"] != "get_weather" || tool["type"] != "function" {
					return errors.New("dynamic tool has unexpected shape")
				}
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{"thread": map[string]any{"id": "thread_tool"}}})
			case "turn/start":
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{"turn": map[string]any{"id": "turn_tool", "status": "inProgress"}}})
				transport.send(map[string]any{"id": 900, "method": "item/tool/call", "params": map[string]any{
					"threadId": "thread_tool", "turnId": "turn_tool", "callId": "call_1",
					"tool": "get_weather", "arguments": map[string]any{"city": "Seoul"},
				}})
			case "":
				if request.ID != 900 || request.Result["success"] != true {
					return errors.New("dynamic tool response failed")
				}
				contentItems, ok := request.Result["contentItems"].([]any)
				if !ok || len(contentItems) != 1 || contentItems[0].(map[string]any)["text"] != `{"weather":"sunny"}` {
					return errors.New("unexpected dynamic tool result")
				}
				transport.send(map[string]any{"method": "item/started", "params": map[string]any{"threadId": "thread_tool", "turnId": "turn_tool", "item": map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer"}}})
				transport.send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": "thread_tool", "turnId": "turn_tool", "itemId": "answer", "delta": "Weather is sunny"}})
				transport.send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread_tool", "turn": map[string]any{"id": "turn_tool", "status": "completed"}}})
				return nil
			default:
				return errors.New("unexpected method: " + request.Method)
			}
		case <-time.After(5 * time.Second):
			return errors.New("timed out waiting for dynamic tool flow")
		}
	}
}

func TestChatDelegatesDynamicTool(t *testing.T) {
	fake := newFakeTransport()
	provider := New(func(config *config) {
		config.transportFactoryForTest = func() (transport, error) { return fake, nil }
	})
	defer provider.Close()
	serverErr := make(chan error, 1)
	go func() { serverErr <- serveDelegatedToolChat(fake) }()

	response, err := provider.Chat(context.Background(), llmprovider.ChatRequest{
		Model:    "test-model",
		Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "Use lookup_value."}},
		Tools: []llmprovider.Tool{{
			Type: llmprovider.ToolTypeFunction,
			Function: llmprovider.FunctionDefinition{
				Name: "lookup_value", Description: "Look up a value.",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{
					"key": map[string]any{"type": "string"},
				}},
			},
		}},
		ToolChoice: llmprovider.ToolChoiceAuto,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Choices) != 1 || response.Choices[0].FinishReason != "tool_calls" ||
		len(response.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("response = %#v", response)
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if call.ID != "call_delegate_1" || call.Function.Name != "lookup_value" ||
		call.Function.Arguments != `{"key":"demo"}` || response.ConversationID != "thread_delegate" {
		t.Fatalf("tool call = %#v, conversation = %q", call, response.ConversationID)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func serveDelegatedToolChat(transport *fakeTransport) error {
	for {
		select {
		case data := <-transport.writes:
			var request struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
				Result map[string]any `json:"result"`
			}
			if err := json.Unmarshal(data, &request); err != nil {
				return err
			}
			switch request.Method {
			case "initialize":
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{}})
			case "initialized":
			case "thread/start":
				tools, ok := request.Params["dynamicTools"].([]any)
				if !ok || len(tools) != 1 {
					return errors.New("delegated dynamic tools were not forwarded")
				}
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{
					"thread": map[string]any{"id": "thread_delegate"},
				}})
			case "turn/start":
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{
					"turn": map[string]any{"id": "turn_delegate", "status": "inProgress"},
				}})
				transport.send(map[string]any{"id": 901, "method": "item/tool/call", "params": map[string]any{
					"threadId": "thread_delegate", "turnId": "turn_delegate", "callId": "call_delegate_1",
					"tool": "lookup_value", "arguments": map[string]any{"key": "demo"},
				}})
				return nil
			default:
				return errors.New("unexpected method: " + request.Method)
			}
		case <-time.After(5 * time.Second):
			return errors.New("timed out waiting for delegated tool flow")
		}
	}
}

func TestChatContinuesFromDelegatedToolResult(t *testing.T) {
	fake := newFakeTransport()
	provider := New(func(config *config) {
		config.transportFactoryForTest = func() (transport, error) { return fake, nil }
	})
	defer provider.Close()
	serverErr := make(chan error, 1)
	go func() { serverErr <- serveDelegatedToolContinuation(fake) }()

	response, err := provider.Chat(context.Background(), llmprovider.ChatRequest{
		Model: "test-model",
		Messages: []llmprovider.Message{
			{Role: llmprovider.RoleUser, Content: "Use lookup_value."},
			{Role: llmprovider.RoleAssistant, ToolCalls: []llmprovider.ToolCall{{
				ID: "call_delegate_1", Type: llmprovider.ToolTypeFunction,
				Function: llmprovider.FunctionCall{Name: "lookup_value", Arguments: `{"key":"demo"}`},
			}}},
			{Role: llmprovider.RoleTool, ToolCallID: "call_delegate_1", Content: `{"value":"RESULT_42"}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Choices[0].Message.Content != "The result is RESULT_42" {
		t.Fatalf("response = %#v", response)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestChatResumesDelegatedToolOnSameTurn(t *testing.T) {
	fake := newFakeTransport()
	provider := New(func(config *config) {
		config.transportFactoryForTest = func() (transport, error) { return fake, nil }
	})
	defer provider.Close()
	serverErr := make(chan error, 1)
	go func() { serverErr <- serveSameTurnDelegatedTool(fake) }()

	request := llmprovider.ChatRequest{
		Model:    "test-model",
		Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "Use lookup_value."}},
		Tools: []llmprovider.Tool{{
			Type: llmprovider.ToolTypeFunction,
			Function: llmprovider.FunctionDefinition{
				Name: "lookup_value", Parameters: map[string]any{"type": "object"},
			},
		}},
	}
	first, err := provider.Chat(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	call := first.Choices[0].Message.ToolCalls[0]
	request.ConversationID = first.ConversationID
	request.Messages = append(request.Messages, first.Choices[0].Message, llmprovider.Message{
		Role: llmprovider.RoleTool, ToolCallID: call.ID, Content: `{"value":"RESULT_42"}`,
	})
	final, err := provider.Chat(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if final.ConversationID != first.ConversationID || final.ID != first.ID {
		t.Fatalf("same turn was not preserved: first=%q/%q final=%q/%q",
			first.ConversationID, first.ID, final.ConversationID, final.ID)
	}
	if final.Choices[0].Message.Content != "The result is RESULT_42" {
		t.Fatalf("final response = %#v", final)
	}
	if final.Usage.PromptDetails == nil || final.Usage.PromptDetails.CachedTokens != 1000 ||
		final.Usage.PromptDetails.CacheWriteTokens != 20 {
		t.Fatalf("cache usage = %#v", final.Usage)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestChatSurfacesLateDelegatedToolOnNextRound(t *testing.T) {
	fake := newFakeTransport()
	provider := New(func(config *config) {
		config.transportFactoryForTest = func() (transport, error) { return fake, nil }
	})
	defer provider.Close()
	releaseLateTool := make(chan struct{})
	serverErr := make(chan error, 1)
	go func() { serverErr <- serveLateDelegatedTools(fake, releaseLateTool) }()

	parallel := false
	request := llmprovider.ChatRequest{
		Model:             "test-model",
		Messages:          []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "Use both tools."}},
		ParallelToolCalls: &parallel,
		Tools: []llmprovider.Tool{{
			Type: llmprovider.ToolTypeFunction,
			Function: llmprovider.FunctionDefinition{
				Name: "lookup_value", Parameters: map[string]any{"type": "object"},
			},
		}},
	}
	first, err := provider.Chat(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls := first.Choices[0].Message.ToolCalls; len(calls) != 1 || calls[0].ID != "call_late_a" {
		t.Fatalf("first response = %#v", first)
	}

	close(releaseLateTool)
	waitForDelegatedToolCount(t, provider, "thread_late", 2)
	request.ConversationID = first.ConversationID
	request.Messages = append(request.Messages, first.Choices[0].Message, llmprovider.Message{
		Role: llmprovider.RoleTool, ToolCallID: "call_late_a", Content: "A",
	})
	second, err := provider.Chat(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls := second.Choices[0].Message.ToolCalls; len(calls) != 1 || calls[0].ID != "call_late_b" || calls[0].Index != 0 {
		t.Fatalf("second response = %#v", second)
	}

	request.Messages = append(request.Messages, second.Choices[0].Message, llmprovider.Message{
		Role: llmprovider.RoleTool, ToolCallID: "call_late_b", Content: "B",
	})
	final, err := provider.Chat(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if final.ConversationID != first.ConversationID || final.ID != first.ID ||
		final.Choices[0].Message.Content != "A and B received" {
		t.Fatalf("final response = %#v", final)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func waitForDelegatedToolCount(t *testing.T, provider *Provider, threadID string, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		provider.mu.Lock()
		state := provider.active[threadID]
		provider.mu.Unlock()
		if state != nil && len(state.delegatedTools()) == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("thread %s did not reach %d delegated tools", threadID, count)
}

func serveLateDelegatedTools(transport *fakeTransport, releaseLateTool <-chan struct{}) error {
	for {
		select {
		case data := <-transport.writes:
			var request struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
				Result map[string]any `json:"result"`
			}
			if err := json.Unmarshal(data, &request); err != nil {
				return err
			}
			switch request.Method {
			case "initialize":
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{}})
			case "initialized":
			case "thread/start":
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{
					"thread": map[string]any{"id": "thread_late"},
				}})
			case "turn/start":
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{
					"turn": map[string]any{"id": "turn_late", "status": "inProgress"},
				}})
				transport.send(map[string]any{"id": 911, "method": "item/tool/call", "params": map[string]any{
					"threadId": "thread_late", "turnId": "turn_late", "callId": "call_late_a",
					"tool": "lookup_value", "arguments": map[string]any{"key": "a"},
				}})
				go func() {
					<-releaseLateTool
					transport.send(map[string]any{"id": 912, "method": "item/tool/call", "params": map[string]any{
						"threadId": "thread_late", "turnId": "turn_late", "callId": "call_late_b",
						"tool": "lookup_value", "arguments": map[string]any{"key": "b"},
					}})
				}()
			case "":
				if request.Result["success"] != true {
					return fmt.Errorf("tool callback response = %#v", request)
				}
				switch request.ID {
				case 911:
				case 912:
					transport.send(map[string]any{"method": "item/started", "params": map[string]any{
						"threadId": "thread_late", "turnId": "turn_late",
						"item": map[string]any{"id": "answer_late", "type": "agentMessage", "phase": "final_answer"},
					}})
					transport.send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{
						"threadId": "thread_late", "turnId": "turn_late", "itemId": "answer_late",
						"delta": "A and B received",
					}})
					transport.send(map[string]any{"method": "turn/completed", "params": map[string]any{
						"threadId": "thread_late", "turn": map[string]any{"id": "turn_late", "status": "completed"},
					}})
					return nil
				default:
					return fmt.Errorf("unexpected callback response id %d", request.ID)
				}
			default:
				return errors.New("unexpected method: " + request.Method)
			}
		case <-time.After(5 * time.Second):
			return errors.New("timed out waiting for late delegated tool flow")
		}
	}
}

func serveSameTurnDelegatedTool(transport *fakeTransport) error {
	threadStarts := 0
	for {
		select {
		case data := <-transport.writes:
			var request struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
				Result map[string]any `json:"result"`
			}
			if err := json.Unmarshal(data, &request); err != nil {
				return err
			}
			switch request.Method {
			case "initialize":
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{}})
			case "initialized":
			case "thread/start":
				threadStarts++
				if threadStarts != 1 {
					return errors.New("delegated continuation created another thread")
				}
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{
					"thread": map[string]any{"id": "thread_same"},
				}})
			case "turn/start":
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{
					"turn": map[string]any{"id": "turn_same", "status": "inProgress"},
				}})
				transport.send(map[string]any{"id": 902, "method": "item/tool/call", "params": map[string]any{
					"threadId": "thread_same", "turnId": "turn_same", "callId": "call_same",
					"tool": "lookup_value", "arguments": map[string]any{"key": "demo"},
				}})
			case "":
				if request.ID != 902 || request.Result["success"] != true {
					return fmt.Errorf("tool callback response = %#v", request)
				}
				items, ok := request.Result["contentItems"].([]any)
				if !ok || len(items) != 1 || items[0].(map[string]any)["text"] != `{"value":"RESULT_42"}` {
					return fmt.Errorf("tool callback content = %#v", request.Result)
				}
				transport.send(map[string]any{"method": "item/started", "params": map[string]any{
					"threadId": "thread_same", "turnId": "turn_same",
					"item": map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer"},
				}})
				transport.send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{
					"threadId": "thread_same", "turnId": "turn_same", "itemId": "answer",
					"delta": "The result is RESULT_42",
				}})
				transport.send(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{
					"threadId": "thread_same", "turnId": "turn_same", "tokenUsage": map[string]any{
						"last": map[string]any{
							"inputTokens": 1200, "cachedInputTokens": 1000, "cacheWriteInputTokens": 20,
							"outputTokens": 30, "totalTokens": 1230,
						},
					},
				}})
				transport.send(map[string]any{"method": "turn/completed", "params": map[string]any{
					"threadId": "thread_same", "turn": map[string]any{"id": "turn_same", "status": "completed"},
				}})
				return nil
			default:
				return errors.New("unexpected method: " + request.Method)
			}
		case <-time.After(5 * time.Second):
			return errors.New("timed out waiting for same-turn delegated tool flow")
		}
	}
}

func TestDynamicToolsNamedChoiceFiltersTools(t *testing.T) {
	provider := New()
	tools, include, err := provider.dynamicTools(llmprovider.ChatRequest{
		Tools: []llmprovider.Tool{
			{Type: llmprovider.ToolTypeFunction, Function: llmprovider.FunctionDefinition{Name: "first"}},
			{Type: llmprovider.ToolTypeFunction, Function: llmprovider.FunctionDefinition{Name: "second"}},
		},
		ToolChoice: llmprovider.NamedToolChoice("second"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !include || len(tools) != 1 || tools[0]["name"] != "second" {
		t.Fatalf("tools = %#v, include = %v", tools, include)
	}
}

func serveDelegatedToolContinuation(transport *fakeTransport) error {
	for {
		select {
		case data := <-transport.writes:
			var request struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal(data, &request); err != nil {
				return err
			}
			switch request.Method {
			case "initialize":
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{}})
			case "initialized":
			case "thread/start":
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{
					"thread": map[string]any{"id": "thread_continuation"},
				}})
			case "thread/inject_items":
				items, ok := request.Params["items"].([]any)
				if !ok || len(items) != 3 {
					return fmt.Errorf("continuation items = %#v", request.Params["items"])
				}
				last := items[2].(map[string]any)
				if last["type"] != "function_call_output" || last["call_id"] != "call_delegate_1" {
					return fmt.Errorf("last continuation item = %#v", last)
				}
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{}})
			case "turn/start":
				input, ok := request.Params["input"].([]any)
				if !ok || len(input) != 1 ||
					input[0].(map[string]any)["text"] != "Continue the response using the supplied function result." {
					return fmt.Errorf("continuation input = %#v", request.Params["input"])
				}
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{
					"turn": map[string]any{"id": "turn_continuation", "status": "inProgress"},
				}})
				transport.send(map[string]any{"method": "item/started", "params": map[string]any{
					"threadId": "thread_continuation", "turnId": "turn_continuation",
					"item": map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer"},
				}})
				transport.send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{
					"threadId": "thread_continuation", "turnId": "turn_continuation",
					"itemId": "answer", "delta": "The result is RESULT_42",
				}})
				transport.send(map[string]any{"method": "turn/completed", "params": map[string]any{
					"threadId": "thread_continuation",
					"turn":     map[string]any{"id": "turn_continuation", "status": "completed"},
				}})
				return nil
			default:
				return errors.New("unexpected method: " + request.Method)
			}
		case <-time.After(5 * time.Second):
			return errors.New("timed out waiting for delegated continuation")
		}
	}
}
