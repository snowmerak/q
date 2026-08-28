package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/sessionstore"
)

func TestRoleTerminalToolsDeliverResultsBeforeReturning(t *testing.T) {
	plan, err := json.Marshal(executableTestPlan())
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"scout", "coder", "griller", "planner", "review"} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%v", role, failed), func(t *testing.T) {
				name, arguments := ScoutCompleteToolName, `{"outcome":"succeeded","summary":"done"}`
				switch role {
				case "griller":
					name, arguments = SubmitBriefToolName, `{"objective":"task","conditions":["bounded"],"acceptance_criteria":["verified"]}`
				case "planner":
					name, arguments = SubmitPlanToolName, string(plan)
				case "review":
					name, arguments = ReviewTaskToolName, `{"decision":"next","feedback":""}`
				}
				call := scoutCall(name, arguments)
				calls := 0
				failure := errors.New("terminal transport failed")
				sink := &scoutRecordSink{}
				fake := contextChatFunc(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
					calls++
					if calls == 1 {
						return &client.ChatResponse{ConversationID: "role-conversation", Choices: []client.Choice{{Message: client.Message{
							Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call},
						}}}}, nil
					}
					last := request.Messages[len(request.Messages)-1]
					if calls != 2 || request.Model != "role-model" || request.ReasoningEffort != "high" || request.ConversationID != "role-conversation" || request.ToolChoice != client.ToolChoiceNone || last.Role != client.RoleTool || last.Name != name || last.ToolCallID != call.ID || strings.HasPrefix(last.Content, "Tool error:") {
						t.Fatalf("terminal result was not delivered: %#v", request)
					}
					for _, record := range sink.records {
						if record.Kind == sessionstore.KindTask && record.Status == sessionstore.StatusSucceeded {
							t.Error("role marked succeeded before its terminal result was delivered")
						}
					}
					if failed {
						return nil, failure
					}
					response := contextResponse("Acknowledged.")
					response.Usage = client.Usage{TotalTokens: 7}
					return response, nil
				})
				tools := &fakeScoutTools{}
				spec := Spec{Role: role, Model: "role-model", ReasoningEffort: "high"}
				var runErr error
				switch role {
				case "scout":
					var result ScoutResult
					result, runErr = (ScoutRunner{Client: fake, Tools: tools, Spec: spec, Sink: sink, RunID: "run"}).Run(t.Context(), ScoutTask{Objective: "task"})
					if !failed && result.Usage.TotalTokens != 7 {
						t.Fatalf("finalization usage missing: %#v", result.Usage)
					}
				case "coder":
					_, runErr = (CoderRunner{Client: fake, Tools: tools, Spec: spec, Sink: sink, RunID: "run"}).Run(t.Context(), CoderAttempt{Plan: executableTestPlan(), TaskIndex: 0, Attempt: 1, Targets: []string{"app/model.go"}})
				case "griller":
					_, runErr = (GrillerRunner{Client: fake, Tools: tools, Spec: spec, Ask: func(context.Context, UserQuestion) (UserAnswer, error) {
						return UserAnswer{}, errors.New("unexpected question")
					}}).Run(t.Context(), GrillTask{Objective: "task"})
				case "planner":
					_, runErr = (PlannerRunner{Client: fake, Tools: tools, Spec: spec}).Run(t.Context(), GrillBrief{Objective: "task"})
				case "review":
					spec.Role = config.AgentRolePlanner
					_, runErr = (PlannerReviewRunner{Client: fake, Tools: tools, Spec: spec, Sink: sink, RunID: "run"}).Run(t.Context(), TaskReviewRequest{Plan: executableTestPlan(), TaskIndex: 0, Attempt: 1, Result: CoderResult{Outcome: "succeeded", Summary: "done"}})
				}
				if calls != 2 || (failed && !errors.Is(runErr, failure)) || (!failed && runErr != nil) {
					t.Fatalf("calls=%d err=%v", calls, runErr)
				}
				if failed && len(sink.records) > 0 && sink.records[len(sink.records)-1].Status != sessionstore.StatusFailed {
					t.Fatalf("failed delivery did not fail lifecycle: %#v", sink.records)
				}
			})
		}
	}
}

func TestFinishToolTurnPinsSelectedRoleProviderWithoutFallback(t *testing.T) {
	spec := Spec{
		Group: "roles", Model: "selected", conversationID: "selected-conversation", activeCandidate: 1,
		Candidates: []client.ModelCandidate{
			{Model: "primary"}, {Model: "selected", ReasoningEffort: "high", Timeout: time.Second}, {Model: "fallback"},
		},
	}
	failure := &client.APIError{StatusCode: http.StatusServiceUnavailable, Message: "offline"}
	calls := 0
	fake := contextChatFunc(func(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("selected candidate timeout was lost")
		}
		if request.Model != "selected" || request.ConversationID != "selected-conversation" || request.ReasoningEffort != "high" {
			t.Fatalf("terminal delivery changed provider: %#v", request)
		}
		return nil, failure
	})
	_, err := spec.FinishToolTurn(t.Context(), fake, client.ChatRequest{Messages: []client.Message{{Role: client.RoleTool, ToolCallID: "done", Content: "accepted"}}}, nil)
	if !errors.Is(err, failure) || calls != 1 || spec.activeCandidate != 1 {
		t.Fatalf("terminal failure was routed to a new provider: calls=%d selected=%d err=%v", calls, spec.activeCandidate, err)
	}
}
