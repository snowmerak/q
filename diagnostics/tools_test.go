package diagnostics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
)

func TestToolCallsKeepNestedIdentitiesAndDefaultInfo(t *testing.T) {
	var output bytes.Buffer
	ctx := WithRequestID(WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, nil))), "")
	calls := NewToolCalls("studio.tool")
	first := ToolIdentity{Name: "read_file", CallID: "reused", Agent: "first", TaskID: "task-1"}
	second := ToolIdentity{Name: "search", CallID: "reused", Agent: "second", TaskID: "task-2"}
	calls.Started(ctx, first)
	calls.Started(ctx, second)
	calls.Finished(ctx, second, true)
	calls.Finished(ctx, first, false)
	// A recovered result may have no observed start. Never invent its duration.
	calls.Finished(ctx, first, false)
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	if len(lines) != 5 {
		t.Fatalf("tool events = %s", output.String())
	}
	for index, line := range lines {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record["request_id"] != RequestID(ctx) || record["call_id"] != "reused" {
			t.Fatalf("missing tool correlation: %s", line)
		}
		switch index {
		case 2:
			if record["level"] != "WARN" || record["status"] != "failed" || record["tool"] != "search" || record["agent"] != "second" || record["duration_ms"] == nil {
				t.Fatalf("failed nested tool: %s", line)
			}
		case 3:
			if record["level"] != "INFO" || record["status"] != "completed" || record["tool"] != "read_file" || record["agent"] != "first" || record["duration_ms"] == nil {
				t.Fatalf("successful nested tool: %s", line)
			}
		case 4:
			if record["duration_ms"] != nil {
				t.Fatalf("invented recovery duration: %s", line)
			}
		}
	}
	if len(calls.active) != 0 {
		t.Fatal("completed calls retained timestamps")
	}
}

func TestToolTimingStorageIsBounded(t *testing.T) {
	calls := NewToolCalls("studio.tool")
	for index := range 1100 {
		calls.Started(t.Context(), ToolIdentity{Name: "read_file", CallID: fmt.Sprint(index)})
	}
	if len(calls.active) != 1024 {
		t.Fatalf("outstanding timestamp count = %d", len(calls.active))
	}
}
