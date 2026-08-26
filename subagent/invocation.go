package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
)

type InvocationSource struct {
	Protocol  string
	Name      string
	Kind      string
	MediaType string
}

type InvocationCaptureFunc func(
	context.Context,
	InvocationSource,
	client.ToolCall,
	client.ToolResult,
) (client.ToolResult, error)

type InvocationHandler func(context.Context, client.ToolCall) (client.ToolResult, error)

type Invocation struct {
	Tool    client.Tool
	Source  InvocationSource
	Handler InvocationHandler
}

// InvocationRuntime decorates an existing tool runtime with model-visible
// subagent calls. Every invocation result is captured before it is returned.
type InvocationRuntime struct {
	base        ToolRuntime
	capture     InvocationCaptureFunc
	tools       []client.Tool
	invocations map[string]Invocation
}

func NewInvocationRuntime(
	base ToolRuntime,
	capture InvocationCaptureFunc,
	invocations ...Invocation,
) (*InvocationRuntime, error) {
	runtime := &InvocationRuntime{
		base: base, capture: capture, invocations: make(map[string]Invocation, len(invocations)),
	}
	seen := make(map[string]struct{})
	if base != nil {
		runtime.tools = append(runtime.tools, base.Tools()...)
		for _, tool := range runtime.tools {
			seen[tool.Function.Name] = struct{}{}
		}
	}
	for _, invocation := range invocations {
		name := strings.TrimSpace(invocation.Tool.Function.Name)
		if name == "" || invocation.Handler == nil {
			return nil, errors.New("subagent: invocation requires a named tool and handler")
		}
		if strings.TrimSpace(invocation.Source.Protocol) == "" || strings.TrimSpace(invocation.Source.Name) == "" {
			return nil, fmt.Errorf("subagent: invocation %q requires a capture protocol and source name", name)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("subagent: invocation tool name collision %q", name)
		}
		seen[name] = struct{}{}
		runtime.tools = append(runtime.tools, invocation.Tool)
		runtime.invocations[name] = invocation
	}
	return runtime, nil
}

func (r *InvocationRuntime) Tools() []client.Tool {
	if r == nil {
		return nil
	}
	return append([]client.Tool(nil), r.tools...)
}

func (r *InvocationRuntime) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	if r == nil {
		return client.ToolResult{}, errors.New("subagent: invocation runtime is unavailable")
	}
	invocation, found := r.invocations[call.Function.Name]
	if !found {
		if r.base == nil {
			return client.ToolResult{}, fmt.Errorf("subagent: unknown tool %q", call.Function.Name)
		}
		return r.base.Call(ctx, call)
	}
	if r.capture == nil {
		return client.ToolResult{}, errors.New("subagent: invocation Loom capture is unavailable")
	}
	result, err := invocation.Handler(ctx, call)
	if err != nil {
		body, marshalErr := json.Marshal(map[string]string{"error": err.Error()})
		if marshalErr != nil {
			return client.ToolResult{}, marshalErr
		}
		result = client.ToolResult{Content: string(body), IsError: true}
	}
	return r.capture(ctx, invocation.Source, call, result)
}
