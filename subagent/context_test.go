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
	"github.com/snowmerak/q/memory"
)

type contextChatFunc func(context.Context, client.ChatRequest) (*client.ChatResponse, error)

func (f contextChatFunc) Chat(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	return f(ctx, request)
}

func contextResponse(content string) *client.ChatResponse {
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: content}}}}
}

func TestContextCompactorKeepsAnchorsAndUserAnswersAcrossCompactions(t *testing.T) {
	spec := Spec{Role: config.AgentRoleGriller, Model: "griller", ContextLength: 16_000, conversationID: "old-backend"}
	anchors := []client.Message{
		{Role: client.RoleSystem, Content: "role contract: ask, investigate, submit_brief"},
		{Role: client.RoleUser, Content: "exact task, conditions and feedback"},
	}
	history := NewContextCompactor(spec, anchors, nil, len(anchors))
	history.PreserveTools(AskToUserToolName)
	question := client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(AskToUserToolName, `{"question":"Which store?"}`)}}
	answer := client.Message{Role: client.RoleTool, Name: AskToUserToolName, ToolCallID: question.ToolCalls[0].ID, Content: `{"freeform":"SQLite only; never Redis."}`}
	history.Append(question, answer)
	compactions := 0
	fake := contextChatFunc(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		compactions++
		if request.ConversationID != "" || len(request.Tools) != 0 || request.MaxCompletionTokens == nil || request.Model != "griller" {
			t.Fatalf("summary request reused execution state: %#v", request)
		}
		if strings.Contains(request.Messages[1].Content, "SQLite only") {
			t.Fatal("authoritative user answer was sent to the summarizer instead of retained verbatim")
		}
		response := contextResponse("Repository investigated. Remaining work is to submit the brief. Keep the Loom references.")
		response.ConversationID = "summary-backend"
		return response, nil
	})
	for index := 0; index < 2; index++ {
		call := scoutCall("loom_read", `{}`)
		history.Append(
			client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}},
			client.Message{Role: client.RoleTool, Name: call.Function.Name, ToolCallID: call.ID, Content: strings.Repeat("x", 40_000)},
		)
		if err := history.CompactIfNeeded(t.Context(), &spec, fake); err != nil {
			t.Fatal(err)
		}
		if spec.conversationID != "" {
			t.Fatalf("old backend conversation retained: %q", spec.conversationID)
		}
		got := history.Messages()
		if !reflect.DeepEqual(got[:2], anchors) {
			t.Fatalf("immutable task changed: %#v", got[:2])
		}
		if !reflect.DeepEqual(got[2:4], []client.Message{question, answer}) {
			t.Fatalf("question/answer pair changed: %#v", got)
		}
		if len(got) != 5 || got[4].Name != memory.SummaryName {
			t.Fatalf("old summaries accumulated: %#v", got)
		}
		requestMessages := history.RequestMessages()
		if requestMessages[4].Role != client.RoleUser || requestMessages[4].Name != "" || history.Messages()[4].Name != memory.SummaryName {
			t.Fatal("provider summary projection changed internal retention state")
		}
		spec.conversationID = "next-backend"
	}
	if compactions != 2 {
		t.Fatalf("compactions = %d", compactions)
	}
}

func TestContextCompactorFailureLeavesHistoryAndBackendIntact(t *testing.T) {
	for _, test := range []struct {
		name string
		chat contextChatFunc
	}{
		{"provider", func(context.Context, client.ChatRequest) (*client.ChatResponse, error) {
			return nil, errors.New("offline")
		}},
		{"empty", func(context.Context, client.ChatRequest) (*client.ChatResponse, error) {
			return contextResponse(""), nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := Spec{Model: "scout", ContextLength: 16_000, conversationID: "keep-backend"}
			history := NewContextCompactor(spec, []client.Message{{Role: client.RoleSystem, Content: "contract"}}, nil, 1)
			history.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("x", 40_000)})
			before := history.Messages()
			if err := history.CompactIfNeeded(t.Context(), &spec, test.chat); err == nil {
				t.Fatal("compaction unexpectedly succeeded")
			}
			if !reflect.DeepEqual(history.Messages(), before) || spec.conversationID != "keep-backend" {
				t.Fatal("failed compaction mutated request state")
			}
		})
	}
}

func TestContextCompactorAppliesSummaryAboveTarget(t *testing.T) {
	spec := Spec{Model: "scout", ContextLength: 16_000, conversationID: "old-backend"}
	anchor := client.Message{Role: client.RoleSystem, Content: "contract"}
	history := NewContextCompactor(spec, []client.Message{anchor}, nil, 1)
	history.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("old context ", 8_000)})
	summary := strings.Repeat("x", 40_000)
	if err := history.CompactIfNeeded(t.Context(), &spec, contextChatFunc(func(context.Context, client.ChatRequest) (*client.ChatResponse, error) {
		return contextResponse(summary), nil
	})); err != nil {
		t.Fatalf("summary above target was rejected: %v", err)
	}
	if spec.conversationID != "" {
		t.Fatalf("backend conversation was not reset: %q", spec.conversationID)
	}
	messages := history.Messages()
	if len(messages) != 2 || !reflect.DeepEqual(messages[0], anchor) || messages[1].Name != memory.SummaryName ||
		!strings.Contains(messages[1].Content, summary) {
		t.Fatalf("oversized summary was not applied: %#v", messages)
	}
}

func TestContextCompactorSkipsSmallOrUnknownContext(t *testing.T) {
	for _, window := range []int64{0, 16_000} {
		spec := Spec{Model: "scout", ContextLength: window, conversationID: "keep-backend"}
		history := NewContextCompactor(spec, []client.Message{{Role: client.RoleUser, Content: "task"}}, nil, 1)
		fake := contextChatFunc(func(context.Context, client.ChatRequest) (*client.ChatResponse, error) {
			t.Fatal("unexpected summary request")
			return nil, nil
		})
		if err := history.CompactIfNeeded(t.Context(), &spec, fake); err != nil {
			t.Fatal(err)
		}
		if spec.conversationID != "keep-backend" {
			t.Fatal("small context reset the backend conversation")
		}
	}
}

func TestContextCompactorUsesRoleLimitAndConfiguredPolicy(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "chat"
	value.Provider.ContextWindow = 12_000
	value.Context.TriggerRatio, value.Context.TargetRatio, value.Context.RecentRatio = .70, .30, .10
	spec, err := Resolve(value, config.AgentRoleScout, []client.Model{{ID: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	policy := spec.memoryPolicy()
	if policy.ContextWindow != 12_000 || policy.TriggerRatio != .70 || policy.TargetRatio != .30 || policy.RecentRatio != .10 {
		t.Fatalf("policy = %#v", policy)
	}
	spec.ContextPolicy = config.ContextConfig{}
	if got := spec.memoryPolicy().TriggerRatio; got != .80 {
		t.Fatalf("internal default trigger = %v", got)
	}
}

func TestContextCompactorIncludesToolSchemasAndModelOutputLimit(t *testing.T) {
	spec := Spec{Model: "scout", ContextLength: 16_000, MaxOutputTokens: 64}
	history := NewContextCompactor(spec, []client.Message{{Role: client.RoleSystem, Content: "contract"}}, []client.Tool{{
		Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "large_schema", Description: strings.Repeat("x", 28_000)},
	}}, 1)
	history.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("x", 10_000)})
	called := false
	if err := history.CompactIfNeeded(t.Context(), &spec, contextChatFunc(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		called = true
		if request.MaxCompletionTokens == nil || *request.MaxCompletionTokens != 64 {
			t.Fatalf("summary ignored model output limit: %#v", request.MaxCompletionTokens)
		}
		return contextResponse("continue from the contract"), nil
	})); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("tool schema overhead was ignored")
	}
}

func TestGrillerRetainsActualUserAnswerAfterToolHistoryCompaction(t *testing.T) {
	question := scoutCall(AskToUserToolName, `{"question":"Which storage?","choices":[{"id":"sqlite","label":"SQLite"}]}`)
	fake := &fakeScoutClient{conversationID: "griller-cache", responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{question}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall("loom_read", `{}`)}},
		{Role: client.RoleAssistant, Content: "Repository inspected; submit the brief from the confirmed user answer."},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(SubmitBriefToolName, `{"objective":"task","conditions":["SQLite only"],"acceptance_criteria":["durable"]}`)}},
	}}
	tools := &fakeScoutTools{available: []client.Tool{scoutFunctionTool("loom_read")}, result: &client.ToolResult{Content: strings.Repeat("x", 72_000)}}
	_, err := (GrillerRunner{
		Client: fake, Tools: tools, Spec: Spec{Role: config.AgentRoleGriller, Model: "griller", ContextLength: 32_000},
		Ask: func(context.Context, UserQuestion) (UserAnswer, error) {
			return UserAnswer{SelectedChoiceID: "sqlite", Freeform: "No Redis."}, nil
		},
	}).Run(t.Context(), GrillTask{Objective: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 4 || fake.requests[2].MaxCompletionTokens == nil || fake.requests[3].ConversationID != "" {
		t.Fatalf("unexpected compaction sequence: %#v", fake.requests)
	}
	got := fake.requests[3].Messages
	if len(got) < 5 || !reflect.DeepEqual(got[2].ToolCalls, []client.ToolCall{question}) ||
		got[3].ToolCallID != question.ID || got[3].Content != `{"selected_choice_id":"sqlite","freeform":"No Redis."}` {
		t.Fatalf("confirmed answer was summarized or detached: %#v", got)
	}
}

func TestInternalRunnersCompactBetweenToolRoundsAndKeepContracts(t *testing.T) {
	planJSON, err := json.Marshal(executableTestPlan())
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"griller", "planner", "scout", "coder", "review"} {
		t.Run(role, func(t *testing.T) {
			var first, resumed []client.Message
			normalCalls, summaryCalls := 0, 0
			fake := contextChatFunc(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
				if request.ToolChoice == client.ToolChoiceNone {
					return contextResponse(""), nil
				}
				if request.MaxCompletionTokens != nil && len(request.Tools) == 0 {
					summaryCalls++
					return contextResponse("Read the repository evidence. Continue from the exact role contract and task."), nil
				}
				normalCalls++
				if normalCalls == 1 {
					first = request.Messages
					return &client.ChatResponse{ConversationID: "old-role", Choices: []client.Choice{{Message: client.Message{
						Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall("mcp_read", `{}`)},
					}}}}, nil
				}
				resumed = request.Messages
				if request.ConversationID != "" {
					t.Fatalf("resumed with pre-compaction backend: %q", request.ConversationID)
				}
				name, arguments := ScoutCompleteToolName, `{"outcome":"succeeded","summary":"done"}`
				switch role {
				case "griller":
					name, arguments = SubmitBriefToolName, `{"objective":"task","conditions":["keep exact constraints"],"acceptance_criteria":["verified"]}`
				case "planner":
					name, arguments = SubmitPlanToolName, string(planJSON)
				case "review":
					name, arguments = ReviewTaskToolName, `{"decision":"next","feedback":""}`
				}
				return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
					Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(name, arguments)},
				}}}}, nil
			})
			tools := &fakeScoutTools{
				available: []client.Tool{scoutFunctionTool("mcp_read")},
				result:    &client.ToolResult{Content: strings.Repeat("x", 72_000)},
			}
			spec := Spec{Role: role, Model: "role-model", ContextLength: 32_000}
			var runErr error
			switch role {
			case "griller":
				_, runErr = (GrillerRunner{Client: fake, Tools: tools, Spec: spec, Ask: func(context.Context, UserQuestion) (UserAnswer, error) {
					return UserAnswer{}, errors.New("unexpected question")
				}}).Run(t.Context(), GrillTask{Objective: "task", Feedback: []string{"preserve this feedback"}})
			case "planner":
				_, runErr = (PlannerRunner{Client: fake, Tools: tools, Spec: spec}).Run(t.Context(), GrillBrief{Objective: "task", Conditions: []string{"preserve this constraint"}})
			case "scout":
				_, runErr = (ScoutRunner{Client: fake, Tools: tools, Spec: spec}).Run(t.Context(), ScoutTask{Objective: "task", CompletionCriteria: []string{"preserve scope"}})
			case "coder":
				_, runErr = (CoderRunner{Client: fake, Tools: tools, Spec: spec}).Run(t.Context(), CoderAttempt{Plan: executableTestPlan(), TaskIndex: 0, Attempt: 1, Targets: []string{"app/model.go"}, Feedback: "preserve reviewer feedback"})
			case "review":
				spec.Role = config.AgentRolePlanner
				_, runErr = (PlannerReviewRunner{Client: fake, Tools: tools, Spec: spec}).Run(t.Context(), TaskReviewRequest{Plan: executableTestPlan(), TaskIndex: 0, Attempt: 1, Result: CoderResult{Outcome: "succeeded", Summary: "exact evidence"}})
			}
			if runErr != nil {
				t.Fatal(runErr)
			}
			if summaryCalls != 1 || normalCalls != 2 {
				t.Fatalf("summary calls=%d, normal calls=%d", summaryCalls, normalCalls)
			}
			if len(resumed) < 3 || !reflect.DeepEqual(first[:2], resumed[:2]) ||
				resumed[2].Role != client.RoleUser || !strings.HasPrefix(resumed[2].Content, "Compressed conversation memory:") {
				t.Fatalf("exact role/task contract did not survive compaction: %#v", resumed)
			}
		})
	}
}
