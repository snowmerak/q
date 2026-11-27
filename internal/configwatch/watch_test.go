package configwatch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/snowmerak/q/internal/fsreplace"
)

func waitChange(t *testing.T, w *Watcher) Change {
	t.Helper()
	select {
	case change := <-w.Changes():
		return change
	case <-time.After(5 * time.Second):
		t.Fatal("no change reported")
		return Change{}
	}
}

func expectQuiet(t *testing.T, w *Watcher, wait time.Duration) {
	t.Helper()
	select {
	case change := <-w.Changes():
		t.Fatalf("unexpected change: %v", change.Paths)
	case <-time.After(wait):
	}
}

func replaceKeepingModTime(t *testing.T, path string, body []byte) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(filepath.Dir(path), ".replacement")
	if err := os.WriteFile(replacement, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := fsreplace.Replace(replacement, path); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		t.Fatalf("replacement changed metadata: size %d -> %d, mtime %v -> %v", info.Size(), after.Size(), info.ModTime(), after.ModTime())
	}
}

func TestWatcherReportsSameMetadataReplacementOnce(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "systemone.json")
	if err := os.WriteFile(path, []byte(`{"api_keys":["a"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := New(ctx, []string{path}, Options{Debounce: 20 * time.Millisecond})

	replaceKeepingModTime(t, path, []byte(`{"api_keys":["b"]}`))
	if change := waitChange(t, w); !slices.Equal(change.Paths, []string{path}) {
		t.Fatalf("paths = %v", change.Paths)
	}
	expectQuiet(t, w, 200*time.Millisecond)

	// Rewriting identical contents is not a change.
	replaceKeepingModTime(t, path, []byte(`{"api_keys":["b"]}`))
	expectQuiet(t, w, 200*time.Millisecond)

	cancel()
	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not stop")
	}
}

func TestWatcherFiltersUnrelatedFilesAndReportsCreation(t *testing.T) {
	directory := t.TempDir()
	gateway := filepath.Join(directory, "gateway.json")
	providers := filepath.Join(directory, "providers.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := New(ctx, []string{providers, gateway}, Options{Debounce: 20 * time.Millisecond})

	if err := os.WriteFile(filepath.Join(directory, "unrelated.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectQuiet(t, w, 200*time.Millisecond)

	if err := os.WriteFile(gateway, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(providers, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen []string
	for len(seen) < 2 {
		for _, path := range waitChange(t, w).Paths {
			if !slices.Contains(seen, path) {
				seen = append(seen, path)
			}
		}
	}
	if err := os.Remove(gateway); err != nil {
		t.Fatal(err)
	}
	if change := waitChange(t, w); !slices.Equal(change.Paths, []string{gateway}) {
		t.Fatalf("removal paths = %v", change.Paths)
	}
}

func TestWatcherPollsDirectoryCreatedLater(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "config")
	path := filepath.Join(directory, "gateway.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := New(ctx, []string{path}, Options{Debounce: 20 * time.Millisecond, PollInterval: 50 * time.Millisecond})

	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if change := waitChange(t, w); !slices.Equal(change.Paths, []string{path}) {
		t.Fatalf("paths = %v", change.Paths)
	}
	// Once the directory exists, events are delivered without polling.
	replaceKeepingModTime(t, path, []byte("[]"))
	if change := waitChange(t, w); !slices.Equal(change.Paths, []string{path}) {
		t.Fatalf("paths = %v", change.Paths)
	}
}
