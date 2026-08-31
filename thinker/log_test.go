package thinker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLogStoreWritesPrivateInvocationAndPrunesExpiredLogs(t *testing.T) {
	now := time.Date(2026, time.September, 1, 12, 34, 56, 0, time.UTC)
	store := NewLogStore(t.TempDir())
	store.now = func() time.Time { return now }
	directory := store.Directory()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	oldPath := filepath.Join(directory, "thinker-old.json")
	boundaryPath := filepath.Join(directory, "thinker-boundary.json")
	unrelatedPath := filepath.Join(directory, "other.json")
	for _, path := range []string{oldPath, boundaryPath, unrelatedPath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(oldPath, now.Add(-LogRetention-time.Second), now.Add(-LogRetention-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(boundaryPath, now.Add(-LogRetention), now.Add(-LogRetention)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unrelatedPath, now.Add(-10*24*time.Hour), now.Add(-10*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	path, err := store.Write(InvocationLog{
		JobID:       "learn-run-1",
		StartedAt:   now.Add(-1500 * time.Millisecond),
		CompletedAt: now,
		Result:      Result{Proposed: 1, Processed: 1, Registered: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != directory || !strings.HasPrefix(filepath.Base(path), "thinker-20260901T123456.000000000Z-") || !strings.HasSuffix(path, ".json") {
		t.Fatalf("log path = %q", path)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expired log still exists: %v", err)
	}
	for _, retained := range []string{boundaryPath, unrelatedPath} {
		if _, err := os.Stat(retained); err != nil {
			t.Fatalf("retained file %q: %v", retained, err)
		}
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var logged InvocationLog
	if err := json.Unmarshal(body, &logged); err != nil {
		t.Fatal(err)
	}
	if logged.Version != InvocationLogVersion || logged.JobID != "learn-run-1" || logged.DurationMS != 1500 ||
		logged.Result.Registered != 1 || logged.Result.LogPath != "" || logged.Result.LogError != "" {
		t.Fatalf("logged invocation = %#v", logged)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".thinker-") {
			t.Fatalf("temporary log was not removed: %s", entry.Name())
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("log directory permissions = %v", info.Mode().Perm())
		}
		info, err = os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("log permissions = %v", info.Mode().Perm())
		}
	}
}
