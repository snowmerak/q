package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/subagent"
)

type failingExternalAgentTools struct {
	fakeAgentTools
	name   string
	err    error
	result client.ToolResult
	called bool
}

func (runtime *failingExternalAgentTools) Tools() []client.Tool {
	return []client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: runtime.name}}}
}

func (runtime *failingExternalAgentTools) Call(context.Context, client.ToolCall) (client.ToolResult, error) {
	runtime.called = true
	return runtime.result, runtime.err
}

func TestStreamExternalAgentFailureAndContinuation(t *testing.T) {
	callErr := errors.New("agent process stopped")
	for _, command := range []externalAgentCommand{searchAgentCommand, webTesterAgentCommand} {
		for _, scenario := range []string{"unavailable", "call failure", "tool error"} {
			t.Run(command.agentName+"/"+scenario, func(t *testing.T) {
				runtime := &failingExternalAgentTools{name: command.toolName}
				switch scenario {
				case "unavailable":
					runtime.name = "other_tool"
				case "call failure":
					runtime.err = callErr
				case "tool error":
					runtime.result = client.ToolResult{Content: "captured failure receipt", IsError: true}
				}
				parent := &fakeClient{}
				events := make(chan agentEvent, 16)
				streamExternalAgent(command, t.Context(), runtime, "inspect the application", "call-1", externalAgentParent{
					client: parent, model: "main-model",
				}, events)
				var activities []agentActivity
				var failure error
				var toolMessage *client.Message
				var toolIsError bool
				for event := range events {
					if event.activity != nil {
						activities = append(activities, *event.activity)
					}
					if event.err != nil {
						failure = event.err
					}
					if event.message != nil && event.message.Role == client.RoleTool {
						toolMessage, toolIsError = event.message, event.toolIsError
					}
				}
				switch scenario {
				case "unavailable":
					if failure == nil || failure.Error() != command.errorText || runtime.called || len(parent.requests) != 0 {
						t.Fatalf("failure=%v called=%v parent requests=%d", failure, runtime.called, len(parent.requests))
					}
				case "call failure":
					if !errors.Is(failure, callErr) || len(parent.requests) != 0 || toolMessage != nil {
						t.Fatalf("failure=%v parent requests=%d tool message=%v", failure, len(parent.requests), toolMessage)
					}
				case "tool error":
					if failure != nil || len(parent.requests) != 1 || toolMessage == nil || !toolIsError ||
						toolMessage.ToolCallID != "call-1" || !strings.HasPrefix(toolMessage.Content, "Tool error: ") {
						t.Fatalf("failure=%v parent requests=%d tool message=%v error=%v", failure, len(parent.requests), toolMessage, toolIsError)
					}
				}
				if scenario != "unavailable" && (len(activities) != 2 || activities[0].Agent != command.agentName ||
					activities[0].Action != subagent.ProgressStarted || activities[1].Action != subagent.ProgressFailed) {
					t.Fatalf("activities = %#v", activities)
				}
			})
		}
	}
}

func TestStreamExternalAgentCancellationClosesEventsBeforeCallingTool(t *testing.T) {
	for _, command := range []externalAgentCommand{searchAgentCommand, webTesterAgentCommand} {
		t.Run(command.agentName, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			runtime := &failingExternalAgentTools{name: command.toolName}
			events := make(chan agentEvent)
			streamExternalAgent(command, ctx, runtime, "inspect", "call-1", externalAgentParent{}, events)
			if _, open := <-events; open || runtime.called {
				t.Fatalf("events open=%v tool called=%v", open, runtime.called)
			}
		})
	}
}
