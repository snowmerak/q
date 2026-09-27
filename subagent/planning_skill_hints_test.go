package subagent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/tools/builtin"
)

type planSkillTools struct {
	*fakeScoutTools
	queries []string
}

func (r *planSkillTools) SearchSkillHints(_ context.Context, query string, _ int) (builtin.SearchSkillsOutput, error) {
	r.queries = append(r.queries, query)
	return builtin.SearchSkillsOutput{Hits: []builtin.SkillSearchHit{{ID: "skill-plan", Title: "planning-guide", Scope: "workspace"}}}, nil
}

func (r *planSkillTools) Call(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
	r.calls = append(r.calls, call)
	if call.Function.Name == "get_skill" {
		return client.ToolResult{Content: `{"content":"Complete planning skill instructions"}`}, nil
	}
	return client.ToolResult{Content: `{"hits":[{"id":"skill-plan"}]}`}, nil
}

func TestPlanPlannerStartsWithSkillHintsAndCanLoadSkill(t *testing.T) {
	runtime := &planSkillTools{fakeScoutTools: &fakeScoutTools{}}
	rounds := 0
	model := contextChatFunc(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		if request.ToolChoice == client.ToolChoiceNone {
			return contextResponse("ack"), nil
		}
		if rounds == 0 && !reflect.DeepEqual(request.ToolChoice, client.NamedToolChoice(TaskStartToolName)) {
			t.Fatalf("initial tool choice = %#v", request.ToolChoice)
		}
		for _, name := range []string{TaskStartToolName, "search_skills", "get_skill"} {
			if !hasTool(request.Tools, name) {
				t.Fatalf("planner missing %s", name)
			}
		}
		if rounds == 1 {
			found := false
			for _, message := range request.Messages {
				if message.Role != client.RoleTool || message.Name != TaskStartToolName {
					continue
				}
				var result struct {
					SkillHints delegatedSkillHintSet `json:"skill_hints"`
				}
				if err := json.Unmarshal([]byte(message.Content), &result); err != nil {
					t.Fatal(err)
				}
				found = len(result.SkillHints.Candidates) == 1 && result.SkillHints.Candidates[0].ID == "skill-plan"
			}
			if !found {
				t.Fatal("task_start did not return planning skill hint")
			}
		}
		var name, arguments string
		switch rounds {
		case 0:
			name, arguments = TaskStartToolName, `{"objective":"Plan a Go change"}`
		case 1:
			name, arguments = "search_skills", `{"query":"Go planning"}`
		case 2:
			name, arguments = "get_skill", `{"id":"skill-plan"}`
		case 3:
			name, arguments = SubmitPlanToolName, plannerSucceededExample
		default:
			t.Fatalf("unexpected planner round %d", rounds)
		}
		rounds++
		return contextResponseWithTool(name, arguments), nil
	})
	plan, err := (PlannerRunner{Client: model, Tools: runtime, Spec: Spec{Role: config.AgentRolePlanner, Model: "model"}}).
		Run(t.Context(), GrillBrief{Objective: "Plan a Go change"})
	if err != nil || plan.Outcome != "succeeded" || rounds != 4 || len(runtime.queries) != 1 ||
		!strings.Contains(runtime.queries[0], "Plan a Go change") || len(runtime.calls) != 2 ||
		runtime.calls[0].Function.Name != "search_skills" || runtime.calls[1].Function.Name != "get_skill" {
		t.Fatalf("plan=%#v err=%v rounds=%d queries=%#v calls=%#v", plan, err, rounds, runtime.queries, runtime.calls)
	}
}
