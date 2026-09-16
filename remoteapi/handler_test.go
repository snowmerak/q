package remoteapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/remoteconfig"
	"github.com/snowmerak/q/workspace"
)

type fakeHost struct {
	subagent string
	runErr   error
	errAfter bool
	runs     int
}

func (h *fakeHost) ListSubagents(string) ([]app.RemoteSubagentInfo, []app.RemoteSubagentIssue, error) {
	return []app.RemoteSubagentInfo{{Name: "builtin/scout", Available: true}}, nil, nil
}

func (h *fakeHost) Run(_ context.Context, store workspace.Store, _ string, subagent, _ string, emit app.RemoteEventSink) error {
	h.runs++
	h.subagent = subagent
	if h.runErr != nil {
		return h.runErr
	}
	if err := emit(app.RemoteEvent{Type: "session", WorkingDirectory: store.Root, SessionID: "session-1", Created: true}); err != nil {
		return err
	}
	if err := emit(app.RemoteEvent{Type: "result", SessionID: "session-1", Content: "done"}); err != nil {
		return err
	}
	if h.errAfter {
		return errors.New("cleanup failed")
	}
	return nil
}

func TestRunWithoutSubagentUsesDefaultMainLoop(t *testing.T) {
	host := &fakeHost{}
	handler := NewHandler(host, nil, 1)
	body := `{"working_directory":` + quoted(t.TempDir()) + `,"prompt":"do the work"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/subagent-runs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if host.subagent != "" || host.runs != 1 {
		t.Fatalf("host call = subagent %q, runs %d", host.subagent, host.runs)
	}
	if response.Header().Get("X-Q-Session-ID") != "session-1" {
		t.Fatalf("session header = %q", response.Header().Get("X-Q-Session-ID"))
	}
	scanner := bufio.NewScanner(strings.NewReader(response.Body.String()))
	var types []string
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		types = append(types, event.Type)
	}
	if len(types) != 2 || types[0] != "session" || types[1] != "result" {
		t.Fatalf("event types = %#v", types)
	}
}

func TestProtectedRoutesRequireRemoteKeyWhenEnabled(t *testing.T) {
	master := [32]byte{9, 8, 7}
	generated, err := remoteconfig.GenerateAPIKey(master, "test", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	value := remoteconfig.Default()
	value.APIKeys = []remoteconfig.APIKey{generated.Record}
	value.Authentication.Enabled = true
	authenticator, err := remoteconfig.NewAuthenticator(master, value)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(&fakeHost{}, authenticator, 1)
	target := "/v1/sessions?working_directory=" + url.QueryEscape(t.TempDir())

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, target, nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Authorization", "Bearer "+generated.Secret)
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, body = %s", authorized.Code, authorized.Body.String())
	}
}

func TestBusySessionFailsBeforeStream(t *testing.T) {
	handler := NewHandler(&fakeHost{runErr: workspace.ErrLocked}, nil, 1)
	body := `{"working_directory":` + quoted(t.TempDir()) + `,"session_id":"busy","prompt":"continue"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/subagent-runs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "session_busy") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestRunRejectsUnknownJSONField(t *testing.T) {
	host := &fakeHost{}
	handler := NewHandler(host, nil, 1)
	body := `{"working_directory":` + quoted(t.TempDir()) + `,"prompt":"work","model":"override"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/subagent-runs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || host.runs != 0 {
		t.Fatalf("response = %d %s, runs = %d", response.Code, response.Body.String(), host.runs)
	}
}

func TestOpenAPIDocumentIsServedAndAllowsDefaultMainLoop(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler(&fakeHost{}, nil, 1).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	runRequest := schemas["RunRequest"].(map[string]any)
	required := runRequest["required"].([]any)
	for _, name := range required {
		if name == "subagent" {
			t.Fatal("OpenAPI still requires subagent")
		}
	}
}

func TestCleanupFailureDoesNotAddASecondTerminalEvent(t *testing.T) {
	handler := NewHandler(&fakeHost{errAfter: true}, nil, 1)
	body := `{"working_directory":` + quoted(t.TempDir()) + `,"prompt":"work"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/subagent-runs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if strings.Count(response.Body.String(), `"type":"result"`) != 1 || strings.Contains(response.Body.String(), `"type":"error"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func quoted(value string) string {
	body, _ := json.Marshal(value)
	return string(body)
}
