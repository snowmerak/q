package app

import (
	"encoding/json"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
)

type collectingRecordArchive struct {
	records []sessionstore.Record
}

func (a *collectingRecordArchive) Append(record sessionstore.Record) error {
	a.records = append(a.records, record)
	return nil
}

func (a *collectingRecordArchive) Flush() error { return nil }

func TestArchiveReadToolRecordsMetadataWithoutRetrievedContent(t *testing.T) {
	archive := &collectingRecordArchive{}
	m := model{archive: archive, runID: "run-1"}
	call := client.ToolCall{
		ID: "call-1", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "search_archive", Arguments: `{"query":"prior decision"}`},
	}
	m.archiveToolCall(call)
	m.archiveMessage(client.Message{
		Role: client.RoleTool, Name: "search_archive", ToolCallID: "call-1",
		Content: `{"hits":[{"content":"a prior decision"}]}`,
	}, sessionstore.StatusSucceeded, false)

	if len(archive.records) != 2 {
		t.Fatalf("records = %#v", archive.records)
	}
	for _, record := range archive.records {
		if record.Content != "" || !containsString(record.Tags, "archive-read") {
			t.Fatalf("archive read record duplicated content: %#v", record)
		}
	}
	var payload archivedMessagePayload
	if err := json.Unmarshal(archive.records[1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Message.Content != "" || payload.Message.Name != "search_archive" {
		t.Fatalf("archived result payload = %#v", payload)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
