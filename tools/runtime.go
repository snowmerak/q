package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/client"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/lsp"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/tools/builtin"
)

const maximumInlineToolResult = 32 << 10

type HostEnvironment struct {
	OS           string
	Architecture string
	Shell        string
}

// CaptureSource describes the transport and artifact identity for a result
// that enters q through a tool-shaped runtime boundary.
type CaptureSource struct {
	Protocol  string
	Name      string
	Kind      string
	MediaType string
}

// Runtime connects q's chat loop to the builtin MCP server in-process.
type Runtime struct {
	client         *mcp.ClientSession
	server         *mcp.ServerSession
	fs             *builtin.FS
	loom           *builtin.LoomRuntime
	lsp            *lsp.Manager
	skills         *agentskills.Registry
	skillStore     agentskills.RecordStore
	globalSkills   builtin.GlobalSkillLibrary
	tools          []client.Tool
	externalMu     sync.RWMutex
	external       map[string]*externalServer
	externalRoutes map[string]externalToolRoute
	externalConfig mcpconfig.Config
	closeMu        sync.Once
}

func NewRuntime(ctx context.Context, root string) (*Runtime, error) {
	return NewRuntimeWithArchive(ctx, root, nil)
}

// NewRuntimeWithArchive connects the chat loop to filesystem, command,
// workspace archive, and Loom tools. The caller owns the archive lifetime.
func NewRuntimeWithArchive(ctx context.Context, root string, archive builtin.Archive) (*Runtime, error) {
	return NewRuntimeWithArchiveAndLoomOptions(ctx, root, archive, loom.StoreOptions{})
}

func NewRuntimeWithArchiveAndLoomOptions(
	ctx context.Context,
	root string,
	archive builtin.Archive,
	options loom.StoreOptions,
) (*Runtime, error) {
	return newRuntime(ctx, root, archive, loom.NewProcessEvaluator(), options)
}

// NewRuntimeWithArchiveAndLoomOptionsAndLSP adds the workspace's configured
// read-only language-server query tools to the in-process MCP runtime.
func NewRuntimeWithArchiveAndLoomOptionsAndLSP(
	ctx context.Context,
	root string,
	archive builtin.Archive,
	options loom.StoreOptions,
	global lsp.GlobalConfig,
	workspace lsp.WorkspaceConfig,
) (*Runtime, error) {
	manager, err := lsp.NewManager(ctx, root, global, workspace)
	if err != nil {
		return nil, err
	}
	runtime, err := newRuntimeWithLSP(ctx, root, archive, loom.NewProcessEvaluator(), options, manager, nil)
	if err != nil {
		_ = manager.Close()
		return nil, err
	}
	return runtime, nil
}

// NewRuntimeWithArchiveAndLoomOptionsAndLSPAndLibrary routes global Agent
// Skills through the shared Library while keeping project skills local.
func NewRuntimeWithArchiveAndLoomOptionsAndLSPAndLibrary(
	ctx context.Context,
	root string,
	archive builtin.Archive,
	options loom.StoreOptions,
	global lsp.GlobalConfig,
	workspace lsp.WorkspaceConfig,
	globalSkills builtin.GlobalSkillLibrary,
) (*Runtime, error) {
	manager, err := lsp.NewManager(ctx, root, global, workspace)
	if err != nil {
		return nil, err
	}
	runtime, err := newRuntimeWithLSP(ctx, root, archive, loom.NewProcessEvaluator(), options, manager, globalSkills)
	if err != nil {
		_ = manager.Close()
		return nil, err
	}
	return runtime, nil
}

func newRuntime(ctx context.Context, root string, archive builtin.Archive, evaluator loom.Evaluator, options loom.StoreOptions) (*Runtime, error) {
	return newRuntimeWithLSP(ctx, root, archive, evaluator, options, nil, nil)
}

func newRuntimeWithLSP(ctx context.Context, root string, archive builtin.Archive, evaluator loom.Evaluator, options loom.StoreOptions, lspManager *lsp.Manager, globalSkills builtin.GlobalSkillLibrary) (*Runtime, error) {
	loomRuntime, err := newLoomRuntime(root, evaluator, withSessionRoots(options, root))
	if err != nil {
		return nil, err
	}
	server, fs, skills, err := newServer(root, archive, loomRuntime, lspManager, globalSkills)
	if err != nil {
		return nil, err
	}
	builtin.RegisterLearning(server)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		fs.Close()
		return nil, fmt.Errorf("tools: connect builtin MCP server: %w", err)
	}
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "q", Version: ServerVersion}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		fs.Close()
		return nil, fmt.Errorf("tools: connect builtin MCP client: %w", err)
	}
	store, _ := archive.(agentskills.RecordStore)
	runtime := &Runtime{
		client: clientSession, server: serverSession, fs: fs, loom: loomRuntime, lsp: lspManager,
		skills: skills, skillStore: store, globalSkills: globalSkills,
	}
	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("tools: list builtin tools: %w", err)
	}
	runtime.tools = make([]client.Tool, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		parameters, err := schemaObject(tool.InputSchema)
		if err != nil {
			_ = runtime.Close()
			return nil, fmt.Errorf("tools: encode schema for %q: %w", tool.Name, err)
		}
		runtime.tools = append(runtime.tools, client.Tool{
			Type: client.ToolTypeFunction,
			Function: client.FunctionDefinition{
				Name: tool.Name, Description: tool.Description, Parameters: parameters,
			},
		})
	}
	sort.Slice(runtime.tools, func(i, j int) bool {
		return runtime.tools[i].Function.Name < runtime.tools[j].Function.Name
	})
	return runtime, nil
}

func (r *Runtime) ConfigureLoom(options loom.StoreOptions) error {
	if r == nil || r.loom == nil || r.loom.Store == nil {
		return errors.New("tools: Loom runtime is unavailable")
	}
	if options.Roots == nil {
		options.Roots = r.loom.Store.Options().Roots
	}
	return r.loom.Store.Configure(options)
}

func (r *Runtime) LoomStats(ctx context.Context) (loom.Stats, error) {
	if r == nil || r.loom == nil || r.loom.Store == nil {
		return loom.Stats{}, errors.New("tools: Loom runtime is unavailable")
	}
	return r.loom.Store.Stats(ctx)
}

func (r *Runtime) CollectLoom(ctx context.Context, dryRun bool) (loom.GCResult, error) {
	if r == nil || r.loom == nil || r.loom.Store == nil {
		return loom.GCResult{}, errors.New("tools: Loom runtime is unavailable")
	}
	options := r.loom.Store.Options()
	return r.loom.Store.CollectWithRootProvider(ctx, options.Roots, loom.GCOptions{
		ProtectNewerThan: time.Now().UTC().Add(-options.GCGracePeriod), DryRun: dryRun,
	})
}

func (r *Runtime) Tools() []client.Tool {
	return append([]client.Tool(nil), r.tools...)
}

// ToolsForRole returns builtin tools plus external MCP tools assigned to role.
func (r *Runtime) ToolsForRole(role string) []client.Tool {
	result := append([]client.Tool(nil), r.tools...)
	r.externalMu.RLock()
	defer r.externalMu.RUnlock()
	for _, serverID := range r.externalConfig.ServersForRole(role) {
		server := r.external[serverID]
		if server != nil {
			result = append(result, server.tools...)
		}
	}
	return result
}

func (r *Runtime) Skills() []agentskills.Skill {
	if r == nil || r.skills == nil {
		return nil
	}
	return r.skills.Skills()
}

func (r *Runtime) SkillEntries() []agentskills.Skill {
	if r == nil || r.skills == nil {
		return nil
	}
	return r.skills.Entries()
}

func (r *Runtime) SkillIssues() []agentskills.Issue {
	if r == nil || r.skills == nil {
		return nil
	}
	return r.skills.Issues()
}

func (r *Runtime) ReloadSkills() error {
	if r == nil || r.skills == nil {
		return errors.New("tools: Agent Skills registry is unavailable")
	}
	if err := r.skills.Reload(); err != nil {
		return err
	}
	if err := r.syncProjectSkills(context.Background()); err != nil {
		return err
	}
	return r.reloadGlobalSkills(context.Background())
}

func (r *Runtime) InstallSkill(ctx context.Context, scope, repository string) (agentskills.Skill, error) {
	if r == nil || r.skills == nil {
		return agentskills.Skill{}, errors.New("tools: Agent Skills registry is unavailable")
	}
	skill, err := r.skills.InstallGit(ctx, scope, repository)
	if err != nil {
		return agentskills.Skill{}, err
	}
	if err := r.syncProjectSkills(ctx); err != nil {
		return agentskills.Skill{}, err
	}
	if skill.Scope == "global" {
		if err := r.reloadGlobalSkills(ctx); err != nil {
			return agentskills.Skill{}, err
		}
	}
	return skill, nil
}

func (r *Runtime) UpdateSkill(ctx context.Context, idOrName string) (agentskills.Skill, error) {
	if r == nil || r.skills == nil {
		return agentskills.Skill{}, errors.New("tools: Agent Skills registry is unavailable")
	}
	skill, err := r.skills.UpdateGit(ctx, idOrName)
	if err != nil {
		return agentskills.Skill{}, err
	}
	if err := r.syncProjectSkills(ctx); err != nil {
		return agentskills.Skill{}, err
	}
	if skill.Scope == "global" {
		if err := r.reloadGlobalSkills(ctx); err != nil {
			return agentskills.Skill{}, err
		}
	}
	return skill, nil
}

func (r *Runtime) RemoveSkill(ctx context.Context, idOrName string) (agentskills.Skill, error) {
	if r == nil || r.skills == nil {
		return agentskills.Skill{}, errors.New("tools: Agent Skills registry is unavailable")
	}
	skill, err := r.skills.RemoveGit(idOrName)
	if err != nil {
		return agentskills.Skill{}, err
	}
	if err := r.syncProjectSkills(ctx); err != nil {
		return agentskills.Skill{}, err
	}
	if skill.Scope == "global" {
		if err := r.reloadGlobalSkills(ctx); err != nil {
			return agentskills.Skill{}, err
		}
	}
	return skill, nil
}

func (r *Runtime) syncProjectSkills(ctx context.Context) error {
	if r.skillStore == nil {
		return nil
	}
	if r.globalSkills == nil {
		return r.skills.SyncRecords(ctx, r.skillStore)
	}
	return r.skills.SyncRecordsForScopes(ctx, r.skillStore, "project")
}

func (r *Runtime) reloadGlobalSkills(ctx context.Context) error {
	if r.globalSkills == nil {
		return nil
	}
	reloader, ok := r.globalSkills.(interface {
		ReloadSkills(context.Context) (qlibrary.SkillReloadResponse, error)
	})
	if !ok {
		return nil
	}
	_, err := reloader.ReloadSkills(ctx)
	return err
}
func (r *Runtime) Environment() HostEnvironment {
	return HostEnvironment{
		OS: goruntime.GOOS, Architecture: goruntime.GOARCH,
		Shell: builtin.CommandShellDescription(),
	}
}

func (r *Runtime) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	arguments := make(map[string]any)
	if strings.TrimSpace(call.Function.Arguments) != "" {
		if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
			return client.ToolResult{Content: "invalid tool arguments: " + err.Error(), IsError: true}, nil
		}
	}
	r.externalMu.RLock()
	route, external := r.externalRoutes[call.Function.Name]
	if external {
		defer r.externalMu.RUnlock()
		callContext, cancel := context.WithTimeout(ctx, externalCallTimeout)
		defer cancel()
		result, err := route.session.CallTool(callContext, &mcp.CallToolParams{Name: route.tool, Arguments: arguments})
		if err != nil {
			return client.ToolResult{}, err
		}
		return CaptureMCPToolResult(ctx, r.loom.Store, route.server, call, result)
	}
	r.externalMu.RUnlock()
	result, err := r.client.CallTool(ctx, &mcp.CallToolParams{Name: call.Function.Name, Arguments: arguments})
	if err != nil {
		return client.ToolResult{}, err
	}
	return CaptureMCPToolResult(ctx, r.loom.Store, ServerName, call, result)
}

// CaptureResult stores a non-MCP runtime result in this workspace's shared
// Loom and returns the same bounded receipt used by ordinary tools.
func (r *Runtime) CaptureResult(
	ctx context.Context,
	source CaptureSource,
	call client.ToolCall,
	result client.ToolResult,
) (client.ToolResult, error) {
	if r == nil || r.loom == nil || r.loom.Store == nil {
		return client.ToolResult{}, errors.New("tools: Loom runtime is unavailable")
	}
	return CaptureResult(ctx, r.loom.Store, source, call, result)
}

// CaptureMCPToolResult captures a raw MCP result and returns the bounded Loom
// receipt that should be sent to the model. Builtin and custom MCP dispatchers
// share this post-CallTool boundary.
func CaptureMCPToolResult(
	ctx context.Context,
	store *loom.Store,
	server string,
	call client.ToolCall,
	result *mcp.CallToolResult,
) (client.ToolResult, error) {
	if result == nil {
		return client.ToolResult{}, errors.New("tools: Loom capture requires an MCP result")
	}
	content, err := toolResultText(result)
	if err != nil {
		return client.ToolResult{}, err
	}
	if strings.HasPrefix(call.Function.Name, "loom_") || (server == ServerName && call.Function.Name == "get_skill") {
		return client.ToolResult{Content: content, IsError: result.IsError}, nil
	}
	artifact, err := CaptureMCPResult(ctx, store, server, call, result)
	if err != nil {
		return client.ToolResult{}, err
	}
	receipt, err := encodeLoomReceipt(artifact, content)
	if err != nil {
		return client.ToolResult{}, err
	}
	return client.ToolResult{Content: receipt, IsError: result.IsError}, nil
}

// CaptureToolResult applies the same Loom capture boundary to tool runtimes
// that already expose q's common client.ToolResult shape. Registering tools
// behind one of these runtimes makes capture automatic for every non-Loom
// result dispatched through it.
func CaptureToolResult(
	ctx context.Context,
	store *loom.Store,
	server string,
	call client.ToolCall,
	result client.ToolResult,
) (client.ToolResult, error) {
	return CaptureResult(ctx, store, CaptureSource{
		Protocol: "mcp", Name: server, Kind: "mcp-result", MediaType: "application/vnd.q.mcp-result+json",
	}, call, result)
}

// CaptureResult applies the common Loom capture and receipt policy to a
// tool-shaped result produced by any protocol.
func CaptureResult(
	ctx context.Context,
	store *loom.Store,
	source CaptureSource,
	call client.ToolCall,
	result client.ToolResult,
) (client.ToolResult, error) {
	if strings.HasPrefix(call.Function.Name, "loom_") {
		return result, nil
	}
	if store == nil || strings.TrimSpace(source.Protocol) == "" || strings.TrimSpace(source.Name) == "" {
		return client.ToolResult{}, errors.New("tools: Loom capture requires a store, protocol, and source name")
	}
	if strings.TrimSpace(source.Kind) == "" {
		source.Kind = "tool-result"
	}
	if strings.TrimSpace(source.MediaType) == "" {
		source.MediaType = "application/vnd.q.tool-result+json"
	}
	envelope := capturedToolResult{
		Protocol: source.Protocol, Tool: call.Function.Name, CallID: call.ID,
		IsError: result.IsError,
	}
	if source.Protocol == "mcp" {
		envelope.Server = source.Name
	} else {
		envelope.Source = source.Name
	}
	if err := json.Unmarshal([]byte(result.Content), &envelope.Structured); err != nil {
		envelope.Content = result.Content
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return client.ToolResult{}, fmt.Errorf("tools: encode tool result for Loom: %w", err)
	}
	artifactSource := map[string]string{
		"protocol": source.Protocol, "source": source.Name,
		"tool": call.Function.Name, "call_id": call.ID,
	}
	if source.Protocol == "mcp" {
		artifactSource["server"] = source.Name
	}
	artifact, err := store.Put(ctx, body, loom.PutOptions{
		Kind: source.Kind, MediaType: source.MediaType,
		Source: artifactSource,
	})
	if err != nil {
		return client.ToolResult{}, fmt.Errorf("tools: store tool result in Loom: %w", err)
	}
	receipt, err := encodeLoomReceipt(artifact, result.Content)
	if err != nil {
		return client.ToolResult{}, err
	}
	return client.ToolResult{Content: receipt, IsError: result.IsError}, nil
}

type capturedToolResult struct {
	Protocol   string `json:"protocol"`
	Server     string `json:"server,omitempty"`
	Source     string `json:"source,omitempty"`
	Tool       string `json:"tool"`
	CallID     string `json:"call_id"`
	IsError    bool   `json:"is_error"`
	Structured any    `json:"structured,omitempty"`
	Content    any    `json:"content,omitempty"`
}

type loomReceipt struct {
	LoomRef loom.Ref `json:"loom_ref"`
	Kind    string   `json:"kind"`
	Bytes   int64    `json:"bytes"`
	Digest  string   `json:"digest"`
	Stored  bool     `json:"stored"`
	Result  any      `json:"result,omitempty"`
	Preview string   `json:"preview,omitempty"`
}

// CaptureMCPResult stores a raw result before it is flattened for a model.
// Builtin and future external MCP dispatchers use the same capture boundary.
func CaptureMCPResult(
	ctx context.Context,
	store *loom.Store,
	server string,
	call client.ToolCall,
	result *mcp.CallToolResult,
) (loom.Artifact, error) {
	if store == nil || result == nil || strings.TrimSpace(server) == "" {
		return loom.Artifact{}, errors.New("tools: Loom capture requires a store, server, and result")
	}
	envelope := capturedToolResult{
		Protocol: "mcp", Server: server, Tool: call.Function.Name, CallID: call.ID,
		IsError: result.IsError, Structured: result.StructuredContent, Content: result.Content,
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return loom.Artifact{}, fmt.Errorf("tools: encode MCP result for Loom: %w", err)
	}
	artifact, err := store.Put(ctx, body, loom.PutOptions{
		Kind: "mcp-result", MediaType: "application/vnd.q.mcp-result+json",
		Source: map[string]string{"server": server, "tool": call.Function.Name, "call_id": call.ID},
	})
	if err != nil {
		return loom.Artifact{}, fmt.Errorf("tools: store MCP result in Loom: %w", err)
	}
	return artifact, nil
}

func encodeLoomReceipt(artifact loom.Artifact, content string) (string, error) {
	receipt := loomReceipt{
		LoomRef: artifact.Ref, Kind: artifact.Kind, Bytes: artifact.Bytes,
		Digest: artifact.Digest, Stored: true,
	}
	if len(content) <= maximumInlineToolResult {
		if err := json.Unmarshal([]byte(content), &receipt.Result); err != nil {
			receipt.Result = content
		}
	} else {
		runes := []rune(content)
		if len(runes) > 4096 {
			runes = runes[:4096]
		}
		receipt.Preview = string(runes)
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return "", fmt.Errorf("tools: encode Loom receipt: %w", err)
	}
	return string(body), nil
}

func (r *Runtime) Close() error {
	var closeErr error
	r.closeMu.Do(func() {
		r.externalMu.Lock()
		for _, server := range r.external {
			closeErr = errors.Join(closeErr, server.session.Close())
		}
		r.external = nil
		r.externalRoutes = nil
		r.externalMu.Unlock()
		if r.client != nil {
			closeErr = errors.Join(closeErr, r.client.Close())
		}
		if r.server != nil {
			if err := r.server.Close(); closeErr == nil {
				closeErr = err
			}
		}
		if r.fs != nil {
			r.fs.Close()
		}
		if r.lsp != nil {
			closeErr = errors.Join(closeErr, r.lsp.Close())
		}
	})
	return closeErr
}

func schemaObject(schema any) (map[string]any, error) {
	body, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, err
	}
	if object["type"] == "object" {
		if properties, found := object["properties"]; !found || properties == nil {
			object["properties"] = map[string]any{}
		}
	}
	return object, nil
}

func toolResultText(result *mcp.CallToolResult) (string, error) {
	if result.StructuredContent != nil {
		body, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
			continue
		}
		body, err := json.Marshal(content)
		if err != nil {
			return "", err
		}
		parts = append(parts, string(body))
	}
	return strings.Join(parts, "\n"), nil
}
