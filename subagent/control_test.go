package subagent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func controlledTestRunner() GeneralRunner {
	return GeneralRunner{
		Tools:      &recoveryToolRuntime{},
		Spec:       Spec{Role: config.AgentRoleResearch, Model: "model", Candidates: []client.ModelCandidate{{Model: "model"}}},
		Definition: AgentDefinition{Info: DelegateInfo{Name: "workspace/worker", Kind: AgentKindInner, Role: config.AgentRoleResearch}, SystemPrompt: "Work.", Tools: []string{"write_file"}, StrictTools: true},
	}
}

func controlToolResponse(name, arguments string) *client.ChatResponse {
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: name, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: name, Arguments: arguments}}}}}}}
}

func TestGeneralRunnerFollowupStartsNewLifecycleAndRoundBudget(t *testing.T) {
	runner := controlledTestRunner()
	runner.MaxRounds = 2
	steps := 0
	runner.Client = contextChatFunc(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		if request.ToolChoice == client.ToolChoiceNone {
			return contextResponse("ack"), nil
		}
		steps++
		if steps%2 == 1 {
			return controlToolResponse(TaskStartToolName, `{"objective":"work"}`), nil
		}
		return controlToolResponse(TaskCompleteToolName, `{"outcome":"succeeded","summary":"done"}`), nil
	})
	var saved GeneralRunState
	runner.Checkpoint = func(state GeneralRunState) error { saved = state; return nil }
	if _, err := runner.Run(t.Context(), "work"); err != nil {
		t.Fatal(err)
	}
	original := append([]client.Message(nil), saved.Transcript...)
	runner.Resume, runner.Followup = &saved, "continue"
	if _, err := runner.Run(t.Context(), "work"); err != nil {
		t.Fatal(err)
	}
	if steps != 4 || saved.Round != 4 || saved.TurnStartRound != 2 {
		t.Fatalf("steps=%d state=%#v", steps, saved)
	}
	if !reflect.DeepEqual(original, saved.Transcript[:len(original)]) {
		t.Fatal("followup changed earlier messages")
	}
	// A crash before the next completion must not recover the previous result.
	incomplete := append(append([]client.Message(nil), original...), client.Message{Role: client.RoleUser, Name: FollowupMessageName, Content: "continue"}, client.Message{Role: client.RoleAssistant, Content: "still working"})
	if _, found := savedGeneralCompletion(incomplete); found {
		t.Fatal("recovered completion from a previous turn")
	}
}

func TestGeneralRunnerFollowupDoesNotReplayInterruptedTool(t *testing.T) {
	runner := controlledTestRunner()
	write := client.ToolCall{ID: "write", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "write_file", Arguments: `{}`}}
	messages := []client.Message{{Role: client.RoleSystem, Content: "Work."}, {Role: client.RoleUser, Content: "work"}, {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{write}}}
	saved := GeneralRunState{Transcript: messages, Context: messages, Round: 1, Started: true, Spec: SpecCheckpoint{Model: "model"}}
	runner.Resume, runner.Followup = &saved, "finish"
	runner.Client = contextChatFunc(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		if request.ToolChoice == client.ToolChoiceNone {
			return contextResponse("ack"), nil
		}
		if request.Messages[3].Role != client.RoleTool || request.Messages[3].ToolCallID != "write" {
			t.Fatal("unclosed interrupted exchange")
		}
		return controlToolResponse(TaskCompleteToolName, `{"outcome":"succeeded","summary":"done"}`), nil
	})
	if _, err := runner.Run(t.Context(), "work"); err != nil {
		t.Fatal(err)
	}
	if runner.Tools.(*recoveryToolRuntime).calls != 0 {
		t.Fatal("replayed interrupted tool")
	}
}

func TestGuidanceAcknowledgesCheckpointAndResumesPause(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "failed"}[fail], func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			control := NewRunControl(cancel)
			defer control.Finish()
			if err := control.Pause(); err != nil {
				t.Fatal(err)
			}
			ack := make(chan error, 1)
			go func() { ack <- control.Guide(ctx, "new direction") }()
			deadline := time.Now().Add(time.Second)
			for !control.hasGuidance() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !control.hasGuidance() {
				t.Fatal("guidance not queued")
			}
			select {
			case err := <-ack:
				t.Fatalf("acknowledged before checkpoint: %v", err)
			default:
			}
			if control.Status() != "running" {
				t.Fatal("guidance left execution paused")
			}
			runner := controlledTestRunner()
			runner.Control = control
			checkpointErr := errors.New("save failed")
			state := GeneralRunState{}
			history := NewContextCompactor(runner.Spec, nil, nil, 0)
			err := runner.applyGuidance(&state, history, nil, func() error {
				if fail {
					return checkpointErr
				}
				return nil
			})
			if fail != errors.Is(err, checkpointErr) {
				t.Fatalf("checkpoint = %v", err)
			}
			select {
			case err := <-ack:
				if fail != errors.Is(err, checkpointErr) {
					t.Fatalf("ack = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("guidance not acknowledged")
			}
		})
	}
}

func TestRunControlStopDoesNotCancelParentOrSibling(t *testing.T) {
	parent, stopParent := context.WithCancel(t.Context())
	defer stopParent()
	child, cancel := context.WithCancelCause(parent)
	sibling, stopSibling := context.WithCancel(parent)
	defer stopSibling()
	control := NewRunControl(cancel)
	if err := control.Cancel(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(context.Cause(child), ErrRunStopped) || parent.Err() != nil || sibling.Err() != nil {
		t.Fatal("stop escaped the child context")
	}
	control.Finish()
	if err := control.Guide(t.Context(), "too late"); err == nil {
		t.Fatal("accepted late guidance")
	}
}

func TestGeneralRunnerAcceptsQueuedGuidanceBeforeCompletion(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	control := NewRunControl(cancel)
	defer control.Finish()
	runner := controlledTestRunner()
	runner.Control = control
	ack := make(chan error, 1)
	steps := 0
	runner.Client = contextChatFunc(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		if request.ToolChoice == client.ToolChoiceNone {
			return contextResponse("ack"), nil
		}
		steps++
		if steps == 1 {
			return controlToolResponse(TaskStartToolName, `{"objective":"work"}`), nil
		}
		if steps == 2 {
			go func() { ack <- control.Guide(ctx, "review again") }()
			deadline := time.Now().Add(time.Second)
			for !control.hasGuidance() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !control.hasGuidance() {
				t.Fatal("guidance not queued")
			}
		}
		if steps == 3 && request.Messages[len(request.Messages)-1].TextContent() != "review again" {
			t.Fatal("next request lost guidance")
		}
		return controlToolResponse(TaskCompleteToolName, `{"outcome":"succeeded","summary":"done"}`), nil
	})
	if _, err := runner.Run(ctx, "work"); err != nil {
		t.Fatal(err)
	}
	if steps != 3 {
		t.Fatalf("completion ignored guidance: rounds=%d", steps)
	}
	select {
	case err := <-ack:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("guidance not acknowledged")
	}
}
