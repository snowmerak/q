package memory

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func appendMemoryUpdate(t *testing.T, manager *Manager, name, arguments string) memoryDelta {
	t.Helper()
	call := memoryCall(name, arguments)
	manager.Append(client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}})
	result, handled := manager.CallMemoryTool(call)
	if !handled || result.IsError {
		t.Fatalf("%s: %s", name, result.Content)
	}
	manager.Append(client.ToolResultMessage(call, result))
	var delta memoryDelta
	if err := json.Unmarshal([]byte(result.Content), &delta); err != nil {
		t.Fatal(err)
	}
	return delta
}

func incrementalFixture(t *testing.T) *Manager {
	t.Helper()
	manager := New(Policy{ContextWindow: 20_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .01}, []client.Message{
		{Role: client.RoleSystem, Content: "role contract"},
		{Role: client.RoleUser, Content: "Inspect Q. Keep source references."},
		{Role: client.RoleAssistant, Content: strings.Repeat("covered investigation detail ", 1000)},
		{Role: client.RoleUser, Content: "Correction: change D only.\n  Keep these exact spaces.  "},
		{Role: client.RoleUser, ContentParts: []client.MessageContentPart{{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/task.png"}}}},
	})
	appendMemoryUpdate(t, manager, SetActiveWorkTool, `{"description":"Implement D","next_action":"verify resume"}`)
	appendMemoryUpdate(t, manager, RecordFactTool, `{"fact":"The investigation is complete","source":"loom://evidence"}`)
	appendMemoryUpdate(t, manager, CheckpointTool, `{"expected_revision":2}`)
	return manager
}

func incrementalPlan(t *testing.T, manager *Manager) Plan {
	t.Helper()
	plan, err := manager.PlanWithRetention(Retention{PreserveInstructions: true, AllowTargetGrowth: true, SummarizeOversizedRecent: true})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestIncrementalCompactionProjectsCoveredMemoryAndExactRequests(t *testing.T) {
	manager := incrementalFixture(t)
	before := manager.Messages()
	// A session restart and provider change must not invalidate visible coverage.
	manager = New(manager.policy, before)
	manager.ClearResponseReplay()
	plan := incrementalPlan(t, manager)
	checkpoint, ready := plan.CheckpointWithoutModel()
	if !ready || plan.CoveredMessages == 0 || len(plan.Source) != 0 {
		t.Fatalf("covered history still needs summary: %#v", plan)
	}
	next, _, err := manager.CheckpointCopy(plan, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manager.Messages(), before) || manager.Stats().Compactions != 0 {
		t.Fatal("preparing compaction mutated live context")
	}
	var requests []client.Message
	for _, message := range next.Messages() {
		if strings.Contains(message.Content, "covered investigation detail") {
			t.Fatal("covered raw transcript survived compaction")
		}
		if message.Name == RequestAnchorName {
			if err := json.Unmarshal([]byte(message.Content), &requests); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !reflect.DeepEqual(requests, []client.Message{before[1], before[3], before[4]}) {
		t.Fatalf("original requests changed: %#v", requests)
	}
	resumed := New(manager.policy, next.Messages())
	if resumed.taskMemory.revision != 3 || len(resumed.taskMemory.active) != 1 || len(resumed.taskMemory.facts) != 1 ||
		!strings.Contains(resumed.taskMemory.facts[0].Text, "loom://evidence") {
		t.Fatalf("projection failed to resume: %#v", resumed.taskMemory)
	}
}

func TestIncrementalCompactionSummarizesOnlyUncoveredSuffix(t *testing.T) {
	manager := incrementalFixture(t)
	manager.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("uncovered verification failed ", 100)})
	plan := incrementalPlan(t, manager)
	if _, ready := plan.CheckpointWithoutModel(); ready {
		t.Fatal("uncovered history was silently discarded")
	}
	request := plan.RequestMessages()[1].Content
	if plan.Maintained == nil || !strings.Contains(request, "uncovered verification failed") ||
		strings.Contains(request, "covered investigation detail") || !strings.Contains(request, "loom://evidence") {
		t.Fatalf("incorrect incremental input: %s", request)
	}
	if _, err := manager.ApplyCheckpoint(plan, `{"current_request":["Implement D"],"active_work":[],"previous_work":[],"facts":["Verification failed: new unrecorded evidence"]}`); err != nil {
		t.Fatal(err)
	}
	if len(manager.taskMemory.facts) != 2 {
		t.Fatalf("overlay erased uncovered evidence: %#v", manager.taskMemory.facts)
	}
	manager.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("second uncovered span ", 100)})
	second := incrementalPlan(t, New(manager.policy, manager.Messages()))
	request = second.RequestMessages()[1].Content
	if second.Maintained == nil || !strings.Contains(request, "second uncovered span") || !strings.Contains(request, "Verification failed") {
		t.Fatal("persisted checkpoint was not reused as the next base")
	}
}

func TestDuplicateAcknowledgmentDoesNotCoverNewHistory(t *testing.T) {
	manager := incrementalFixture(t)
	acknowledgment := manager.Messages()[len(manager.Messages())-1]
	manager.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("new unrecorded discovery ", 100)})
	manager.Append(acknowledgment)
	plan := incrementalPlan(t, manager)
	if _, ready := plan.CheckpointWithoutModel(); ready || !strings.Contains(plan.RequestMessages()[1].Content, "new unrecorded discovery") {
		t.Fatal("duplicate acknowledgment expanded the covered boundary")
	}
}

func TestMalformedStoredCheckpointCannotAdvanceCoverage(t *testing.T) {
	manager := incrementalFixture(t)
	manager.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("unrecorded investigation ", 100)})
	manager.Append(client.Message{Role: client.RoleSystem, Name: SummaryName, Content: `{"_memory":{"version":1,"revision":3},"broken":"no checkpoint sections"}`})
	plan := incrementalPlan(t, manager)
	if _, ready := plan.CheckpointWithoutModel(); ready || !strings.Contains(plan.RequestMessages()[1].Content, "unrecorded investigation") {
		t.Fatal("malformed stored checkpoint swallowed uncovered history")
	}
}

func TestMemoryOverlayDoesNotDuplicateUnkeyedSummaryFacts(t *testing.T) {
	var state taskMemory
	state.loadCheckpoint(Checkpoint{Facts: []string{"existing finding"}})
	state.touchedFacts = true
	merged := state.overlay(Checkpoint{Facts: []string{"existing finding", "new finding"}})
	if len(merged.Facts) != 2 {
		t.Fatalf("repeated summary duplicated facts: %#v", merged.Facts)
	}
}

func TestIncrementalCompactionFallsBackForUnconfirmedOrAlteredHistory(t *testing.T) {
	for _, scenario := range []string{"no confirmation", "altered prefix", "revision gap", "missing result"} {
		t.Run(scenario, func(t *testing.T) {
			manager := incrementalFixture(t)
			history := manager.Messages()
			switch scenario {
			case "no confirmation":
				history = history[:len(history)-2]
			case "altered prefix":
				history[2].Content += "changed after confirmation"
			case "revision gap":
				history[6].Content = `{"error":"missing memory delta"}`
			case "missing result":
				history = history[:len(history)-1]
			}
			manager.Reset(history)
			plan := incrementalPlan(t, manager)
			if plan.Maintained != nil || !strings.Contains(plan.RequestMessages()[1].Content, "covered investigation detail") {
				t.Fatal("untrusted coverage bypassed full-source fallback")
			}
		})
	}
}

func TestMemoryCheckpointRejectsStaleBatchedAndPendingCalls(t *testing.T) {
	for _, scenario := range []string{"stale", "batched", "pending", "missing revision"} {
		t.Run(scenario, func(t *testing.T) {
			manager := New(Policy{}, nil)
			appendMemoryUpdate(t, manager, RecordFactTool, `{"fact":"evidence"}`)
			arguments := `{"expected_revision":1}`
			switch scenario {
			case "stale":
				arguments = `{"expected_revision":0}`
			case "missing revision":
				arguments = `{}`
			}
			call := memoryCall(CheckpointTool, arguments)
			other := memoryCall("read_file", `{}`)
			calls := []client.ToolCall{call}
			switch scenario {
			case "batched":
				calls = append(calls, other)
			case "pending":
				manager.Append(client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{other}})
			}
			manager.Append(client.Message{Role: client.RoleAssistant, ToolCalls: calls})
			result, _ := manager.CallMemoryTool(call)
			if !result.IsError || manager.taskMemory.revision != 1 {
				t.Fatalf("invalid acknowledgment accepted: %s", result.Content)
			}
		})
	}
}

func TestMemoryRevisionPreventsDuplicateAndStaleReplay(t *testing.T) {
	manager := incrementalFixture(t)
	workID := manager.taskMemory.active[0].ID
	oldResult := manager.Messages()[6]
	appendMemoryUpdate(t, manager, CompleteWorkTool, fmt.Sprintf(`{"work_id":%q,"result":"D implemented","expected_revision":3}`, workID))
	// A duplicate old output cannot resurrect completed work.
	manager.Append(oldResult)
	if len(manager.taskMemory.active) != 0 || len(manager.taskMemory.previous) != 1 || manager.taskMemory.revision != 4 {
		t.Fatalf("stale result changed state: %#v", manager.taskMemory)
	}
	result, _ := manager.CallMemoryTool(memoryCall(RecordFactTool, `{"fact":"stale write","expected_revision":3}`))
	if !result.IsError || manager.taskMemory.revision != 4 {
		t.Fatal("stale write changed memory")
	}
	plan := Plan{Source: manager.Messages(), Recent: []client.Message{oldResult}}
	if _, err := manager.ApplyCheckpoint(plan, checkpointResponse("pending verification")); err != nil {
		t.Fatal(err)
	}
	resumed := New(manager.policy, manager.Messages())
	for _, entry := range resumed.taskMemory.active {
		if entry.ID == workID {
			t.Fatal("retained old result resurrected work after checkpoint restart")
		}
	}
	if resumed.taskMemory.revision != 4 {
		t.Fatalf("revision lost across compaction: %d", resumed.taskMemory.revision)
	}
}

func TestIncrementalCompactionRejectsOversizedExactRequestsAtomically(t *testing.T) {
	manager := incrementalFixture(t)
	manager.Append(client.Message{Role: client.RoleUser, Content: strings.Repeat("exact user constraint ", 4000)})
	appendMemoryUpdate(t, manager, CheckpointTool, `{"expected_revision":3}`)
	before := manager.Messages()
	if _, err := manager.PlanWithRetention(Retention{PreserveInstructions: true, AllowTargetGrowth: true, SummarizeOversizedRecent: true}); err == nil {
		t.Fatal("oversized exact constraints were silently truncated")
	}
	if !reflect.DeepEqual(before, manager.Messages()) {
		t.Fatal("failed plan mutated live history")
	}
}

func TestIncrementalCompactionRejectsOversizedMaintainedStateAtomically(t *testing.T) {
	manager := incrementalFixture(t)
	for index := range 30 {
		arguments, _ := json.Marshal(map[string]string{"fact": fmt.Sprintf("fact %d: %s", index, strings.Repeat("x", 1800))})
		appendMemoryUpdate(t, manager, RecordFactTool, string(arguments))
	}
	appendMemoryUpdate(t, manager, CheckpointTool, `{"expected_revision":33}`)
	plan := incrementalPlan(t, manager)
	checkpoint, ready := plan.CheckpointWithoutModel()
	if !ready {
		t.Fatal("fixture should be fully covered")
	}
	before := manager.Messages()
	if _, _, err := manager.CheckpointCopy(plan, checkpoint); err == nil {
		t.Fatal("oversized maintained state was accepted")
	}
	if !reflect.DeepEqual(before, manager.Messages()) || manager.Stats().Compactions != 0 {
		t.Fatal("failed projection changed live context")
	}
}

func TestIncrementalCompactionKeepsSkillResourceOutsideSummary(t *testing.T) {
	manager := incrementalFixture(t)
	call := memoryCall("get_skill", `{"id":"go"}`)
	manager.Append(client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}})
	manager.Append(client.ToolResultMessage(call, client.ToolResult{Content: `{"skill":{"id":"go"},"path":"SKILL.md","content":"Exact skill instructions"}`}))
	appendMemoryUpdate(t, manager, CheckpointTool, `{"expected_revision":3}`)
	plan := incrementalPlan(t, manager)
	checkpoint, ready := plan.CheckpointWithoutModel()
	if !ready || len(plan.RetainedSkillResources) != 2 || !strings.Contains(plan.RetainedSkillResources[1].Content, "Exact skill instructions") {
		t.Fatal("skill filtering broke the coverage boundary or resource retention")
	}
	if err := manager.Apply(plan, checkpoint); err != nil {
		t.Fatal(err)
	}
}
