package thinker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
)

func TestLearningMachineInitializesFromLatestSummaryOrFirstUser(t *testing.T) {
	machine, err := NewMachine(LearningState{}, 4000)
	if err != nil {
		t.Fatal(err)
	}
	machine.Initialize([]client.Message{
		{Role: client.RoleUser, Content: "old user"},
		{Role: client.RoleAssistant, Content: "old assistant"},
		{Role: client.RoleSystem, Name: memory.SummaryName, Content: "current summary"},
		{Role: client.RoleUser, Content: "recent user"},
	})
	state := machine.State()
	if len(state.Events) != 2 || state.Events[0].Message.Name != memory.SummaryName || state.Events[1].Message.Content != "recent user" {
		t.Fatalf("summary initialization = %#v", state.Events)
	}

	fresh, err := NewMachine(LearningState{}, 4000)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Initialize([]client.Message{
		{Role: client.RoleSystem, Content: "excluded"},
		{Role: client.RoleUser, Content: "first user"},
		{Role: client.RoleAssistant, Content: "answer"},
	})
	if got := fresh.State().Events; len(got) != 2 || got[0].Message.Content != "first user" {
		t.Fatalf("first-user initialization = %#v", got)
	}
}

func TestLearningMachineExcludesToolsAndForcesSpecialBoundaries(t *testing.T) {
	machine, err := NewMachine(LearningState{}, 16_000)
	if err != nil {
		t.Fatal(err)
	}
	machine.AppendMessage(client.Message{Role: client.RoleUser, Content: "Implement the durable decision."})
	machine.AppendMessage(client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
		ID: "call", Function: client.FunctionCall{Name: "run_command", Arguments: `{"command":"go test"}`},
	}}})
	machine.AppendMessage(client.Message{Role: client.RoleTool, Name: "run_command", Content: "raw output"})
	taskResult := json.RawMessage(`{"outcome":"succeeded","summary":"All tests passed","details":{"count":42}}`)
	closed := machine.AppendTaskComplete(taskResult)
	if len(closed) != 1 || closed[0].Reason != BoundaryTaskComplete {
		t.Fatalf("task boundary = %#v", closed)
	}
	_, messages, ok := machine.Next()
	if !ok || len(messages) != 2 || messages[1].Name != TaskCompleteEventName || messages[1].Content != string(taskResult) {
		t.Fatalf("task segment = %#v", messages)
	}
	encoded, _ := json.Marshal(messages)
	if strings.Contains(string(encoded), "run_command") || strings.Contains(string(encoded), "raw output") {
		t.Fatalf("tool data leaked into task segment: %s", encoded)
	}

	if err := machine.Commit(closed[0].ID); err != nil {
		t.Fatal(err)
	}
	plan := json.RawMessage(`{"approved":true,"plan":{"summary":"full plan","steps":[{"title":"one"},{"title":"two"}]}}`)
	closed = machine.AppendPlanApproved(plan)
	if len(closed) != 1 || closed[0].Reason != BoundaryPlanApproved {
		t.Fatalf("plan boundary = %#v", closed)
	}
	_, messages, ok = machine.Next()
	if !ok || len(messages) != 1 || messages[0].Name != PlanApprovedEventName || messages[0].Content != string(plan) {
		t.Fatalf("plan segment = %#v", messages)
	}
}

func TestLearningMachineSizeBoundaryExplicitNoopAndCommit(t *testing.T) {
	machine, err := NewMachine(LearningState{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if closed := machine.EnqueueExplicit(); len(closed) != 0 {
		t.Fatalf("empty explicit boundary = %#v", closed)
	}
	var closed []LearningSegment
	for index := 0; index < 20 && len(closed) == 0; index++ {
		closed = machine.AppendMessage(client.Message{
			Role: client.RoleUser, Content: strings.Repeat("semantic context ", 25),
		})
	}
	if len(closed) == 0 || closed[0].Reason != BoundarySize {
		t.Fatalf("size boundary = %#v", closed)
	}
	segment, messages, ok := machine.Next()
	if !ok || segment.ID != closed[0].ID || len(messages) == 0 {
		t.Fatalf("queued segment = %#v, %#v", segment, messages)
	}
	restored, err := NewMachine(machine.State(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if retry, _, ok := restored.Next(); !ok || retry.ID != segment.ID {
		t.Fatalf("durable retry segment = %#v", retry)
	}
	if err := restored.Commit(segment.ID); err != nil {
		t.Fatal(err)
	}
	state := restored.State()
	if state.CommittedCursor != segment.EndCursor {
		t.Fatalf("committed state = %#v", state)
	}
}

func TestLearningMachineClosesBeforeOverflowingMessage(t *testing.T) {
	const contextLength = int64(1000)
	maximum := contextMaximumTokens(contextLength)
	first := client.Message{Role: client.RoleUser, Content: strings.Repeat("first durable context ", 20)}
	if got := contextPromptTokens([]client.Message{first}, false); got >= maximum {
		t.Fatalf("first message already reaches boundary: %d >= %d", got, maximum)
	}
	var second client.Message
	for count := 1; count < 200; count++ {
		candidate := client.Message{Role: client.RoleAssistant, Content: strings.Repeat("second durable context ", count)}
		if contextPromptTokens([]client.Message{candidate}, false) < maximum &&
			contextPromptTokens([]client.Message{first, candidate}, false) > maximum {
			second = candidate
			break
		}
	}
	if second.Content == "" {
		t.Fatal("failed to construct individually bounded messages that overflow together")
	}

	machine, err := NewMachine(LearningState{}, contextLength)
	if err != nil {
		t.Fatal(err)
	}
	machine.AppendMessage(first)
	closed := machine.AppendMessage(second)
	if len(closed) != 1 || closed[0].StartCursor != 1 || closed[0].EndCursor != 1 {
		t.Fatalf("overflow boundaries = %#v", closed)
	}
	_, messages, ok := machine.Next()
	if !ok || len(messages) != 1 || messages[0].Content != first.Content {
		t.Fatalf("first queued segment = %#v", messages)
	}
	if explicit := machine.EnqueueExplicit(); len(explicit) != 1 || explicit[0].StartCursor != 2 || explicit[0].EndCursor != 2 {
		t.Fatalf("next active segment = %#v", explicit)
	}
}

func TestLearningMachineIncludesMessageAtExactBoundary(t *testing.T) {
	exact := client.Message{Role: client.RoleUser, Content: strings.Repeat("exact boundary context ", 30)}
	tokens := contextPromptTokens([]client.Message{exact}, false)
	var contextLength int64
	for candidate := int64(tokens); candidate < int64(tokens*4); candidate++ {
		if contextMaximumTokens(candidate) == tokens {
			contextLength = candidate
			break
		}
	}
	if contextLength == 0 {
		t.Fatalf("failed to construct exact boundary for %d tokens", tokens)
	}
	machine, err := NewMachine(LearningState{}, contextLength)
	if err != nil {
		t.Fatal(err)
	}
	closed := machine.AppendMessage(exact)
	if len(closed) != 1 || closed[0].StartCursor != 1 || closed[0].EndCursor != 1 {
		t.Fatalf("exact boundary = %#v", closed)
	}
}
