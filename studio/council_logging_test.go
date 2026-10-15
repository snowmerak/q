package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/council"
	"github.com/snowmerak/q/diagnostics"
	"github.com/snowmerak/q/workspace"
)

type correlatedCouncilHost struct {
	councilTestHost
	ids      chan string
	gate     chan struct{}
	failNext atomic.Bool
}

func (host *correlatedCouncilHost) RunWithOptions(ctx context.Context, store workspace.Store, id, prompt string, options app.SessionOptions, emit app.SessionEventSink) error {
	host.ids <- diagnostics.RequestID(ctx)
	if host.gate != nil {
		select {
		case <-host.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if host.failNext.Swap(false) {
		return errors.New("private-participant-error")
	}
	if err := emit(app.SessionEvent{Type: "tool_call", Name: "read_file", CallID: "call-1", Content: "private-tool-arguments"}); err != nil {
		return err
	}
	if err := emit(app.SessionEvent{Type: "message", Role: "tool", Name: "read_file", CallID: "call-1", Content: "private-tool-result"}); err != nil {
		return err
	}
	return host.councilTestHost.RunWithOptions(ctx, store, id, prompt, options, emit)
}

type councilLogFixture struct {
	directory string
	handler   http.Handler
	sessions  *sessionsService
	commits   *commitService
	runtime   *diagnostics.Runtime
	value     council.Council
	closed    bool
}

func newCouncilLogFixture(t *testing.T, host *correlatedCouncilHost) *councilLogFixture {
	t.Helper()
	directory := t.TempDir()
	runtime, err := diagnostics.Open(directory, diagnostics.Options{Level: slog.LevelInfo, FileEnabled: true, MaxFileBytes: 1 << 20, MaxTotalBytes: 2 << 20, MaxAge: 7 * 24 * time.Hour}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &councilLogFixture{directory: directory, runtime: runtime}
	t.Cleanup(func() { fixture.close(t) })
	fixture.handler, fixture.commits, fixture.sessions, err = newHandlerRuntime(t.Context(), config.Store{Dir: directory}, host, WithLogger(runtime.Logger()))
	if err != nil {
		t.Fatal(err)
	}
	input := councilInput{Name: "Log council", Scope: council.Independent, Members: []council.Seat{{Model: "test/one"}, {Model: "test/two"}}, Chair: council.Seat{Model: "test/chair"}}
	if status := councilAPI(t, fixture.handler, http.MethodPost, "/api/v1/councils", input, &fixture.value); status != http.StatusCreated {
		t.Fatalf("create = %d", status)
	}
	return fixture
}

func (fixture *councilLogFixture) close(t *testing.T) {
	t.Helper()
	if fixture.closed {
		return
	}
	fixture.closed = true
	var err error
	if fixture.sessions != nil {
		err = errors.Join(err, fixture.sessions.Close())
	}
	if fixture.commits != nil {
		err = errors.Join(err, fixture.commits.Close())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = errors.Join(err, fixture.runtime.Close(ctx)); err != nil {
		t.Error(err)
	}
}

func (fixture *councilLogFixture) request(t *testing.T, ctx context.Context, path, body, requestID string, target any) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("X-Request-ID", requestID)
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("%s = %d %s", path, response.Code, response.Body.String())
	}
	if target != nil {
		if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}
	return response.Header().Get("X-Request-ID")
}

func (fixture *councilLogFixture) records(t *testing.T) []map[string]any {
	t.Helper()
	fixture.close(t)
	paths, err := filepath.Glob(filepath.Join(fixture.directory, "logs", "studio", "*.jsonl"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("log files = %v, %v", paths, err)
	}
	var records []map[string]any
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"private-prompt", "private-participant-error", "private-tool-arguments", "private-tool-result", "Independent answer", "Final council answer"} {
			if bytes.Contains(body, []byte(secret)) {
				t.Fatalf("diagnostics contain private data %q", secret)
			}
		}
		for _, line := range bytes.Split(bytes.TrimSpace(body), []byte("\n")) {
			var record map[string]any
			if err := json.Unmarshal(line, &record); err != nil {
				t.Fatal(err)
			}
			records = append(records, record)
		}
	}
	return records
}

func TestCouncilLogsStagesAndSeatsWithIncomingOrGeneratedRequestID(t *testing.T) {
	for _, supplied := range []string{uuid.NewString(), ""} {
		t.Run(supplied, func(t *testing.T) {
			host := &correlatedCouncilHost{ids: make(chan string, 16), gate: make(chan struct{})}
			fixture := newCouncilLogFixture(t, host)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var turn council.Turn
			requestID := fixture.request(t, ctx, "/api/v1/councils/"+fixture.value.ID+"/turns", `{"prompt":"private-prompt"}`, supplied, &turn)
			cancel()
			close(host.gate)
			fixture.sessions.councils.wg.Wait()
			if _, err := uuid.Parse(requestID); err != nil || (supplied != "" && requestID != supplied) || turn.RequestID != requestID {
				t.Fatalf("request correlation = %q, turn = %+v", requestID, turn)
			}
			if len(host.ids) != 5 {
				t.Fatalf("participant invocations = %d", len(host.ids))
			}
			for len(host.ids) > 0 {
				if id := <-host.ids; id != requestID {
					t.Fatalf("participant ID = %q", id)
				}
			}
			var saved council.Turn
			councilAPI(t, fixture.handler, http.MethodGet, "/api/v1/councils/"+fixture.value.ID+"/runs/"+turn.ID, nil, &saved)
			if saved.Status != "completed" || saved.RequestID != requestID {
				t.Fatalf("persisted turn = %+v", saved)
			}
			counts := make(map[string]int)
			for _, record := range fixture.records(t) {
				event, _ := record["msg"].(string)
				if !strings.HasPrefix(event, "council.") {
					continue
				}
				counts[event]++
				if strings.HasPrefix(event, "council.tool.") && (record["level"] != "INFO" || strings.HasSuffix(event, ".finished") && record["duration_ms"] == nil) {
					t.Fatalf("missing default council tool detail: %+v", record)
				}
				if record["request_id"] != requestID || record["council_id"] != fixture.value.ID || record["turn_id"] != turn.ID {
					t.Fatalf("lost identity: %+v", record)
				}
				if strings.HasPrefix(event, "council.seat.") || strings.HasPrefix(event, "council.tool.") {
					for _, field := range []string{"stage", "round", "seat", "role", "session_id", "operation_id"} {
						if record[field] == nil || record[field] == "" {
							t.Fatalf("missing %s: %+v", field, record)
						}
					}
				}
				if strings.HasSuffix(event, ".finished") && !strings.HasPrefix(event, "council.tool.") && record["status"] != "completed" {
					t.Fatalf("browser cancellation interrupted council: %+v", record)
				}
			}
			for event, want := range map[string]int{"council.turn.queued": 1, "council.turn.started": 1, "council.turn.finished": 1, "council.stage.started": 3, "council.stage.finished": 3, "council.seat.started": 5, "council.seat.finished": 5, "council.tool.started": 5, "council.tool.finished": 5} {
				if counts[event] != want {
					t.Fatalf("%s count = %d, want %d", event, counts[event], want)
				}
			}
		})
	}
}

func TestCouncilRetryLinksPreviousRequestID(t *testing.T) {
	host := &correlatedCouncilHost{ids: make(chan string, 32)}
	host.failNext.Store(true)
	fixture := newCouncilLogFixture(t, host)
	base := "/api/v1/councils/" + fixture.value.ID
	var original council.Turn
	firstID := fixture.request(t, t.Context(), base+"/turns", `{"prompt":"private-prompt"}`, "", &original)
	fixture.sessions.councils.wg.Wait()
	var failed council.Turn
	councilAPI(t, fixture.handler, http.MethodGet, base+"/runs/"+original.ID, nil, &failed)
	if failed.Status != "failed" || failed.RequestID != firstID {
		t.Fatalf("failed turn = %+v", failed)
	}
	for len(host.ids) > 0 {
		if id := <-host.ids; id != firstID {
			t.Fatalf("initial ID = %q", id)
		}
	}
	var resumed council.Turn
	secondID := fixture.request(t, t.Context(), base+"/runs/"+original.ID+"/retry", `{"mode":"resume"}`, "", &resumed)
	fixture.sessions.councils.wg.Wait()
	if resumed.ID != original.ID || resumed.RequestID != secondID || firstID == secondID || len(host.ids) != 4 {
		t.Fatalf("resume = %+v, calls = %d", resumed, len(host.ids))
	}
	for len(host.ids) > 0 {
		if id := <-host.ids; id != secondID {
			t.Fatalf("resume ID = %q", id)
		}
	}
	var rerun council.Turn
	thirdID := fixture.request(t, t.Context(), base+"/runs/"+original.ID+"/retry", `{"mode":"rerun"}`, "", &rerun)
	fixture.sessions.councils.wg.Wait()
	if rerun.ID == original.ID || rerun.RerunOf != original.ID || rerun.RequestID != thirdID || thirdID == secondID || len(host.ids) != 5 {
		t.Fatalf("rerun = %+v, calls = %d", rerun, len(host.ids))
	}
	for len(host.ids) > 0 {
		if id := <-host.ids; id != thirdID {
			t.Fatalf("rerun ID = %q", id)
		}
	}
	linked, failures := 0, 0
	for _, record := range fixture.records(t) {
		if record["msg"] == "council.turn.retry_queued" {
			previous := firstID
			if record["request_id"] == thirdID {
				previous = secondID
			}
			if record["previous_request_id"] != previous || record["source_turn_id"] != original.ID {
				t.Fatalf("missing retry link: %+v", record)
			}
			linked++
		}
		if record["msg"] == "council.turn.finished" && record["status"] == "failed" {
			if record["level"] != "ERROR" || record["request_id"] != firstID {
				t.Fatalf("failure log = %+v", record)
			}
			failures++
		}
	}
	if linked != 2 || failures != 1 {
		t.Fatalf("retry links = %d, failures = %d", linked, failures)
	}
}

func TestCouncilCancelLogsCauseAndOriginalExecution(t *testing.T) {
	host := &correlatedCouncilHost{ids: make(chan string, 16), gate: make(chan struct{})}
	fixture := newCouncilLogFixture(t, host)
	base := "/api/v1/councils/" + fixture.value.ID
	var turn council.Turn
	runID := fixture.request(t, t.Context(), base+"/turns", `{"prompt":"private-prompt"}`, "", &turn)
	select {
	case <-host.ids:
	case <-time.After(3 * time.Second):
		t.Fatal("participant did not start")
	}
	cancelID := fixture.request(t, t.Context(), base+"/runs/"+turn.ID+"/cancel", "", "", nil)
	fixture.sessions.councils.wg.Wait()
	var saved council.Turn
	councilAPI(t, fixture.handler, http.MethodGet, base+"/runs/"+turn.ID, nil, &saved)
	if saved.Status != "cancelled" || saved.RequestID != runID || runID == cancelID {
		t.Fatalf("cancelled turn = %+v", saved)
	}
	requested, finished := false, false
	for _, record := range fixture.records(t) {
		switch record["msg"] {
		case "council.turn.cancel_requested":
			requested = record["request_id"] == cancelID && record["run_request_id"] == runID
		case "council.turn.finished":
			finished = record["request_id"] == runID && record["status"] == "cancelled" && record["level"] == "INFO"
		}
	}
	if !requested || !finished {
		t.Fatalf("cancel request = %v, completion = %v", requested, finished)
	}
}
