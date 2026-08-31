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

func testReviewExecutionLog() subagent.ReviewExecutionLog {
	started := time.Date(2026, time.August, 31, 1, 2, 3, 0, time.UTC)
	return subagent.ReviewExecutionLog{
		RunID: "run-review", Objective: "Review current changes",
		StartedAt: started, CompletedAt: started.Add(time.Minute), Outcome: subagent.ReviewOutcomeSucceeded,
		Files: []subagent.ReviewFile{{
			Path: "main.go", Status: " M",
			Sections: []subagent.ReviewPatch{{Title: "UNSTAGED", Patch: "@@ -1 +1 @@\n-old\n+new\n"}},
		}},
		Report: &subagent.ReviewReport{
			Summary: "One correctness issue was found.", Verdict: "request_changes",
			Findings: []subagent.ReviewFinding{{
				Severity: "high", Title: "State is stale", Explanation: "The update skips invalidation.",
				Locations: []subagent.ReviewLocation{{Path: "main.go", Line: 1}}, Recommendation: "Invalidate state.",
			}},
		},
		Events: []subagent.PlanningEvent{{
			Sequence: 1, At: started, Type: subagent.PlanningEventInput, Agent: "review", Content: "bounded diff",
		}},
	}
}

func TestArchiveReviewExecutionWritesCompleteUniqueHistory(t *testing.T) {
	store := Store{Root: t.TempDir(), SessionID: "session-review"}
	execution := testReviewExecutionLog()
	now := time.Date(2026, time.August, 31, 12, 34, 56, 123456789, time.FixedZone("KST", 9*60*60))
	seen := make(map[string]bool)
	for range 2 {
		path, err := store.archiveReviewExecutionAt(execution, now)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(path) != store.ReviewExecutionHistoryDir() ||
			!strings.HasPrefix(filepath.Base(path), "review-execution-20260831T033456.123456789Z-") ||
			!strings.HasSuffix(path, ".json") || seen[path] {
			t.Fatalf("invalid or reused review history path: %s", path)
		}
		seen[path] = true
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var stored reviewExecutionFile
		if err := json.Unmarshal(body, &stored); err != nil {
			t.Fatal(err)
		}
		if stored.Version != executionCurrentVersion || stored.SessionID != store.SessionID ||
			!reflect.DeepEqual(stored.Execution, execution) || !strings.Contains(string(body), `"request_changes"`) ||
			!strings.Contains(string(body), `"UNSTAGED"`) {
			t.Fatalf("review execution history is incomplete: %s", body)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("review history permissions are too broad: %o", info.Mode().Perm())
		}
	}
}

func TestArchiveReviewExecutionRejectsInvalidLogsWithoutCreatingHistory(t *testing.T) {
	store := Store{Root: t.TempDir()}
	tests := []subagent.ReviewExecutionLog{
		{},
		func() subagent.ReviewExecutionLog {
			value := testReviewExecutionLog()
			value.Report = nil
			return value
		}(),
		func() subagent.ReviewExecutionLog {
			value := testReviewExecutionLog()
			value.Outcome = subagent.ReviewOutcomeFailed
			value.Report = nil
			return value
		}(),
	}
	for _, execution := range tests {
		if path, err := store.ArchiveReviewExecution(execution); err == nil || path != "" {
			t.Fatalf("invalid review execution archived: path=%q err=%v", path, err)
		}
	}
	if _, err := os.Stat(store.ReviewExecutionHistoryDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid logs created review history: %v", err)
	}
}
