package memory

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func memoryCall(name, arguments string) client.ToolCall {
	return client.ToolCall{ID: name, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: name, Arguments: arguments}}
}

func TestMemoryToolsUpdateReplayAndCompaction(t *testing.T) {
	manager := New(Policy{ContextWindow: 16_000}, []client.Message{{Role: client.RoleUser, Content: "Inspect the project"}})
	set := memoryCall(SetActiveWorkTool, `{"description":"Inspect project structure","next_action":"read entrypoint"}`)
	setResult, handled := manager.CallMemoryTool(set)
	if !handled || setResult.IsError {
		t.Fatalf("set result = %#v, handled=%v", setResult, handled)
	}
	var setDelta memoryDelta
	if err := json.Unmarshal([]byte(setResult.Content), &setDelta); err != nil {
		t.Fatal(err)
	}
	if setDelta.Entry.ID == "" || setDelta.Entry.Text != "Inspect project structure; next: read entrypoint" {
		t.Fatalf("set delta = %#v", setDelta)
	}
	manager.Append(client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{set}})
	manager.Append(client.ToolResultMessage(set, setResult))

	fact := memoryCall(RecordFactTool, `{"fact":"main.go starts the server","source":"loom://evidence"}`)
	factResult, handled := manager.CallMemoryTool(fact)
	if !handled || factResult.IsError {
		t.Fatalf("fact result = %#v, handled=%v", factResult, handled)
	}
	manager.Append(client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{fact}})
	manager.Append(client.ToolResultMessage(fact, factResult))

	completeArgs, _ := json.Marshal(map[string]string{
		"work_id": setDelta.Entry.ID, "result": "Found the entrypoint", "next_work": "Inspect tests",
	})
	complete := memoryCall(CompleteWorkTool, string(completeArgs))
	completeResult, handled := manager.CallMemoryTool(complete)
	if !handled || completeResult.IsError {
		t.Fatalf("complete result = %#v, handled=%v", completeResult, handled)
	}
	manager.Append(client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{complete}})
	manager.Append(client.ToolResultMessage(complete, completeResult))

	restored := New(Policy{ContextWindow: 16_000}, manager.Messages())
	if !reflect.DeepEqual(restored.taskMemory.active, manager.taskMemory.active) ||
		!reflect.DeepEqual(restored.taskMemory.previous, manager.taskMemory.previous) ||
		!reflect.DeepEqual(restored.taskMemory.facts, manager.taskMemory.facts) {
		t.Fatalf("replayed state differs: %#v vs %#v", restored.taskMemory, manager.taskMemory)
	}
	if len(restored.taskMemory.active) != 1 || len(restored.taskMemory.previous) != 1 || len(restored.taskMemory.facts) != 1 {
		t.Fatalf("unexpected task memory = %#v", restored.taskMemory)
	}

	// A weak compaction response must not erase updates recorded through tools.
	checkpoint, err := restored.ApplyCheckpoint(Plan{Source: restored.Messages()},
		`{"current_request":["Inspect the project"],"active_work":[],"previous_work":[],"facts":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	var value Checkpoint
	if err := json.Unmarshal([]byte(checkpoint), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.ActiveWork) != 1 || len(value.PreviousWork) != 1 || len(value.Facts) != 1 {
		t.Fatalf("maintained state was lost: %#v", value)
	}
	if !strings.Contains(value.ActiveWork[0], "Inspect tests") || !strings.Contains(value.PreviousWork[0], "Found the entrypoint") {
		t.Fatalf("unexpected checkpoint = %#v", value)
	}
	resumed := New(Policy{ContextWindow: 16_000}, restored.Messages())
	if len(resumed.taskMemory.active) != 1 || len(resumed.taskMemory.previous) != 1 || len(resumed.taskMemory.facts) != 1 {
		t.Fatalf("compacted replay lost state: %#v", resumed.taskMemory)
	}
}

func TestMemoryToolsRejectInvalidUpdateWithoutMutation(t *testing.T) {
	manager := New(Policy{}, nil)
	invalid := memoryCall(CompleteWorkTool, `{"work_id":"missing","result":"done"}`)
	result, handled := manager.CallMemoryTool(invalid)
	if !handled || !result.IsError || len(manager.taskMemory.previous) != 0 {
		t.Fatalf("invalid completion changed state: %#v", manager.taskMemory)
	}
	result, handled = manager.CallMemoryTool(memoryCall(RecordFactTool, `{"fact":"confirmed","unexpected":true}`))
	if !handled || !result.IsError || len(manager.taskMemory.facts) != 0 {
		t.Fatalf("invalid fact changed state: %#v", manager.taskMemory)
	}
	if len(AppendMemoryTools(MemoryTools())) != 3 {
		t.Fatal("memory tools were duplicated")
	}
}
