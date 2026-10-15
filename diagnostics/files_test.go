package diagnostics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testOptions() Options {
	return Options{FileEnabled: true, MaxFileBytes: 256, MaxTotalBytes: 4096, MaxAge: 7 * 24 * time.Hour}
}

func readLogs(t *testing.T, dir string) ([][]byte, int64) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var records [][]byte
	var total int64
	for _, entry := range entries {
		if !logFileName.MatchString(entry.Name()) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		total += int64(len(body))
		if len(body) > 0 && body[len(body)-1] != '\n' {
			t.Fatal("unterminated record")
		}
		for _, line := range bytes.Split(bytes.TrimSpace(body), []byte("\n")) {
			if !json.Valid(line) {
				t.Fatalf("invalid JSON: %s", line)
			}
			records = append(records, line)
		}
	}
	return records, total
}

func TestFileRotationPreservesWholeRecords(t *testing.T) {
	options := testOptions()
	store := newFileStore(t.TempDir(), strings.Repeat("a", 32), options)
	for index := range 30 {
		body := []byte(fmt.Sprintf("{\"index\":%d,\"value\":\"%s\"}\n", index, strings.Repeat("x", 35)))
		if err := store.write([][]byte{body}); err != nil {
			t.Fatal(err)
		}
	}
	records, total := readLogs(t, store.dir)
	if len(records) != 30 || total > options.MaxTotalBytes {
		t.Fatalf("records=%d bytes=%d", len(records), total)
	}
	paths, _ := filepath.Glob(filepath.Join(store.dir, "*.jsonl"))
	if len(paths) < 2 {
		t.Fatal("did not rotate")
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > options.MaxFileBytes {
			t.Fatalf("oversized file: %d", info.Size())
		}
	}
}

func TestTotalBudgetAndReducedBudgetRemoveOldest(t *testing.T) {
	options := testOptions()
	options.MaxFileBytes = 64
	options.MaxTotalBytes = 128
	store := newFileStore(t.TempDir(), strings.Repeat("b", 32), options)
	for index := range 6 {
		if err := store.write([][]byte{[]byte(fmt.Sprintf("{\"index\":%d,\"value\":\"%s\"}\n", index, strings.Repeat("x", 30)))}); err != nil {
			t.Fatal(err)
		}
	}
	records, total := readLogs(t, store.dir)
	if total > 128 || len(records) != 2 || !bytes.Contains(records[0], []byte(`"index":4`)) {
		t.Fatalf("retained=%s bytes=%d", records, total)
	}
	store.options.MaxTotalBytes = 64
	if err := store.maintain(); err != nil {
		t.Fatal(err)
	}
	records, total = readLogs(t, store.dir)
	if total > 64 || len(records) != 1 || !bytes.Contains(records[0], []byte(`"index":5`)) {
		t.Fatalf("reduced=%s bytes=%d", records, total)
	}
}

func TestRetentionBoundaryAndDeletionScope(t *testing.T) {
	store := newFileStore(t.TempDir(), strings.Repeat("c", 32), testOptions())
	if err := store.maintain(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	names := []string{}
	for index, age := range []time.Duration{store.options.MaxAge + time.Second, store.options.MaxAge} {
		name := fmt.Sprintf("studio-%s-%s-%06d.jsonl", now.Format("20060102T150405.000000000Z"), store.instance, index)
		path := filepath.Join(store.dir, name)
		names = append(names, path)
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	foreign := filepath.Join(store.dir, "unrelated.jsonl")
	if err := os.WriteFile(foreign, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	thinker := filepath.Join(filepath.Dir(store.dir), "thinker", "old.json")
	if err := os.MkdirAll(filepath.Dir(thinker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thinker, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.maintain(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(names[0]); !os.IsNotExist(err) {
		t.Fatalf("expired file: %v", err)
	}
	for _, path := range []string{names[1], foreign, thinker} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLogProcessWorker(t *testing.T) {
	directory := os.Getenv("Q_DIAGNOSTIC_TEST_DIRECTORY")
	if directory == "" {
		t.Skip("subprocess helper")
	}
	options := testOptions()
	options.MaxTotalBytes = 1024
	store := newFileStore(directory, os.Getenv("Q_DIAGNOSTIC_TEST_INSTANCE"), options)
	for index := range 8 {
		if err := store.write([][]byte{[]byte(fmt.Sprintf("{\"index\":%d,\"value\":\"%s\"}\n", index, strings.Repeat("x", 100)))}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTwoProcessesShareTotalBudget(t *testing.T) {
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var commands []*exec.Cmd
	var outputs []*bytes.Buffer
	for _, instance := range []string{strings.Repeat("d", 32), strings.Repeat("e", 32)} {
		command := exec.CommandContext(t.Context(), executable, "-test.run=^TestLogProcessWorker$")
		command.Env = append(os.Environ(), "Q_DIAGNOSTIC_TEST_DIRECTORY="+directory, "Q_DIAGNOSTIC_TEST_INSTANCE="+instance)
		output := &bytes.Buffer{}
		command.Stdout = output
		command.Stderr = output
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
		outputs = append(outputs, output)
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("worker: %v %s", err, outputs[index])
		}
	}
	records, total := readLogs(t, filepath.Join(directory, "logs", "studio"))
	if total > 1024 || len(records) == 0 {
		t.Fatalf("total=%d records=%d", total, len(records))
	}
}
