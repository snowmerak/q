package subagent

import (
	"errors"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
)

type collectingSink struct {
	records []sessionstore.Record
}

func (s *collectingSink) Append(record sessionstore.Record) error {
	s.records = append(s.records, record)
	return nil
}

func TestLifecycleRecordsTaskMessagesAndResult(t *testing.T) {
	sink := &collectingSink{}
	spec := Spec{
		Role: "coder", Model: "coder-model", ReasoningEffort: "high",
	}
	lifecycle, err := NewLifecycle(sink, "run-1", "task-1", "parent-1", &spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Queued("implement the archive"); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Started(""); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Message(client.Message{Role: client.RoleAssistant, Content: "working"}); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Succeeded("done", "archive implemented", map[string]int{"files": 2}); err != nil {
		t.Fatal(err)
	}

	if len(sink.records) != 5 {
		t.Fatalf("records = %#v", sink.records)
	}
	if sink.records[0].Kind != sessionstore.KindTask || sink.records[0].Status != sessionstore.StatusQueued ||
		sink.records[1].Status != sessionstore.StatusRunning {
		t.Fatalf("task records = %#v", sink.records[:2])
	}
	message := sink.records[2]
	if message.Kind != sessionstore.KindMessage || message.Role != "coder" || message.Model != "coder-model" || message.Effort != "high" {
		t.Fatalf("message record = %#v", message)
	}
	result := sink.records[4]
	if result.Kind != sessionstore.KindResult || result.Status != sessionstore.StatusSucceeded || result.ParentID == "" {
		t.Fatalf("result record = %#v", result)
	}
}

func TestLifecycleRecordsFailure(t *testing.T) {
	sink := &collectingSink{}
	spec := Spec{Role: "advisor", Model: "advisor-model"}
	lifecycle, err := NewLifecycle(sink, "run-1", "task-1", "", &spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Started("help the coder"); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Failed(errors.New("cannot resolve approach")); err != nil {
		t.Fatal(err)
	}
	if got := sink.records[len(sink.records)-1]; got.Status != sessionstore.StatusFailed || got.Role != "advisor" {
		t.Fatalf("failure record = %#v", got)
	}
}

func TestLifecycleRecordsActiveFallbackModel(t *testing.T) {
	sink := &collectingSink{}
	spec := Spec{Role: "analyst", Model: "primary", ReasoningEffort: "high"}
	lifecycle, err := NewLifecycle(sink, "run-1", "task-1", "", &spec)
	if err != nil {
		t.Fatal(err)
	}
	if err = lifecycle.Queued("inspect"); err != nil {
		t.Fatal(err)
	}
	spec.Model = "secondary"
	spec.ReasoningEffort = "medium"
	if err = lifecycle.Succeeded("done", "done", nil); err != nil {
		t.Fatal(err)
	}
	for _, record := range sink.records {
		if record.Status == sessionstore.StatusSucceeded && record.Model != "secondary" {
			t.Fatalf("fallback model was not recorded: %#v", record)
		}
	}
}
