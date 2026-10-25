package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/composer"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func createTestComposer(t *testing.T, handler http.Handler) composer.Conversation {
	t.Helper()
	var c composer.Conversation
	if status := councilAPI(t, handler, http.MethodPost, "/api/v1/composers", map[string]string{"name": "A new idea", "model": "default/test-model"}, &c); status != http.StatusCreated {
		t.Fatalf("create composer = %d: %+v", status, c)
	}
	return c
}

func waitTestComposer(t *testing.T, handler http.Handler, id string) composer.Conversation {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var c composer.Conversation
		response := serveJSON(t, handler, http.MethodGet, "/api/v1/composers/"+id, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("read composer = %d %s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		if len(c.Turns) > 0 && c.Turns[len(c.Turns)-1].Status != "running" {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("composer reply did not finish")
	return composer.Conversation{}
}

// Only the provider is substituted. The real Gateway, SessionHost and saved
// session context must deliver the selected model and both conversation turns.
func TestComposerRealRuntimeRetainsModelAndConversationAfterReopening(t *testing.T) {
	requests := make(chan client.ChatRequest, 8)
	fixture := newStudioIntegrationConfigured(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			studioTestModel(w, r)
			return
		}
		var input struct {
			client.ChatRequest
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		requests <- input.ChatRequest
		if input.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Who would use your idea first?\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "message": client.Message{Role: client.RoleAssistant, Content: "Who would use your idea first?"}, "finish_reason": "stop"}}})
	}, nil)
	c := createTestComposer(t, fixture.handler)
	for _, prompt := range []string{"I want to make a reading club app", "The first users will be local libraries"} {
		response := serveJSON(t, fixture.handler, http.MethodPost, "/api/v1/composers/"+c.ID+"/messages", map[string]string{"content": prompt})
		if response.Code != http.StatusAccepted {
			t.Fatalf("send = %d %s", response.Code, response.Body.String())
		}
		c = waitTestComposer(t, fixture.handler, c.ID)
		if last := c.Turns[len(c.Turns)-1]; last.Status != "completed" || last.Answer != "Who would use your idea first?" {
			t.Fatalf("reply = %+v", last)
		}
		select {
		case input := <-requests:
			if input.Model != "test-model" || len(input.Tools) != 0 {
				t.Fatalf("wrong model or tool catalog: model=%s tools=%d", input.Model, len(input.Tools))
			}
			body, _ := json.Marshal(input.Messages)
			for _, expected := range []string{"I want to make a reading club app", prompt, "You are Composer"} {
				if !strings.Contains(string(body), expected) {
					t.Fatalf("context missing %q", expected)
				}
			}
			if len(c.Turns) == 2 && !strings.Contains(string(body), "Who would use your idea first?") {
				t.Fatal("previous assistant reply was lost")
			}
		case <-time.After(time.Second):
			t.Fatal("provider request was not captured")
		}
	}
	if len(c.Turns) != 2 {
		t.Fatalf("history = %+v", c.Turns)
	}
	// A freshly opened HTTP service sees the same disk state, separately from
	// the original live session runtime and the registered workspace list.
	settings, err := config.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := newHandler(settings)
	if err != nil {
		t.Fatal(err)
	}
	var saved composer.Conversation
	if status := councilAPI(t, reopened, http.MethodGet, "/api/v1/composers/"+c.ID, nil, &saved); status != http.StatusOK || len(saved.Turns) != 2 || saved.SessionID != c.SessionID {
		t.Fatalf("reopened = %d %+v", status, saved)
	}
	if response := serveJSON(t, reopened, http.MethodDelete, "/api/v1/composers/"+c.ID, nil); response.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", response.Code, response.Body.String())
	}
	if response := serveJSON(t, reopened, http.MethodGet, "/api/v1/composers/"+c.ID, nil); response.Code != http.StatusNotFound {
		t.Fatalf("deleted composer remains: %d", response.Code)
	}
}

type blockedComposerHost struct {
	councilTestHost
	started chan struct{}
}

func (h blockedComposerHost) RunWithOptions(ctx context.Context, _ workspace.Store, _ string, _ string, options app.SessionOptions, emit app.SessionEventSink) error {
	runtime, closer, err := options.RuntimeFactory(ctx, workspace.Store{})
	if closer != nil {
		defer func() { _ = closer.Close() }()
	}
	if err != nil {
		return err
	}
	if len(runtime.Tools()) != 0 {
		panic("Composer acquired tools")
	}
	if err := emit(app.SessionEvent{Type: "stream", Kind: "response", Start: true, Content: "A partial thought"}); err != nil {
		return err
	}
	close(h.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestComposerLocksCancellationRecoveryAndIsolation(t *testing.T) {
	settings := config.Store{Dir: t.TempDir()}
	host := blockedComposerHost{started: make(chan struct{})}
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), settings, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close(); _ = commits.Close() })
	other, err := newHandler(settings)
	if err != nil {
		t.Fatal(err)
	}
	c := createTestComposer(t, handler)
	second := createTestComposer(t, handler)
	path := "/api/v1/composers/" + c.ID
	response := serveJSON(t, handler, http.MethodPost, path+"/messages", map[string]string{"content": "Explore this idea"})
	if response.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", response.Code, response.Body.String())
	}
	select {
	case <-host.started:
	case <-time.After(5 * time.Second):
		t.Fatal("reply did not start")
	}
	var active composer.Conversation
	if status := councilAPI(t, other, http.MethodGet, path, nil, &active); status != http.StatusOK || active.Turns[0].Status != "running" || active.Turns[0].Answer != "A partial thought" {
		t.Fatalf("active reply = %d %+v", status, active)
	}
	for _, handlerToCheck := range []http.Handler{handler, other} {
		if response := serveJSON(t, handlerToCheck, http.MethodDelete, path, nil); response.Code != http.StatusConflict {
			t.Fatalf("running deletion = %d", response.Code)
		}
	}
	if response := serveJSON(t, handler, http.MethodPost, path+"/messages", map[string]string{"content": "Duplicate"}); response.Code != http.StatusConflict {
		t.Fatalf("duplicate turn = %d", response.Code)
	}
	if response := serveJSON(t, handler, http.MethodPost, path+"/cancel", struct{}{}); response.Code != http.StatusAccepted {
		t.Fatalf("cancel = %d", response.Code)
	}
	c = waitTestComposer(t, handler, c.ID)
	if len(c.Turns) != 1 || c.Turns[0].Status != "cancelled" || c.Turns[0].Answer != "A partial thought" {
		t.Fatalf("cancelled = %+v", c)
	}
	// Simulate a process dying after accepting a turn and releasing its OS lock.
	c.Turns = append(c.Turns, composer.Turn{ID: uuid.NewString(), Prompt: "After a crash", Status: "running"})
	if err := sessions.composers.store.Save(c); err != nil {
		t.Fatal(err)
	}
	if status := councilAPI(t, other, http.MethodGet, path, nil, &c); status != http.StatusOK || c.Turns[1].Status != "interrupted" {
		t.Fatalf("recovery = %d %+v", status, c)
	}
	if response := serveJSON(t, other, http.MethodDelete, path, nil); response.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", response.Code)
	}
	if response := serveJSON(t, other, http.MethodGet, "/api/v1/composers/"+second.ID, nil); response.Code != http.StatusOK {
		t.Fatal("deleting one conversation changed another")
	}
	if response := serveJSON(t, other, http.MethodGet, "/api/v1/composers/not-an-id", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid ID = %d", response.Code)
	}
	if response := serveJSON(t, other, http.MethodPost, "/api/v1/composers", map[string]string{"name": "Idea", "model": ""}); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty model = %d", response.Code)
	}
	if response := serveJSON(t, other, http.MethodPost, "/api/v1/composers", map[string]string{"name": strings.Repeat("생각", 60), "model": "default/test-model"}); response.Code != http.StatusCreated {
		t.Fatalf("120-character Korean title = %d %s", response.Code, response.Body.String())
	}
	// Composer's private session never appears as a registered coding session.
	var registered registeredSessionsResponse
	if status := councilAPI(t, other, http.MethodGet, "/api/v1/registered-sessions", nil, &registered); status != http.StatusOK || len(registered.Sessions) != 0 {
		t.Fatalf("Composer leaked into workspace sessions: %+v", registered)
	}
}

func TestComposerRejectsBrowserRequestCancellationAsRunLifetime(t *testing.T) {
	settings := config.Store{Dir: t.TempDir()}
	host := blockedComposerHost{started: make(chan struct{})}
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), settings, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close(); _ = commits.Close() })
	c := createTestComposer(t, handler)
	ctx, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/composers/"+c.ID+"/messages", strings.NewReader(`{"content":"Keep exploring"}`)).WithContext(ctx)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	cancel()
	<-host.started
	var active composer.Conversation
	if status := councilAPI(t, handler, http.MethodGet, "/api/v1/composers/"+c.ID, nil, &active); status != http.StatusOK || active.Turns[0].Status != "running" {
		t.Fatalf("browser cancellation ended accepted work: %d %+v", status, active)
	}
	if err := sessions.Close(); err != nil {
		t.Fatal(err)
	}
	finished := waitTestComposer(t, handler, c.ID)
	if finished.Turns[0].Status != "cancelled" {
		t.Fatalf("shutdown left a running reply: %+v", finished)
	}
}
