package subagent

import (
	"context"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

type repeatedSkillRuntime struct{}

func (repeatedSkillRuntime) Tools() []client.Tool {
	return []client.Tool{scoutFunctionTool("get_skill")}
}

func (repeatedSkillRuntime) Call(context.Context, client.ToolCall) (client.ToolResult, error) {
	return client.ToolResult{Content: `{"skill":{"id":"one","digest":"v1"},"path":"SKILL.md","content":"Exact skill body"}`}, nil
}

func TestGeneralWorkerCheckpointsShortSkillReferenceInCurrentContext(t *testing.T) {
	runtime := repeatedSkillRuntime{}
	call := func(id string) client.ToolCall {
		return client.ToolCall{ID: id, Function: client.FunctionCall{Name: "get_skill", Arguments: `{"id":"one"}`}}
	}
	first := call("first")
	full, _ := runtime.Call(t.Context(), first)
	messages := []client.Message{{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{first}}, client.ToolResultMessage(first, full), {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call("second")}}}
	state := GeneralRunState{Transcript: messages, Context: messages, Started: true}
	history := NewContextCompactor(Spec{}, messages, runtime.Tools(), 0)
	runner := GeneralRunner{Tools: runtime, Definition: AgentDefinition{Info: DelegateInfo{Name: "worker"}}}
	started, saved := true, false
	_, done, err := runner.completeGeneralCalls(t.Context(), &state, history, runtime.Tools(), "task", nil, &started, func() error {
		last := state.Transcript[len(state.Transcript)-1]
		saved = strings.Contains(last.Content, `"previous_tool_call_id":"first"`) && !strings.Contains(last.Content, "Exact skill body")
		return nil
	})
	if err != nil || done || !saved {
		t.Fatalf("saved=%v done=%v error=%v", saved, done, err)
	}
	last := history.Messages()[len(history.Messages())-1]
	if last.ToolCallID != "second" || !strings.Contains(last.Content, `"already_in_context":true`) {
		t.Fatalf("wrong worker context: %+v", last)
	}
}

func TestInnerRoleContextAlsoReusesUnchangedSkillBodies(t *testing.T) {
	runtime := repeatedSkillRuntime{}
	history := NewContextCompactor(Spec{}, nil, runtime.Tools(), 0)
	for _, id := range []string{"first", "second"} {
		call := client.ToolCall{ID: id, Function: client.FunctionCall{Name: "get_skill", Arguments: `{"id":"one"}`}}
		result, _ := runtime.Call(t.Context(), call)
		history.Append(client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}}, client.ToolResultMessage(call, result))
	}
	messages := history.Messages()
	if !strings.Contains(messages[1].Content, "Exact skill body") || strings.Contains(messages[3].Content, "Exact skill body") || !strings.Contains(messages[3].Content, `"previous_tool_call_id":"first"`) {
		t.Fatalf("inner-role context did not reuse skill body: %+v", messages)
	}
}
