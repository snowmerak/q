package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/tools/builtin"
)

type delegatedSkillRuntime struct {
	queries []string
	calls   []client.ToolCall
}

func (*delegatedSkillRuntime) Tools() []client.Tool {
	return []client.Tool{
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "search_skills", Parameters: map[string]any{"type": "object"}}},
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "get_skill", Parameters: map[string]any{"type": "object"}}},
	}
}

func (r *delegatedSkillRuntime) Call(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
	r.calls = append(r.calls, call)
	return client.ToolResult{}, errors.New("unexpected model tool call")
}

func (r *delegatedSkillRuntime) SearchSkillHints(_ context.Context, query string, _ int) (builtin.SearchSkillsOutput, error) {
	r.queries = append(r.queries, query)
	return builtin.SearchSkillsOutput{Hits: []builtin.SkillSearchHit{{
		ID: "skill-go", Title: "go-review", Description: "Review Go code", Scope: "workspace",
	}}}, nil
}

func TestGeneralScoutTaskStartReceivesSkillHints(t *testing.T) {
	runtime := &delegatedSkillRuntime{}
	rounds := 0
	configured := contextChatFunc(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		if request.ToolChoice == client.ToolChoiceNone {
			return contextResponse("ack"), nil
		}
		if !hasTool(request.Tools, "search_skills") || !hasTool(request.Tools, "get_skill") {
			t.Fatal("scout did not receive its configured skill tools")
		}
		switch rounds {
		case 0:
			if !reflect.DeepEqual(request.ToolChoice, client.NamedToolChoice(TaskStartToolName)) {
				t.Fatalf("initial tool choice = %#v", request.ToolChoice)
			}
			if !strings.Contains(request.Messages[0].Content, "call search_skills") {
				t.Fatal("scout did not receive skill retrieval guidance")
			}
			rounds++
			return contextResponseWithTool(TaskStartToolName, `{"objective":"Inspect Go code","completion_criteria":["Review tests"]}`), nil
		case 1:
			var hints delegatedSkillHintSet
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
				hints = result.SkillHints
				found = true
			}
			if !found || hints.Trigger != "task_start" || len(hints.Candidates) != 1 || hints.Candidates[0].ID != "skill-go" {
				t.Fatalf("task_start skill hints = %#v, found=%v", hints, found)
			}
			rounds++
			return contextResponseWithTool(TaskCompleteToolName, `{"outcome":"blocked","summary":"fixture complete","blocker":"test fixture"}`), nil
		default:
			t.Fatalf("unexpected model round %d", rounds)
			return nil, nil
		}
	})
	result, err := (GeneralRunner{
		Client: configured, Tools: runtime,
		Spec: Spec{Role: config.AgentRoleResearch, Model: "model", Candidates: []client.ModelCandidate{{Model: "model"}}},
		Definition: AgentDefinition{Info: DelegateInfo{Name: "workspace/inspector", Kind: AgentKindInner, Role: config.AgentRoleResearch},
			SystemPrompt: "Inspect repository evidence.", StrictTools: true},
	}).Run(t.Context(), "Inspect the repository")
	if err != nil || result.Outcome != "blocked" || rounds != 2 || len(runtime.queries) != 1 || runtime.queries[0] != "Inspect Go code Review tests" || len(runtime.calls) != 0 {
		t.Fatalf("result=%#v err=%v rounds=%d queries=%#v calls=%#v", result, err, rounds, runtime.queries, runtime.calls)
	}
}

func contextResponseWithTool(name, arguments string) *client.ChatResponse {
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
			ID: name, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: name, Arguments: arguments},
		}},
	}}}}
}
