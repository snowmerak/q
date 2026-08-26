package subagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestInvocationRuntimeCapturesInvocationAndDelegatesBaseTools(t *testing.T) {
	base := &stubInvocationBase{tools: []client.Tool{namedInvocationTool("base_tool")}}
	var captured client.ToolResult
	runtime, err := NewInvocationRuntime(base, func(
		_ context.Context,
		source InvocationSource,
		call client.ToolCall,
		result client.ToolResult,
	) (client.ToolResult, error) {
		if source.Protocol != "acp" || source.Name != "search" || call.Function.Name != ExternalSearchToolName {
			t.Fatalf("capture source=%#v call=%#v", source, call)
		}
		captured = result
		return client.ToolResult{Content: `{"loom_ref":"loom:test"}`}, nil
	}, Invocation{
		Tool:   ExternalSearchTool(),
		Source: InvocationSource{Protocol: "acp", Name: "search", Kind: "agent-result"},
		Handler: func(context.Context, client.ToolCall) (client.ToolResult, error) {
			return client.ToolResult{Content: `{"summary":"complete"}`}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{Name: ExternalSearchToolName}})
	if err != nil || !strings.Contains(result.Content, "loom_ref") || !strings.Contains(captured.Content, "complete") {
		t.Fatalf("captured=%#v result=%#v err=%v", captured, result, err)
	}
	result, err = runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{Name: "base_tool"}})
	if err != nil || result.Content != "base result" || base.calls != 1 {
		t.Fatalf("base result=%#v calls=%d err=%v", result, base.calls, err)
	}
}

func TestInvocationRuntimeCapturesHandlerErrors(t *testing.T) {
	var captured client.ToolResult
	runtime, err := NewInvocationRuntime(nil, func(
		_ context.Context,
		_ InvocationSource,
		_ client.ToolCall,
		result client.ToolResult,
	) (client.ToolResult, error) {
		captured = result
		return client.ToolResult{Content: "receipt", IsError: result.IsError}, nil
	}, Invocation{
		Tool:   namedInvocationTool("delegate"),
		Source: InvocationSource{Protocol: "q-subagent", Name: "scout"},
		Handler: func(context.Context, client.ToolCall) (client.ToolResult, error) {
			return client.ToolResult{}, errors.New("scout failed")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{Name: "delegate"}})
	if err != nil || !result.IsError || !captured.IsError || !strings.Contains(captured.Content, "scout failed") {
		t.Fatalf("captured=%#v result=%#v err=%v", captured, result, err)
	}
}

func TestInvocationRuntimeRejectsCollisionAndMissingCapture(t *testing.T) {
	base := &stubInvocationBase{tools: []client.Tool{namedInvocationTool("delegate")}}
	_, err := NewInvocationRuntime(base, nil, Invocation{
		Tool:    namedInvocationTool("delegate"),
		Source:  InvocationSource{Protocol: "q-subagent", Name: "scout"},
		Handler: func(context.Context, client.ToolCall) (client.ToolResult, error) { return client.ToolResult{}, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("collision error = %v", err)
	}

	runtime, err := NewInvocationRuntime(nil, nil, Invocation{
		Tool:    namedInvocationTool("delegate"),
		Source:  InvocationSource{Protocol: "q-subagent", Name: "scout"},
		Handler: func(context.Context, client.ToolCall) (client.ToolResult, error) { return client.ToolResult{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{Name: "delegate"}})
	if err == nil || !strings.Contains(err.Error(), "Loom capture") {
		t.Fatalf("missing capture error = %v", err)
	}
}

func TestInvocationRuntimeRejectsMissingCaptureSource(t *testing.T) {
	_, err := NewInvocationRuntime(nil, func(
		context.Context, InvocationSource, client.ToolCall, client.ToolResult,
	) (client.ToolResult, error) {
		return client.ToolResult{}, nil
	}, Invocation{
		Tool:    namedInvocationTool("delegate"),
		Handler: func(context.Context, client.ToolCall) (client.ToolResult, error) { return client.ToolResult{}, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "capture protocol") {
		t.Fatalf("missing source error = %v", err)
	}
}

type stubInvocationBase struct {
	tools []client.Tool
	calls int
}

func (s *stubInvocationBase) Tools() []client.Tool { return s.tools }

func (s *stubInvocationBase) Call(context.Context, client.ToolCall) (client.ToolResult, error) {
	s.calls++
	return client.ToolResult{Content: "base result"}, nil
}

func namedInvocationTool(name string) client.Tool {
	return client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: name}}
}
