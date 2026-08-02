package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/tools/builtin"
)

// Runtime connects q's chat loop to the builtin MCP server in-process.
type Runtime struct {
	client  *mcp.ClientSession
	server  *mcp.ServerSession
	fs      *builtin.FS
	tools   []client.Tool
	closeMu sync.Once
}

func NewRuntime(ctx context.Context, root string) (*Runtime, error) {
	server, fs, err := newServer(root)
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
	runtime := &Runtime{client: clientSession, server: serverSession, fs: fs}
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

func (r *Runtime) Tools() []client.Tool {
	return append([]client.Tool(nil), r.tools...)
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
	content, err := toolResultText(result)
	if err != nil {
		return client.ToolResult{}, err
	}
	return client.ToolResult{Content: content, IsError: result.IsError}, nil
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
