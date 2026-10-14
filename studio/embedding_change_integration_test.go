package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/snowmerak/q/workspace"
)

func TestStudioEmbeddingChangeAppliesAfterAllSessionTurnsFinish(t *testing.T) {
	first := newStudioIntegration(t)
	server := httptest.NewServer(first.handler)
	defer server.Close()
	second := first
	var err error
	var lock *workspace.Lock
	second.session, lock, err = workspace.CreateSession(first.root, "Second embedding session")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := func() settingsSnapshot {
		t.Helper()
		response := serveJSON(t, first.handler, http.MethodGet, "/api/v1/settings", nil)
		var value settingsSnapshot
		if response.Code != http.StatusOK {
			t.Fatalf("settings = %d %s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	queue := func(model string) {
		t.Helper()
		response := serveJSON(t, first.handler, http.MethodPut, "/api/v1/settings/models/embedding", map[string]any{
			"model": model, "embedding_dimensions": 3, "workspace_root": first.root,
		})
		if response.Code != http.StatusAccepted {
			t.Fatalf("queue model = %d %s", response.Code, response.Body.String())
		}
	}
	waitApplied := func(model string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			value := snapshot()
			if value.Models.EmbeddingChange == nil && value.Models.EmbeddingModel == model {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("model not applied: %+v", snapshot().Models)
	}
	poll := func(fixture studioIntegration, run studioRunSnapshot, ready func(studioRunPage) bool) studioRunPage {
		t.Helper()
		endpoint := server.URL + "/api/v1/sessions/" + fixture.session.SessionID + "/runs/" + run.ID + "/events?workspace_root=" + url.QueryEscape(fixture.root)
		return waitStudioPage(t, endpoint, ready)
	}
	queue("default/embed-old")
	waitApplied("default/embed-old")
	fixtures := []studioIntegration{first, second}
	var runs []studioRunSnapshot
	for _, fixture := range fixtures {
		run := fixture.start(t, "wait for guidance")
		poll(fixture, run, func(page studioRunPage) bool { return page.Run.PendingQuestion != nil })
		runs = append(runs, run)
	}
	queue("default/embed-new")
	for index, fixture := range fixtures {
		value := snapshot()
		if value.Models.EmbeddingModel != "default/embed-old" || value.Models.EmbeddingChange == nil || value.Models.EmbeddingChange.Model != "default/embed-new" {
			t.Fatalf("running session observed new configuration: %+v", value.Models)
		}
		response := serveJSON(t, first.handler, http.MethodPost, "/api/v1/sessions/"+fixture.session.SessionID+"/runs/"+runs[index].ID+"/commands", studioRunCommandRequest{WorkspaceRoot: first.root, Action: "cancel"})
		if response.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", response.Code, response.Body.String())
		}
		poll(fixture, runs[index], func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
	}
	waitApplied("default/embed-new")
	for _, fixture := range fixtures {
		run := fixture.start(t, "controlled finish")
		page := poll(fixture, run, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
		if page.Run.Status != "completed" {
			t.Fatalf("post-transition turn = %+v", page.Run)
		}
	}
}
