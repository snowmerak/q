package studio

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/composer"
)

func TestComposerArtifactsQuestionAndPreviewThroughSharedRuntime(t *testing.T) {
	fixture := newStudioIntegration(t)
	c := createTestComposer(t, fixture.handler)
	base := "/api/v1/composers/" + c.ID
	response := serveJSON(t, fixture.handler, http.MethodPost, base+"/messages", map[string]string{"content": "composer artifacts"})
	if response.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", response.Code, response.Body)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		response = serveJSON(t, fixture.handler, http.MethodGet, base, nil)
		if err := json.Unmarshal(response.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		if c.Question != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c.Question == nil || len(c.Question.Choices) != 2 {
		t.Fatalf("question missing: %+v", c)
	}
	sourceURL := base + "/artifacts/source?path=" + url.QueryEscape("docs/diagram/manual.d2")
	if response := serveJSON(t, fixture.handler, http.MethodPut, sourceURL, map[string]string{"content": "x -> y", "expected_hash": ""}); response.Code != http.StatusConflict {
		t.Fatalf("editing during turn = %d", response.Code)
	}
	for _, callID := range []string{"stale", c.Question.CallID} {
		response = serveJSON(t, fixture.handler, http.MethodPost, base+"/answer", map[string]string{"call_id": callID, "answer": "flow"})
		expected := http.StatusAccepted
		if callID == "stale" {
			expected = http.StatusConflict
		}
		if response.Code != expected {
			t.Fatalf("answer %s = %d %s", callID, response.Code, response.Body)
		}
	}
	c = waitTestComposer(t, fixture.handler, c.ID)
	if c.Question != nil || c.Turns[0].Status != "completed" {
		t.Fatalf("completed = %+v", c)
	}
	if len(c.Turns[0].Interactions) != 1 || c.Turns[0].Interactions[0].Answer != "Reading flow" {
		t.Fatalf("question answer history lost: %+v", c.Turns[0].Interactions)
	}
	response = serveJSON(t, fixture.handler, http.MethodGet, base+"/artifacts", nil)
	var listing struct {
		Artifacts []composer.Artifact `json:"artifacts"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil || len(listing.Artifacts) != 2 {
		t.Fatalf("list = %s %v", response.Body, err)
	}
	for _, value := range listing.Artifacts {
		response = serveJSON(t, fixture.handler, http.MethodGet, base+"/artifacts/preview?path="+url.QueryEscape(value.Path), nil)
		if response.Code != http.StatusOK {
			t.Fatalf("preview = %d %s", response.Code, response.Body)
		}
		csp := response.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "sandbox allow-scripts") || !strings.Contains(csp, "connect-src 'none'") || strings.Contains(csp, "allow-same-origin") {
			t.Fatalf("unsafe CSP: %s", csp)
		}
		if value.Kind == "d2" && !strings.Contains(response.Body.String(), "<svg") {
			t.Fatal("D2 SVG missing")
		}
	}
	var created composer.Artifact
	if status := councilAPI(t, fixture.handler, http.MethodPut, sourceURL, map[string]string{"content": "one -> two", "expected_hash": ""}, &created); status != http.StatusOK {
		t.Fatalf("create = %d", status)
	}
	if response := serveJSON(t, fixture.handler, http.MethodPut, sourceURL, map[string]string{"content": "lost", "expected_hash": "stale"}); response.Code != http.StatusConflict {
		t.Fatalf("stale update = %d", response.Code)
	}
	// Invalid source remains editable and the preview displays a useful error.
	if status := councilAPI(t, fixture.handler, http.MethodPut, sourceURL, map[string]string{"content": "a: {", "expected_hash": created.Hash}, &created); status != http.StatusOK {
		t.Fatalf("invalid source save = %d", status)
	}
	response = serveJSON(t, fixture.handler, http.MethodGet, base+"/artifacts/preview?path="+url.QueryEscape(created.Path), nil)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "Preview unavailable") {
		t.Fatalf("error preview = %d %s", response.Code, response.Body)
	}
	if response := serveJSON(t, fixture.handler, http.MethodDelete, sourceURL, map[string]string{"expected_hash": created.Hash}); response.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", response.Code)
	}
	other := createTestComposer(t, fixture.handler)
	if response := serveJSON(t, fixture.handler, http.MethodGet, "/api/v1/composers/"+other.ID+"/artifacts/source?path="+url.QueryEscape("docs/diagram/reading.d2"), nil); response.Code != http.StatusNotFound {
		t.Fatalf("cross-session read = %d", response.Code)
	}
}
