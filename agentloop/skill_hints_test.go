package agentloop

import (
	"context"
	"fmt"
	"testing"

	"github.com/snowmerak/q/client"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/tools/builtin"
)

type skillHintRuntime struct {
	result builtin.SearchSkillsOutput
	limit  int
}

func (*skillHintRuntime) Tools() []client.Tool {
	return []client.Tool{
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "search_skills"}},
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "get_skill"}},
	}
}

func (*skillHintRuntime) Environment() qtools.HostEnvironment { return qtools.HostEnvironment{} }

func (*skillHintRuntime) Call(context.Context, client.ToolCall) (client.ToolResult, error) {
	return client.ToolResult{}, nil
}

func (r *skillHintRuntime) SearchSkillHints(_ context.Context, _ string, limit int) (builtin.SearchSkillsOutput, error) {
	r.limit = limit
	return r.result, nil
}

func TestAutomaticSkillHintLimits(t *testing.T) {
	runtime := &skillHintRuntime{}
	for index := range 12 {
		runtime.result.Hits = append(runtime.result.Hits, builtin.SkillSearchHit{
			ID: fmt.Sprintf("skill-%d", index), Title: fmt.Sprintf("Skill %d", index),
		})
	}
	for _, test := range []struct {
		trigger  string
		reranked bool
		search   int
		hints    int
	}{
		{trigger: "task_start", reranked: true, search: 12, hints: 8},
		{trigger: "task_start", search: 12, hints: 6},
		{trigger: "user_input", reranked: true, search: 8, hints: 6},
		{trigger: "user_input", search: 8, hints: 4},
		{trigger: "ask_to_user", reranked: true, search: 8, hints: 6},
		{trigger: "ask_to_user", search: 8, hints: 4},
	} {
		t.Run(fmt.Sprintf("%s/reranked=%t", test.trigger, test.reranked), func(t *testing.T) {
			runtime.result.Reranked = test.reranked
			hints := automaticSkillHints(t.Context(), runtime, "review Go code", test.trigger, map[string]struct{}{})
			if runtime.limit != test.search || hints == nil || len(hints.Candidates) != test.hints {
				t.Fatalf("search limit = %d, hints = %#v", runtime.limit, hints)
			}
		})
	}
}
