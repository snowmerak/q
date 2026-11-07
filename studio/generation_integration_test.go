package studio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/snowmerak/q/config"
)

func TestStudioRecoversCutoffStreamAndRetainsUsageAndAttemptLog(t *testing.T) {
	var calls atomic.Int32
	fixture := newStudioIntegrationConfigured(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			studioTestModel(w, r)
			return
		}
		var request struct {
			Limit  int               `json:"max_completion_tokens"`
			Tools  []json.RawMessage `json:"tools"`
			Stream bool              `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		n := calls.Add(1)
		if request.Limit != 128 {
			t.Errorf("request %d limit=%d", n, request.Limit)
		}
		if n > 1 && len(request.Tools) > 0 {
			t.Error("text recovery enabled tools")
		}
		text, reason, output := "The answer is ", "length", 128
		if n > 1 {
			text, reason, output = "42.", "stop", 3
		}
		if !request.Stream {
			t.Error("expected streaming request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		body, _ := json.Marshal(map[string]any{"id": fmt.Sprint("generation-", n), "model": "test-model", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}, "finish_reason": reason}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": output, "total_tokens": 10 + output}})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", body)
	}, func(value *config.Config) { value.ModelOutputLimits = map[string]int{"default/test-model": 128} })
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	run := fixture.start(t, "output recovery")
	endpoint := server.URL + "/api/v1/sessions/" + fixture.session.SessionID + "/runs/" + run.ID + "/events?workspace_root=" + url.QueryEscape(fixture.root)
	page := waitStudioPage(t, endpoint, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
	if page.Run.Status != "completed" || calls.Load() != 2 {
		t.Fatalf("run=%+v calls=%d", page.Run, calls.Load())
	}
	response, err := http.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	var replay studioRunPage
	if err := json.Unmarshal(body, &replay); err != nil {
		t.Fatal(err)
	}
	foundResult, foundRecovery := false, false
	for _, record := range replay.Events {
		event := record.Event
		if event.Type == "result" {
			foundResult = event.Content == "The answer is 42."
			if event.Usage == nil || event.Usage.OutputTokens != 131 {
				t.Fatalf("usage=%+v", event.Usage)
			}
		}
		if event.Type == "trace" && event.Kind == "generation" {
			var attempt struct {
				Action string `json:"action"`
			}
			_ = json.Unmarshal([]byte(event.Content), &attempt)
			foundRecovery = foundRecovery || attempt.Action == "continue_text"
		}
	}
	if !foundResult || !foundRecovery {
		t.Fatalf("missing result or audit: %s", body)
	}
}

// Browser fixture only: a pure model that ends one answer at the token limit.
func generationBrowserModel(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			studioTestModel(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "read model request", 400)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if taskAnswerFixtureResponse(w, body) {
			return
		}
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &request) != nil {
			studioTestModel(w, r)
			return
		}
		match, lastUser := false, ""
		for _, message := range request.Messages {
			if message.Role == "user" {
				lastUser = message.Content
				if message.Content == "browser output recovery" {
					match = true
				}
			}
		}
		if !match {
			studioTestModel(w, r)
			return
		}
		text, reason, tokens := "The answer is ", "length", 128
		if strings.HasPrefix(lastUser, "Continue the unfinished answer") {
			text, reason, tokens = "42.", "stop", 3
		}
		chunk, _ := json.Marshal(map[string]any{"id": "browser-generation", "model": "test-model", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}, "finish_reason": reason}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": tokens, "total_tokens": 10 + tokens}})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}
}
