package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/subagent"
)

func testDebugExecutionLog() subagent.DebugExecutionLog {
	started := time.Date(2026, time.August, 31, 1, 2, 3, 0, time.UTC)
	return subagent.DebugExecutionLog{
		RunID: "run-debug", Objective: "Investigate session replacement failure",
		StartedAt: started, CompletedAt: started.Add(time.Minute), Outcome: subagent.DebugOutcomeSucceeded,
		Brief: &subagent.GrillBrief{
			Objective:  "Investigate session replacement failure",
			Conditions: []string{"The failure occurs on Windows"},
		},
		Report: &subagent.DebugReport{
			Summary:     "The atomic replacement is temporarily blocked.",
			Findings:    []string{"MoveFileExW returned access denied"},
			LikelyCause: "A short-lived file handle holds the destination.", Confidence: "medium",
			SuggestedFixes: []string{"Retry with a short backoff"}, Verification: []string{"Simulate a transient lock"},
		},
		Events: []subagent.PlanningEvent{
			{Sequence: 1, At: started, Type: subagent.PlanningEventInput, Agent: "debug", Content: "reported failure"},
			{Sequence: 2, At: started.Add(time.Second), Type: subagent.PlanningEventToolCall, Agent: "scout", Name: "read_file", Content: `{"path":"workspace/session.go"}`},
			{Sequence: 3, At: started.Add(2 * time.Second), Type: subagent.PlanningEventToolResult, Agent: "scout", Name: "read_file", Content: "replacement implementation"},
		},
	}
}

func TestArchiveDebugExecutionWritesCompleteUniqueHistory(t *testing.T) {
	store := Store{Root: t.TempDir(), SessionID: "session-debug"}
	execution := testDebugExecutionLog()
	now := time.Date(2026, time.August, 31, 12, 34, 56, 123456789, time.FixedZone("KST", 9*60*60))
	seen := make(map[string]bool)
	for range 2 {
		path, err := store.archiveDebugExecutionAt(execution, now)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(path) != store.DebugExecutionHistoryDir() ||
			!strings.HasPrefix(filepath.Base(path), "debug-execution-20260831T033456.123456789Z-") ||
			!strings.HasSuffix(path, ".json") || seen[path] {
			t.Fatalf("invalid or reused debug history path: %s", path)
		}
		seen[path] = true
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var stored debugExecutionFile
		if err := json.Unmarshal(body, &stored); err != nil {
			t.Fatal(err)
		}
		if stored.Version != executionCurrentVersion || stored.SessionID != store.SessionID ||
			!reflect.DeepEqual(stored.Execution, execution) || !strings.Contains(string(body), `"tool_result"`) ||
			!strings.Contains(string(body), `"likely_cause"`) {
			t.Fatalf("debug execution history is incomplete: %s", body)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("debug history permissions are too broad: %o", info.Mode().Perm())
		}
	}
}

func TestArchiveDebugExecutionRejectsInvalidLogsWithoutCreatingHistory(t *testing.T) {
	store := Store{Root: t.TempDir()}
	tests := []subagent.DebugExecutionLog{
		{},
		func() subagent.DebugExecutionLog {
			value := testDebugExecutionLog()
			value.Report = nil
			return value
		}(),
		func() subagent.DebugExecutionLog {
			value := testDebugExecutionLog()
			value.Outcome = subagent.DebugOutcomeFailed
			value.Report = nil
			return value
		}(),
	}
	for _, execution := range tests {
		if path, err := store.ArchiveDebugExecution(execution); err == nil || path != "" {
			t.Fatalf("invalid debug execution archived: path=%q err=%v", path, err)
		}
	}
	if _, err := os.Stat(store.DebugExecutionHistoryDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid logs created debug history: %v", err)
	}
}
