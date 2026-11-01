package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workspace"
)

func TestStudioDisablesEmbeddingsAfterOldProviderFails(t *testing.T) {
	var embeddingCalls atomic.Int64
	fixture := newStudioIntegrationConfigured(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/embeddings" {
			embeddingCalls.Add(1)
			http.Error(w, `model "removed/embedding" uses unknown provider prefix "removed"`, http.StatusBadRequest)
			return
		}
		studioTestModel(w, r)
	}, func(value *config.Config) {
		value.Embedding = config.EmbeddingConfig{Model: "default/embedding", Dimensions: 3}
	})
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	turn := func() studioRunPage {
		t.Helper()
		run := fixture.start(t, "controlled finish")
		endpoint := server.URL + "/api/v1/sessions/" + fixture.session.SessionID + "/runs/" + run.ID + "/events?workspace_root=" + url.QueryEscape(fixture.root)
		return waitStudioPage(t, endpoint, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
	}
	turn()
	if embeddingCalls.Load() == 0 {
		t.Fatal("old provider did not fail before disabling embeddings")
	}
	response := serveJSON(t, fixture.handler, http.MethodPut, "/api/v1/settings/models/embedding", map[string]any{
		"model": "", "embedding_dimensions": 0, "workspace_root": fixture.root,
	})
	if response.Code != http.StatusAccepted {
		t.Fatalf("disable = %d %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		response = serveJSON(t, fixture.handler, http.MethodGet, "/api/v1/settings", nil)
		var value settingsSnapshot
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if value.Models.EmbeddingChange == nil && value.Models.EmbeddingModel == "" && value.Models.EmbeddingDimensions == 0 {
			break
		}
		if time.Now().After(deadline) || value.Models.EmbeddingChange != nil && value.Models.EmbeddingChange.State == "failed" {
			t.Fatalf("disabled request blocked by old provider: %+v", value.Models)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Failed enrichment must still leave the original conversation searchable.
	archive, err := sessionstore.Open(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	result, searchErr := archive.Search(t.Context(), sessionstore.SearchOptions{Text: "controlled finish", Limit: 10})
	closeErr := archive.Close()
	if searchErr != nil || closeErr != nil || len(result.Hits) == 0 {
		t.Fatalf("original archive lost: %+v, %v, %v", result, searchErr, closeErr)
	}
	calls := embeddingCalls.Load()
	page := turn()
	if page.Run.Status != "completed" || strings.Contains(page.Run.Error, "embedding") {
		t.Fatalf("turn after disabling = %+v", page.Run)
	}
	if embeddingCalls.Load() != calls {
		t.Fatal("disabled session still called embedding provider")
	}
}

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
