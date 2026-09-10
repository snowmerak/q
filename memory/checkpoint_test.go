package memory

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestApplyCheckpointRecoversAndNormalizesSmallModelJSON(t *testing.T) {
	manager := New(Policy{ContextWindow: 16_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}, nil)
	response := "Here is the checkpoint:\n```json\n" + `{
 current_request: ['Improve context compaction'],
 active_work: ["Implementing the tolerant parser",],
 previous_work: "Inspected memory.Manager",
 facts: {window: '16k', path: 'memory/manager.go'},
}` + "\n```"

	canonical, err := manager.ApplyCheckpoint(Plan{Source: []client.Message{{Role: client.RoleUser, Content: "history"}}}, response)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal([]byte(canonical), &checkpoint); err != nil {
		t.Fatalf("canonical checkpoint is invalid JSON: %v\n%s", err, canonical)
	}
	if !reflect.DeepEqual(checkpoint.CurrentRequest, []string{"Improve context compaction"}) ||
		!reflect.DeepEqual(checkpoint.ActiveWork, []string{"Implementing the tolerant parser"}) ||
		!reflect.DeepEqual(checkpoint.PreviousWork, []string{"Inspected memory.Manager"}) ||
		!reflect.DeepEqual(checkpoint.Facts, []string{"path: memory/manager.go", "window: 16k"}) {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	if messages := manager.Messages(); len(messages) != 1 || messages[0].Content != checkpointHeading+canonical {
		t.Fatalf("compacted messages = %#v", messages)
	}
}

func TestApplyCheckpointRepairsControlCharactersInsideStrings(t *testing.T) {
	manager := New(Policy{ContextWindow: 16_000}, nil)
	response := "{\"current_request\":\"first line\nsecond line\",\"active_work\":[],\"previous_work\":[],\"facts\":[]}"
	canonical, err := manager.ApplyCheckpoint(Plan{Source: []client.Message{{Role: client.RoleUser, Content: "history"}}}, response)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal([]byte(canonical), &checkpoint); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(checkpoint.CurrentRequest, []string{"first line\nsecond line"}) {
		t.Fatalf("current request = %#v", checkpoint.CurrentRequest)
	}
}

func TestApplyCheckpointSkipsUnrelatedJSONBeforeCheckpoint(t *testing.T) {
	manager := New(Policy{ContextWindow: 16_000}, nil)
	response := `I used {"format":"json"} and recovered the result: {"active_work":"Apply the recovered checkpoint"}`
	canonical, err := manager.ApplyCheckpoint(Plan{Source: []client.Message{{Role: client.RoleUser, Content: "history"}}}, response)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal([]byte(canonical), &checkpoint); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(checkpoint.ActiveWork, []string{"Apply the recovered checkpoint"}) {
		t.Fatalf("active work = %#v", checkpoint.ActiveWork)
	}
}

func TestApplyCheckpointKeepsPriorSectionsOmittedBySmallModel(t *testing.T) {
	manager := New(Policy{ContextWindow: 16_000}, nil)
	firstPlan := Plan{Source: []client.Message{{Role: client.RoleUser, Content: "history"}}}
	_, err := manager.ApplyCheckpoint(firstPlan, `{
		"current_request":["Fix compaction"],
		"active_work":["Inspecting code"],
		"previous_work":["Located memory.Manager"],
		"facts":["The transcript remains authoritative"]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	prior := manager.Messages()[0]
	canonical, err := manager.ApplyCheckpoint(Plan{Source: []client.Message{prior}}, `{"active_work":"Writing recovery tests"}`)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal([]byte(canonical), &checkpoint); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(checkpoint.CurrentRequest, []string{"Fix compaction"}) ||
		!reflect.DeepEqual(checkpoint.ActiveWork, []string{"Writing recovery tests"}) ||
		!reflect.DeepEqual(checkpoint.PreviousWork, []string{"Located memory.Manager"}) ||
		!reflect.DeepEqual(checkpoint.Facts, []string{"The transcript remains authoritative"}) {
		t.Fatalf("merged checkpoint = %#v", checkpoint)
	}
}

func TestApplyCheckpointRejectsUnrecoverableResponseWithoutMutation(t *testing.T) {
	history := []client.Message{{Role: client.RoleUser, Content: "keep the original history"}}
	manager := New(Policy{ContextWindow: 16_000}, history)
	before := manager.Messages()
	if _, err := manager.ApplyCheckpoint(Plan{Source: history}, "I could not produce JSON."); err == nil {
		t.Fatal("unrecoverable response unexpectedly applied")
	}
	if !reflect.DeepEqual(manager.Messages(), before) || manager.Stats().Compactions != 0 {
		t.Fatal("failed checkpoint mutated memory")
	}
}

func TestCheckpointRequestUsesSimpleSessionStateContract(t *testing.T) {
	request := (Plan{OutputBudget: 512, Source: []client.Message{{Role: client.RoleUser, Content: "history"}}}).RequestMessages()
	if len(request) != 2 {
		t.Fatalf("request messages = %#v", request)
	}
	for _, required := range []string{"current_request", "active_work", "previous_work", "facts", "Do not copy raw tool output"} {
		if !strings.Contains(request[0].Content, required) {
			t.Fatalf("checkpoint prompt is missing %q:\n%s", required, request[0].Content)
		}
	}
}

func BenchmarkDecodeRecoverableJSONObject(b *testing.B) {
	response := "Checkpoint follows:\n```json\n" + `{
		current_request: ['Continue the active task'],
		active_work: ['Run the focused tests',],
		previous_work: ['Inspected the request context',],
		facts: ['The full transcript is retained',],
	}` + "\n```"
	b.ReportAllocs()
	for b.Loop() {
		if _, err := decodeRecoverableJSONObject(response); err != nil {
			b.Fatal(err)
		}
	}
}
