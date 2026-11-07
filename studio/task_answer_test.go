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
	"testing"

	"github.com/snowmerak/q/client"
)

const fixtureTaskAnswer = "## Research results\n\nThe verified answer is **42**. Source: https://example.com/evidence"

// The browser fixture and HTTP integration exercise the same actual model loop.
func taskAnswerFixtureResponse(w http.ResponseWriter, body []byte) bool {
	var request struct {
		client.ChatRequest
		Stream bool `json:"stream"`
	}
	if json.Unmarshal(body, &request) != nil {
		return false
	}
	start, placement := -1, ""
	for i, message := range request.Messages {
		if message.Role == client.RoleUser && strings.HasPrefix(message.TextContent(), "task answer ") {
			start, placement = i, strings.Fields(message.TextContent())[2]
		}
	}
	if start < 0 {
		return false
	}
	started, answered := false, false
	for _, message := range request.Messages[start+1:] {
		for _, call := range message.ToolCalls {
			started = started || call.Function.Name == "task_start"
		}
		answered = answered || message.Role == client.RoleAssistant && message.TextContent() == fixtureTaskAnswer
	}
	last := request.Messages[len(request.Messages)-1]
	terminal := last.Role == client.RoleTool && strings.Contains(last.TextContent(), "The host has accepted this terminal tool result.")
	message := client.Message{Role: client.RoleAssistant}
	if terminal {
		message.Content = "Internal completion acknowledgment."
		if strings.Contains(last.TextContent(), "Provide the final answer") {
			message.Content = fixtureTaskAnswer
		}
	} else if !started {
		message.ToolCalls = []client.ToolCall{{ID: "answer-start", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "task_start", Arguments: `{"objective":"Research the answer"}`}}}
	} else if placement == "before" && !answered {
		message.Content = fixtureTaskAnswer
	} else {
		message.ToolCalls = []client.ToolCall{{ID: "answer-complete", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "task_complete", Arguments: `{"outcome":"succeeded","summary":"Research performed","verification":["Source checked"]}`}}}
		if placement == "alongside" {
			message.Content = fixtureTaskAnswer
		}
	}
	reason := "stop"
	if len(message.ToolCalls) > 0 {
		reason = "tool_calls"
	}
	usage := client.Usage{PromptTokens: 1024, CompletionTokens: 32, TotalTokens: 1056, PromptDetails: &client.TokenDetails{CachedTokens: 1000}}
	if request.Stream {
		calls := []map[string]any{}
		for i, call := range message.ToolCalls {
			calls = append(calls, map[string]any{"index": i, "id": call.ID, "type": call.Type, "function": call.Function})
		}
		chunk, _ := json.Marshal(map[string]any{"model": "test-model", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": message.Content, "tool_calls": calls}, "finish_reason": reason}}, "usage": usage})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	} else {
		writeJSON(w, 200, client.ChatResponse{Model: "test-model", Choices: []client.Choice{{Message: message, FinishReason: reason}}, Usage: usage})
	}
	return true
}

func TestTaskAnswerLiveEventsMatchPersistedHistory(t *testing.T) {
	for _, placement := range []string{"before", "alongside", "after"} {
		t.Run(placement, func(t *testing.T) {
			fixture := newStudioIntegrationConfigured(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				if r.URL.Path == "/v1/chat/completions" && taskAnswerFixtureResponse(w, body) {
					return
				}
				studioTestModel(w, r)
			}, nil)
			server := httptest.NewServer(fixture.handler)
			defer server.Close()
			run := fixture.start(t, "task answer "+placement)
			base := server.URL + "/api/v1/sessions/" + fixture.session.SessionID
			query := "?workspace_root=" + url.QueryEscape(fixture.root)
			page := waitStudioPage(t, base+"/runs/"+run.ID+"/events"+query, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
			if page.Run.Status != "completed" {
				t.Fatalf("run failed: %+v", page.Run)
			}
			count := 0
			for _, row := range page.Events {
				e := row.Event
				if (e.Type == "message" && e.Role == "assistant" || e.Type == "result") && e.Content == fixtureTaskAnswer {
					count++
				}
				if strings.Contains(e.Content, "Internal completion acknowledgment") || strings.Contains(e.Content, "still needs task_complete") {
					t.Fatalf("private event leaked: %+v", e)
				}
			}
			if count != 1 {
				t.Fatalf("live answer count=%d", count)
			}
			saved, err := fixture.session.Load()
			if err != nil {
				t.Fatal(err)
			}
			count = 0
			for _, message := range saved.Transcript {
				if message.Role == client.RoleAssistant && message.TextContent() == fixtureTaskAnswer {
					count++
				}
			}
			if count != 1 || saved.ActiveTask != nil {
				t.Fatalf("stored answer count=%d active=%+v", count, saved.ActiveTask)
			}
			response, err := http.Get(base + query + "&limit=80")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			var detail struct {
				Transcript []struct{ Role, Content string }
			}
			if err := json.NewDecoder(response.Body).Decode(&detail); err != nil {
				t.Fatal(err)
			}
			count = 0
			for _, message := range detail.Transcript {
				if message.Role == "assistant" && message.Content == fixtureTaskAnswer {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("reloaded answer count=%d", count)
			}
		})
	}
}
