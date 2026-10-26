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
// session context must execute an artifact tool and retain its result across
// subsequent turns, rather than taking a plain-completion bypass.
func TestComposerRealRuntimeUsesSharedLoopAndRetainsConversation(t *testing.T) {
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
		message := client.Message{Role: client.RoleAssistant, Content: "Who would use your idea first?"}
		finish := "stop"
		if input.Messages[len(input.Messages)-1].Role == client.RoleUser {
			message.Content = ""
			message.ToolCalls = []client.ToolCall{{ID: "fact-" + uuid.NewString(), Type: client.ToolTypeFunction,
				Function: client.FunctionCall{Name: "composer_write_artifact", Arguments: `{"path":"docs/diagram/club.d2","content":"reader -> club"}`},
			}}
			finish = "tool_calls"
		}
		if input.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			delta := map[string]any{"role": "assistant", "content": message.Content}
			if len(message.ToolCalls) > 0 {
				call := message.ToolCalls[0]
				delta["tool_calls"] = []any{map[string]any{"index": 0, "id": call.ID, "type": call.Type, "function": call.Function}}
			}
			body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
	}, nil)
	var c composer.Conversation
	if status := councilAPI(t, fixture.handler, http.MethodPost, "/api/v1/composers", map[string]string{"name": "A new idea", "model": "default/test-model", "reasoning_effort": "high"}, &c); status != http.StatusCreated {
		t.Fatalf("create composer = %d: %+v", status, c)
	}
	for _, prompt := range []string{"I want to make a reading club app", "The first users will be local libraries"} {
		response := serveJSON(t, fixture.handler, http.MethodPost, "/api/v1/composers/"+c.ID+"/messages", map[string]string{"content": prompt})
		if response.Code != http.StatusAccepted {
			t.Fatalf("send = %d %s", response.Code, response.Body.String())
		}
		c = waitTestComposer(t, fixture.handler, c.ID)
		if last := c.Turns[len(c.Turns)-1]; last.Status != "completed" || last.Answer != "Who would use your idea first?" {
			t.Fatalf("reply = %+v", last)
		}
		for round := range 2 {
			select {
			case input := <-requests:
				if input.Model != "test-model" {
					t.Fatalf("wrong model: %s", input.Model)
				}
				if input.ReasoningEffort != "high" {
					t.Fatalf("wrong reasoning effort: %q", input.ReasoningEffort)
				}
				allowed := map[string]bool{
					"ask_to_user": true, "search_skills": true, "get_skill": true, "search_archive": true, "get_archive_record": true, "learn": true,
					"search_propositions": true, "get_proposition": true, "composer_list_artifacts": true, "composer_read_artifact": true, "composer_write_artifact": true, "composer_delete_artifact": true,
				}
				if len(input.Tools) != len(allowed) {
					t.Fatalf("wrong loop tool catalog: %+v", input.Tools)
				}
				for _, tool := range input.Tools {
					if !allowed[tool.Function.Name] {
						t.Fatalf("Composer acquired workspace tool %q", tool.Function.Name)
					}
				}
				body, _ := json.Marshal(input.Messages)
				for _, expected := range []string{"I want to make a reading club app", prompt, "You are Composer"} {
					if !strings.Contains(string(body), expected) {
						t.Fatalf("context missing %q", expected)
					}
				}
				if round == 1 {
					last := input.Messages[len(input.Messages)-1]
					matchedCall := false
					for _, message := range input.Messages {
						for _, call := range message.ToolCalls {
							if call.ID == last.ToolCallID && call.Function.Name == "composer_write_artifact" {
								matchedCall = true
							}
						}
					}
					if last.Role != client.RoleTool || !matchedCall || !strings.Contains(last.Content, `"path":"docs/diagram/club.d2"`) {
						t.Fatalf("shared loop did not execute the artifact tool: %+v", last)
					}
				}
				if len(c.Turns) == 2 && !strings.Contains(string(body), "Who would use your idea first?") {
					t.Fatal("previous assistant reply was lost")
				}
			case <-time.After(time.Second):
				t.Fatal("provider request was not captured")
			}
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
	native, err := composer.NewStore(settings.Dir).SessionStore(c).Load()
	if err != nil {
		t.Fatal(err)
	}
	if native.Type != workspace.Composer || native.ID != c.ID || c.ID != c.SessionID {
		t.Fatalf("Composer lost canonical type/identity: %+v", native)
	}
	facts := 0
	for _, message := range native.Transcript {
		if message.Role == client.RoleTool && message.Name == "composer_write_artifact" {
			facts++
		}
	}
	if facts != 2 {
		t.Fatalf("loop tool results were not persisted: got %d", facts)
	}
	reopened, err := newHandler(settings)
	if err != nil {
		t.Fatal(err)
	}
	var saved composer.Conversation
	if status := councilAPI(t, reopened, http.MethodGet, "/api/v1/composers/"+c.ID, nil, &saved); status != http.StatusOK || len(saved.Turns) != 2 || saved.SessionID != c.SessionID || saved.ReasoningEffort != "high" {
		t.Fatalf("reopened = %d %+v", status, saved)
	}
	if response := serveJSON(t, fixture.handler, http.MethodDelete, "/api/v1/composers/"+c.ID, nil); response.Code != http.StatusNoContent {
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
	if options.WrapRuntime == nil || options.RuntimeFactory != nil || len(options.AllowedTools) == 0 {
		panic("Composer did not restrict the standard runtime")
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
	// Unified discovery includes Composer roots with their workflow type; it
	// must not expose their contents as ordinary interaction/delegation roots.
	var registered registeredSessionsResponse
	if status := councilAPI(t, other, http.MethodGet, "/api/v1/registered-sessions", nil, &registered); status != http.StatusOK || len(registered.Sessions) != 2 {
		t.Fatalf("Composer catalog: %+v", registered)
	}
	for _, entry := range registered.Sessions {
		if entry.Session.Type != workspace.Composer || len(entry.Delegations) != 0 {
			t.Fatalf("Composer was exposed as an ordinary coding session: %+v", entry)
		}
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
