package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileDefaultAndSafeCorrelatedOutput(t *testing.T) {
	directory := t.TempDir()
	var stdout, stderr bytes.Buffer
	options := testOptions()
	options.MaxFileBytes = 1 << 20
	options.MaxTotalBytes = 2 << 20
	runtime, err := Open(directory, options, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRequestID(WithLogger(t.Context(), runtime.Logger()), "")
	Logger(ctx).Info("studio.test", "error", errors.New("private-error-token"), "content", "private-body", "detail", "private-detail")
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	records, _ := readLogs(t, filepath.Join(directory, "logs", "studio"))
	if len(records) != 1 || !bytes.Contains(records[0], []byte(RequestID(ctx))) || bytes.Contains(records[0], []byte("private")) || stdout.Len() != 0 {
		t.Fatalf("file=%s stdout=%s", records, stdout.String())
	}
}

func TestStdoutOptInUsesSameEventIdentity(t *testing.T) {
	directory := t.TempDir()
	options := testOptions()
	options.MaxFileBytes = 1 << 20
	options.MaxTotalBytes = 2 << 20
	options.Stdout = true
	var output bytes.Buffer
	runtime, err := Open(directory, options, &output, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Logger().Info("studio.test")
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	records, _ := readLogs(t, filepath.Join(directory, "logs", "studio"))
	if len(records) != 1 || !bytes.Equal(bytes.TrimSpace(output.Bytes()), records[0]) {
		t.Fatalf("stdout=%s files=%s", output.Bytes(), records)
	}
}

func TestFileFailureDoesNotStopStdout(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "logs"), []byte("obstruction"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	options.Stdout = true
	var output, fallback bytes.Buffer
	runtime, err := Open(directory, options, &output, &fallback)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Logger().Info("studio.test")
	if err := runtime.Close(t.Context()); err == nil {
		t.Fatal("missing sink failure summary")
	}
	if !strings.Contains(output.String(), "studio.test") || !strings.Contains(fallback.String(), "degraded") || strings.Contains(fallback.String(), directory) {
		t.Fatalf("output=%s fallback=%s", output.String(), fallback.String())
	}
}

type blockedWriter struct {
	started chan struct{}
	release chan struct{}
}

func (w *blockedWriter) Write(body []byte) (int, error) {
	select {
	case w.started <- struct{}{}:
	default:
	}
	<-w.release
	return len(body), nil
}

func TestSlowStdoutDoesNotBlockFilesOrShutdown(t *testing.T) {
	directory := t.TempDir()
	options := testOptions()
	options.MaxFileBytes = 1 << 20
	options.MaxTotalBytes = 2 << 20
	options.Stdout = true
	writer := &blockedWriter{started: make(chan struct{}, 1), release: make(chan struct{})}
	runtime, err := Open(directory, options, writer, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(writer.release); _ = runtime.Close(t.Context()) })
	runtime.Logger().Info("studio.before_block")
	select {
	case <-writer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("stdout worker did not start")
	}
	for range 300 {
		runtime.Logger().Info("studio.overflow")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := runtime.Close(ctx); err == nil {
		t.Fatal("blocked sink did not respect shutdown deadline")
	}
	records, _ := readLogs(t, filepath.Join(directory, "logs", "studio"))
	if len(records) == 0 || runtime.sinks[1].dropped.Load() == 0 {
		t.Fatal("file isolation or bounded queue failed")
	}
}

func TestCorrelationInheritDoesNotCopyCancellation(t *testing.T) {
	source, cancel := context.WithCancel(WithRequestID(t.Context(), ""))
	cancel()
	child := Inherit(t.Context(), source)
	if child.Err() != nil || RequestID(child) != RequestID(source) {
		t.Fatal("diagnostic inheritance changed lifetime or identity")
	}
	if Logger(t.Context()).Enabled(t.Context(), slog.LevelInfo) {
		t.Fatal("unconfigured diagnostics enabled")
	}
}

func TestRecordBoundsAndSafeHTTPFailures(t *testing.T) {
	directory := t.TempDir()
	options := testOptions()
	options.MaxFileBytes = 1 << 20
	options.MaxTotalBytes = 2 << 20
	runtime, err := Open(directory, options, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Logger().Info("studio.truncation", "description", strings.Repeat("가", 1000))
	HTTPErrorLogger(runtime.Logger()).Print("private panic token and stack dump")
	attrs := []any{}
	for index := range 30 {
		attrs = append(attrs, fmt.Sprintf("field_%d", index), strings.Repeat("x", 2048))
	}
	runtime.Logger().Info("studio.too_large", attrs...)
	if err := runtime.Close(t.Context()); err == nil {
		t.Fatal("oversized record was not counted")
	}
	records, _ := readLogs(t, filepath.Join(directory, "logs", "studio"))
	if len(records) != 2 {
		t.Fatalf("unexpected record count: %d", len(records))
	}
	for _, line := range records {
		if len(line)+1 > maximumRecordBytes || bytes.Contains(line, []byte("private")) || bytes.Contains(line, []byte("too_large")) {
			t.Fatalf("unsafe record: %s", line)
		}
	}
	var record map[string]any
	if err := json.Unmarshal(records[0], &record); err != nil {
		t.Fatal(err)
	}
	if value, ok := record["description"].(map[string]any); !ok || value["truncated"] != true {
		t.Fatalf("truncation metadata missing: %s", records[0])
	}
}
