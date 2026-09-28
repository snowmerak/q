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

func (r *skillHintRuntime) SearchSkillHints(context.Context, string, int) (builtin.SearchSkillsOutput, error) {
	return r.result, nil
}

func TestAutomaticSkillHintsKeepsSixSystemOneResults(t *testing.T) {
	runtime := &skillHintRuntime{result: builtin.SearchSkillsOutput{Reranked: true}}
	for index := range 8 {
		runtime.result.Hits = append(runtime.result.Hits, builtin.SkillSearchHit{
			ID: fmt.Sprintf("skill-%d", index), Title: fmt.Sprintf("Skill %d", index),
		})
	}
	hints := automaticSkillHints(t.Context(), runtime, "review Go code", "user_input", map[string]struct{}{})
	if hints == nil || len(hints.Candidates) != 6 {
		t.Fatalf("System One hints = %#v", hints)
	}

	runtime.result.Reranked = false
	hints = automaticSkillHints(t.Context(), runtime, "review Go code", "user_input", map[string]struct{}{})
	if hints == nil || len(hints.Candidates) != 4 {
		t.Fatalf("ordinary hints = %#v", hints)
	}
}
