package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/diagnostics"
	"github.com/snowmerak/q/workspace"
)

func TestHTTPIncomingRequestCorrelation(t *testing.T) {
	id := uuid.NewString()
	for _, supplied := range []string{id, "", "private-invalid-id"} {
		t.Run(supplied, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
			var received string
			handler := logRequests(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				received = diagnostics.RequestID(request.Context())
			}), logger)
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("X-Request-ID", supplied)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if _, err := uuid.Parse(received); err != nil || response.Header().Get("X-Request-ID") != received {
				t.Fatalf("correlation = %q, response = %v", received, response.Header())
			}
			if supplied == id && received != id {
				t.Fatal("valid incoming request ID was replaced")
			}
			if !strings.Contains(output.String(), received) || strings.Contains(output.String(), "private-invalid-id") {
				t.Fatalf("unsafe or missing correlation: %s", output.String())
			}
		})
	}
}

func TestStudioRequestLogsStatusAndOmitsPayloads(t *testing.T) {
	for _, test := range []struct {
		status int
		level  string
	}{
		{http.StatusOK, "DEBUG"},
		{http.StatusBadRequest, "DEBUG"},
		{http.StatusInternalServerError, "ERROR"},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
			handler := logRequests(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("X-Test", "preserved")
				request.Pattern = "POST /api/v1/settings"
				if test.status == http.StatusOK {
					_, _ = writer.Write([]byte("private response"))
					return
				}
				writeAPIError(writer, test.status, errors.New("operation failed"))
			}), logger)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/settings?api_key=secret-key", strings.NewReader("private prompt"))
			request.Header.Set("Authorization", "Bearer private-token")
			handler.ServeHTTP(response, request)
			if response.Code != test.status || response.Header().Get("X-Test") != "preserved" {
				t.Fatalf("response = %d %v", response.Code, response.Header())
			}
			var record map[string]any
			if err := json.Unmarshal(output.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if record["level"] != test.level || record["status"] != float64(test.status) || record["method"] != http.MethodPost || record["route"] != "POST /api/v1/settings" || record["duration_ms"] == nil || record["request_id"] != response.Header().Get("X-Request-ID") || record["request_id"] == "" {
				t.Fatalf("request log = %s", output.String())
			}
			if test.status != http.StatusOK && record["error"] != "operation failed" {
				t.Fatalf("missing API failure detail: %s", output.String())
			}
			for _, private := range []string{"secret-key", "private prompt", "private response", "private-token"} {
				if strings.Contains(output.String(), private) {
					t.Fatalf("log contains %q: %s", private, output.String())
				}
			}
		})
	}
}

func TestStudioRequestLoggingPreservesResponseController(t *testing.T) {
	response := httptest.NewRecorder()
	writer := &loggedResponseWriter{ResponseWriter: response}
	writer.WriteHeader(http.StatusEarlyHints)
	writer.WriteHeader(http.StatusCreated)
	writer.WriteHeader(http.StatusInternalServerError)
	if writer.status != http.StatusCreated {
		t.Fatalf("logged final status = %d", writer.status)
	}
	if err := http.NewResponseController(writer).Flush(); err != nil {
		t.Fatal(err)
	}
	if !response.Flushed {
		t.Fatal("underlying response was not flushed")
	}
	response = httptest.NewRecorder()
	writer = &loggedResponseWriter{ResponseWriter: response}
	if err := http.NewResponseController(writer).Flush(); err != nil {
		t.Fatal(err)
	}
	writer.WriteHeader(http.StatusInternalServerError)
	if writer.status != http.StatusOK || response.Code != http.StatusOK {
		t.Fatalf("implicit flush status: logged=%d sent=%d", writer.status, response.Code)
	}
}

func TestStudioLoggerIsScopedToServer(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), config.Store{Dir: t.TempDir()}, nil, WithLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = errors.Join(sessions.Close(), commits.Close()) })
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if !strings.Contains(output.String(), "studio.request.completed") {
		t.Fatalf("missing request log: %s", output.String())
	}
	before := output.String()
	defaultHandler, defaultCommits, defaultSessions, err := newHandlerRuntime(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = errors.Join(defaultSessions.Close(), defaultCommits.Close()) })
	defaultHandler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if output.String() != before {
		t.Fatal("default logger reused the configured server logger")
	}
}

func TestStudioRunLogsLifecycleWithoutConversation(t *testing.T) {
	for _, test := range []struct {
		name   string
		event  app.SessionEvent
		err    error
		status string
		level  string
	}{
		{name: "success", status: "completed", level: "INFO"},
		{name: "failure", err: errors.New("model unavailable"), status: "failed", level: "ERROR"},
		{name: "error event", event: app.SessionEvent{Type: "error", Detail: "model unavailable"}, status: "failed", level: "ERROR"},
		{name: "cancelled", event: app.SessionEvent{Type: "cancelled"}, err: context.Canceled, status: "cancelled", level: "INFO"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, lock, err := workspace.CreateSession(t.TempDir(), "test")
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			runner := sessionRunnerFunc(func(_ context.Context, _ workspace.Store, _, _ string, emit app.SessionEventSink) error {
				for _, event := range []app.SessionEvent{
					{Type: "tool_call", Name: "read_file", CallID: "call-1", Content: "private arguments"},
					{Type: "message", Role: "tool", Name: "read_file", CallID: "call-1", Content: "private file"},
					{Type: "trace", Kind: "tool_call", Agent: "child", TaskID: "task-1", ParentID: "parent-1", Name: "search", CallID: "nested-1", Content: "private arguments"},
					{Type: "trace", Kind: "tool_result", Agent: "child", TaskID: "task-1", ParentID: "parent-1", Name: "search", CallID: "nested-1", Content: "private result"},
					{Type: "stream", Content: "private response"},
				} {
					if err := emit(event); err != nil {
						return err
					}
				}
				if test.event.Type != "" {
					if err := emit(test.event); err != nil {
						return err
					}
				}
				return test.err
			})
			var output bytes.Buffer
			service := newSessionRunService(t.Context(), runner)
			service.logger = slog.New(slog.NewJSONHandler(&output, nil))
			t.Cleanup(func() { _ = service.Close() })
			run, err := service.start(store.Root, store.SessionID, "private prompt")
			if err != nil {
				t.Fatal(err)
			}
			service.wg.Wait()
			var records []map[string]any
			for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
				var record map[string]any
				if err := json.Unmarshal(line, &record); err != nil {
					t.Fatal(err)
				}
				if record["session_id"] != store.SessionID || record["run_id"] != run.snapshotCopy().ID {
					t.Fatalf("missing run identity: %s", line)
				}
				records = append(records, record)
			}
			first, last := records[0], records[len(records)-1]
			if first["msg"] != "studio.run.started" || last["msg"] != "studio.run.finished" || last["status"] != test.status || last["level"] != test.level || last["duration_ms"] == nil {
				t.Fatalf("lifecycle log = %s", output.String())
			}
			if !strings.Contains(output.String(), "studio.tool.finished") || strings.Contains(output.String(), "private") {
				t.Fatalf("unexpected tool or payload log: %s", output.String())
			}
			starts, finishes := 0, 0
			for _, record := range records {
				if record["msg"] == "studio.tool.started" {
					starts++
				}
				if record["msg"] == "studio.tool.finished" {
					finishes++
					if record["level"] != "INFO" || record["duration_ms"] == nil {
						t.Fatalf("missing default tool detail: %+v", record)
					}
				}
				if record["call_id"] == "nested-1" && (record["agent"] != "child" || record["task_id"] != "task-1" || record["parent_id"] != "parent-1") {
					t.Fatalf("missing nested tool identity: %+v", record)
				}
			}
			if starts != 2 || finishes != 2 {
				t.Fatalf("tool logs = %d starts, %d finishes", starts, finishes)
			}
		})
	}
}

func TestRequestCorrelationSurvivesBrowserCancellation(t *testing.T) {
	store, lock, err := workspace.CreateSession(t.TempDir(), "correlation")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 1)
	release := make(chan struct{})
	runner := sessionRunnerFunc(func(ctx context.Context, _ workspace.Store, _, _ string, emit app.SessionEventSink) error {
		started <- diagnostics.RequestID(ctx)
		<-release
		if err := ctx.Err(); err != nil {
			return err
		}
		return emit(app.SessionEvent{Type: "result", Outcome: "succeeded"})
	})
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), config.Store{Dir: t.TempDir()}, runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close(); _ = commits.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+store.SessionID+"/messages", strings.NewReader(`{"workspace_root":`+quotedJSON(store.Root)+`,"content":"work"}`)).WithContext(ctx)
	request.Header.Set("X-Request-ID", "untrusted-private-value")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	cancel()
	close(release)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start=%d %s", response.Code, response.Body.String())
	}
	requestID := response.Header().Get("X-Request-ID")
	if id := <-started; id == "" || id != requestID || id == "untrusted-private-value" {
		t.Fatalf("request IDs: %q %q", id, requestID)
	}
	sessions.runs.wg.Wait()
	run, err := sessions.runs.latest(store.Root, store.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if run.snapshotCopy().RequestID != requestID {
		t.Fatal("snapshot lost correlation")
	}
	reloaded, err := loadLatestStudioRun(store.Root, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range reloaded.page(0, maximumRunEventPage).Events {
		if event.Event.RequestID != requestID {
			t.Fatalf("event lost correlation: %+v", event)
		}
	}
}
