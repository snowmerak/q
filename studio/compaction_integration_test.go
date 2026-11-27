package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
)

func TestStudioBackgroundCompactionSurvivesDisconnect(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	fixture := newStudioIntegrationConfigured(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			studioTestModel(w, r)
			return
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
			return
		case <-release:
		}
		body, _ := json.Marshal(memory.Checkpoint{ActiveWork: []string{"continue the latest request"}})
		response := map[string]any{"id": "checkpoint", "model": "test-model", "choices": []any{
			map[string]any{"index": 0, "message": client.Message{Role: client.RoleAssistant, Content: string(body)}, "finish_reason": "stop"},
		}}
		writeJSON(w, http.StatusOK, response)
	}, func(settings *config.Config) {
		settings.Context.Window = 16000
		settings.Context.RecentRatio = 0.02
	})
	t.Cleanup(unblock)
	history := []client.Message{
		{Role: client.RoleUser, Content: strings.Repeat("old context ", 180)},
		{Role: client.RoleAssistant, Content: strings.Repeat("old answer ", 120)},
		{Role: client.RoleUser, Content: "latest request"},
	}
	session, err := fixture.session.Load()
	if err != nil {
		t.Fatal(err)
	}
	session.Transcript, session.Context = history, history
	if err := fixture.session.Save(session); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(fixture.handler)
	t.Cleanup(server.Close)
	base := "/api/v1/sessions/" + fixture.session.SessionID
	body := sessionCompactRequest{WorkspaceRoot: fixture.root, Background: true}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	browser, disconnect := context.WithTimeout(t.Context(), 5*time.Second)
	defer disconnect()
	request, err := http.NewRequestWithContext(browser, http.MethodPost, server.URL+base+"/compact", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	sent := time.Now()
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("compaction did not acknowledge before the model responded: %v", err)
	}
	payload, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	disconnect()
	var run studioRunSnapshot
	if err != nil || response.StatusCode != http.StatusAccepted || json.Unmarshal(payload, &run) != nil || run.Kind != compactionRunKind || run.ID == "" {
		t.Fatalf("compaction acknowledgement: %d %s %v", response.StatusCode, payload, err)
	}
	t.Logf("Compaction acknowledged in %v", time.Since(sent).Round(time.Millisecond))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		latest := serveJSON(t, fixture.handler, http.MethodGet, base+"/runs/latest?workspace_root="+url.QueryEscape(fixture.root), nil)
		t.Fatalf("background compaction did not reach the model: %s", latest.Body)
	}

	duplicate := serveJSON(t, fixture.handler, http.MethodPost, base+"/compact", body)
	var same studioRunSnapshot
	if duplicate.Code != http.StatusAccepted || json.Unmarshal(duplicate.Body.Bytes(), &same) != nil || same.ID != run.ID {
		t.Fatalf("duplicate compaction did not return the active run: %d %s", duplicate.Code, duplicate.Body)
	}
	message := serveJSON(t, fixture.handler, http.MethodPost, base+"/messages", sessionRunRequest{WorkspaceRoot: fixture.root, Content: "opinion"})
	if message.Code != http.StatusConflict {
		t.Fatalf("chat entered an active compaction: %d %s", message.Code, message.Body)
	}

	query := "?workspace_root=" + url.QueryEscape(fixture.root)
	unblock()
	page := waitStudioPage(t, server.URL+base+"/runs/"+run.ID+"/events"+query, func(page studioRunPage) bool {
		return terminalRunStatus(page.Run.Status)
	})
	if page.Run.Status != "completed" || !strings.Contains(page.Run.StatusDetail, "Context compacted") {
		t.Fatalf("compaction failed after disconnect: %+v", page.Run)
	}
	saved, err := fixture.session.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.Transcript, history) || !slices.ContainsFunc(saved.Context, func(message client.Message) bool { return message.Name == memory.SummaryName }) {
		t.Fatal("compaction did not save the checkpoint and preserve the conversation")
	}
	t.Logf("Checkpoint saved in %v", time.Since(sent).Round(time.Millisecond))
}
