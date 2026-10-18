package studio

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

func TestStudioSessionModelChangesActualRequestsAndRetainsHistory(t *testing.T) {
	var mu sync.Mutex
	var requests []client.ChatRequest
	fixture := newStudioIntegrationConfigured(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]string{{"id": "test-model"}, {"id": "alternate-model"}}})
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			requests = append(requests, client.ChatRequest{})
			err = json.Unmarshal(body, &requests[len(requests)-1])
			mu.Unlock()
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		studioTestModel(w, r)
	}, nil)
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	// Use the same named provider route exposed by the Studio catalog.
	response := serveJSON(t, fixture.handler, http.MethodPost, "/api/v1/settings/gateway/providers", map[string]any{
		"id": "default", "type": "openai-compatible", "enabled": true, "base_url": fixture.modelURI,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("provider = %d %s", response.Code, response.Body.String())
	}
	response = serveJSON(t, fixture.handler, http.MethodPut, "/api/v1/settings/models/default", map[string]string{"model": "default/test-model"})
	if response.Code != http.StatusOK {
		t.Fatalf("default = %d %s", response.Code, response.Body.String())
	}
	base := "/api/v1/sessions/" + fixture.session.SessionID
	query := "?workspace_root=" + url.QueryEscape(fixture.root)
	setModel := func(model string) {
		t.Helper()
		response := serveJSON(t, fixture.handler, http.MethodPut, base+"/model", sessionModelUpdate{WorkspaceRoot: fixture.root, Model: model})
		if response.Code != http.StatusOK {
			t.Fatalf("set model = %d %s", response.Code, response.Body.String())
		}
	}
	run := func(prompt, expectedModel string, history bool) {
		t.Helper()
		mu.Lock()
		requests = nil
		mu.Unlock()
		response := serveJSON(t, fixture.handler, http.MethodPost, base+"/messages", sessionRunRequest{WorkspaceRoot: fixture.root, Content: prompt})
		if response.Code != http.StatusAccepted {
			t.Fatalf("run = %d %s", response.Code, response.Body.String())
		}
		var started studioRunSnapshot
		if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
			t.Fatal(err)
		}
		page := waitStudioPage(t, server.URL+base+"/runs/"+started.ID+"/events"+query, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
		if page.Run.Status != "completed" {
			t.Fatalf("turn = %+v", page.Run)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(requests) == 0 {
			t.Fatal("no provider requests")
		}
		for _, request := range requests {
			if request.Model != expectedModel {
				t.Fatalf("model = %q; want %q", request.Model, expectedModel)
			}
		}
		if history {
			found := false
			for _, message := range requests[0].Messages {
				found = found || message.Content == "first controlled finish"
			}
			if !found {
				t.Fatal("changing the model lost conversation history")
			}
		}
	}
	run("first controlled finish", "test-model", false)
	setModel("default/alternate-model")
	run("second controlled finish", "alternate-model", true)
	// The choice is session-local; workspace and global defaults still apply elsewhere.
	local, err := fixture.session.LoadModelConfig()
	if err != nil || len(local.Overrides) != 0 {
		t.Fatalf("workspace changed: %+v, %v", local, err)
	}
	other, lock, err := workspace.CreateSession(fixture.root, "other")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	response = serveJSON(t, fixture.handler, http.MethodGet, "/api/v1/sessions/"+other.SessionID+"/model"+query, nil)
	var inherited sessionModelSettings
	if err := json.Unmarshal(response.Body.Bytes(), &inherited); err != nil {
		t.Fatal(err)
	}
	if inherited.Model != "" || inherited.EffectiveModel != "default/test-model" {
		t.Fatalf("other session model = %+v", inherited)
	}
	setModel("")
	run("third controlled finish", "test-model", true)
	// Inheritance also includes the existing workspace default.
	if err := fixture.session.SaveModelConfig(workspace.ModelConfig{Overrides: map[string]workspace.ModelOverride{"default": {Model: "default/alternate-model"}}}); err != nil {
		t.Fatal(err)
	}
	run("fourth controlled finish", "alternate-model", true)
	response = serveJSON(t, fixture.handler, http.MethodPut, base+"/model", sessionModelUpdate{WorkspaceRoot: fixture.root, Model: " invalid "})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid model = %d", response.Code)
	}
}
