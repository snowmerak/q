package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/tools/builtin"
)

const maximumInlineToolResult = 32 << 10

type HostEnvironment struct {
	OS           string
	Architecture string
	Shell        string
}

// Runtime connects q's chat loop to the builtin MCP server in-process.
type Runtime struct {
	client  *mcp.ClientSession
	server  *mcp.ServerSession
	fs      *builtin.FS
	loom    *builtin.LoomRuntime
	tools   []client.Tool
	closeMu sync.Once
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

func newRuntime(ctx context.Context, root string, archive builtin.Archive, evaluator loom.Evaluator, options loom.StoreOptions) (*Runtime, error) {
	loomRuntime, err := newLoomRuntime(root, evaluator, withSessionRoots(options, root))
	if err != nil {
		return nil, err
	}
	server, fs, err := newServer(root, archive, loomRuntime)
	if err != nil {
		return nil, err
	}
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
	runtime := &Runtime{client: clientSession, server: serverSession, fs: fs, loom: loomRuntime}
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
	var roots []loom.Ref
	if options.Roots != nil {
		var err error
		roots, err = options.Roots(ctx)
		if err != nil {
			return loom.GCResult{}, err
		}
	}
	return r.loom.Store.Collect(ctx, loom.GCOptions{
		Roots: roots, ProtectNewerThan: time.Now().UTC().Add(-options.GCGracePeriod), DryRun: dryRun,
	})
}

func (r *Runtime) Tools() []client.Tool {
	return append([]client.Tool(nil), r.tools...)
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
	result, err := r.client.CallTool(ctx, &mcp.CallToolParams{Name: call.Function.Name, Arguments: arguments})
	if err != nil {
		return client.ToolResult{}, err
	}
	return CaptureMCPToolResult(ctx, r.loom.Store, ServerName, call, result)
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
	if strings.HasPrefix(call.Function.Name, "loom_") {
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
	if strings.HasPrefix(call.Function.Name, "loom_") {
		return result, nil
	}
	if store == nil || strings.TrimSpace(server) == "" {
		return client.ToolResult{}, errors.New("tools: Loom capture requires a store and server")
	}
	envelope := capturedMCPResult{
		Protocol: "mcp", Server: server, Tool: call.Function.Name, CallID: call.ID,
		IsError: result.IsError,
	}
	if err := json.Unmarshal([]byte(result.Content), &envelope.Structured); err != nil {
		envelope.Content = result.Content
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return client.ToolResult{}, fmt.Errorf("tools: encode tool result for Loom: %w", err)
	}
	artifact, err := store.Put(ctx, body, loom.PutOptions{
		Kind: "mcp-result", MediaType: "application/vnd.q.mcp-result+json",
		Source: map[string]string{"server": server, "tool": call.Function.Name, "call_id": call.ID},
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

type capturedMCPResult struct {
	Protocol   string `json:"protocol"`
	Server     string `json:"server"`
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
	envelope := capturedMCPResult{
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
		if r.client != nil {
			closeErr = r.client.Close()
		}
		if r.server != nil {
			if err := r.server.Close(); closeErr == nil {
				closeErr = err
			}
		}
		if r.fs != nil {
			r.fs.Close()
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
