package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

func TestStudioChangesSavedResponsesInPlace(t *testing.T) {
	var reject atomic.Bool
	fixture := newStudioIntegrationConfigured(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			studioTestModel(writer, request)
			return
		}
		if reject.Load() {
			http.Error(writer, "fixture replacement rejected", http.StatusUnprocessableEntity)
			return
		}
		var input client.ChatRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		history := input.Messages
		if len(input.Tools) != 0 || len(history) == 0 || history[len(history)-1].TextContent() != "existing tool output" ||
			slices.ContainsFunc(history, func(message client.Message) bool { return strings.Contains(message.TextContent(), "later turn") }) {
			t.Errorf("replacement did not use the original history without tools: %+v", input)
			http.Error(writer, "invalid original history", http.StatusBadRequest)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"id": "replacement", "model": "test-model", "choices": []any{map[string]any{
				"index": 0, "message": client.Message{Role: client.RoleAssistant, Content: "Replacement answer"}, "finish_reason": "stop",
			}}, "usage": client.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
		})
	}, nil)
	initial := time.Now().Add(-time.Hour).UTC()
	call := client.ToolCall{ID: "read-1", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "read_file", Arguments: "{}"}}
	initialHistory := []client.Message{
		{Role: client.RoleUser, Content: "Original prompt"},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}},
		client.ToolResultMessage(call, client.ToolResult{Content: "existing tool output"}),
		{Role: client.RoleAssistant, Content: "Original answer"},
		{Role: client.RoleUser, Content: "later turn"},
		{Role: client.RoleAssistant, Content: "Later answer"},
	}
	saved := workspace.Session{
		UpdatedAt: &initial, Transcript: initialHistory,
		Context:          []client.Message{{Role: client.RoleAssistant, Content: "stale provider context"}},
		ResponseAffinity: &workspace.ResponseAffinity{Key: "stale response id"},
		ResponseUsage: []workspace.ResponseUsage{
			{AssistantIndex: 1, TokenUsage: workspace.TokenUsage{InputTokens: 20}},
			{AssistantIndex: 2, TokenUsage: workspace.TokenUsage{InputTokens: 40}},
		},
		TranscriptThoughts: []workspace.TranscriptThought{{Before: 3, Content: "original thought"}, {Before: 5, Content: "later thought"}},
	}
	if err := fixture.session.Save(saved); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	reload := func() workspace.Session {
		t.Helper()
		value, err := fixture.session.Load()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}

	edited := runStudioResponseChange(t, fixture, server.URL, 3, "edit", "Manually edited", initial)
	value := reload()
	if edited.StatusDetail != "Response edited" || value.Transcript[3].TextContent() != "Manually edited" ||
		value.Transcript[5].TextContent() != "Later answer" || value.HistoryRevision == "" || value.Context != nil || value.ResponseAffinity != nil ||
		len(value.TranscriptThoughts) != 1 || value.TranscriptThoughts[0].Before != 5 {
		t.Fatalf("unexpected edit result: %+v, %+v", edited, value)
	}

	stale := runStudioResponseChange(t, fixture, server.URL, 3, "delete", "", initial)
	if stale.Status != "failed" || !strings.Contains(stale.Error, "history changed") || reload().Transcript[3].TextContent() != "Manually edited" {
		t.Fatalf("stale request affected history: %+v", stale)
	}
	reject.Store(true)
	failed := runStudioResponseChange(t, fixture, server.URL, 3, "regenerate", "", *value.UpdatedAt)
	if failed.Status != "failed" || reload().HistoryRevision != value.HistoryRevision {
		t.Fatalf("failed regeneration changed history: %+v", failed)
	}
	reject.Store(false)
	generated := runStudioResponseChange(t, fixture, server.URL, 3, "regenerate", "", *value.UpdatedAt)
	value = reload()
	if generated.StatusDetail != "Response regenerated" || value.Transcript[3].TextContent() != "Replacement answer" ||
		value.Transcript[5].TextContent() != "Later answer" || len(value.ResponseUsage) != 2 ||
		!slices.ContainsFunc(value.ResponseUsage, func(usage workspace.ResponseUsage) bool {
			return usage.AssistantIndex == 1 && usage.OutputTokens == 10
		}) {
		t.Fatalf("unexpected regeneration: %+v, %+v", generated, value)
	}
	projected := serveJSON(t, fixture.handler, http.MethodGet, "/api/v1/sessions/"+fixture.session.SessionID+"?workspace_root="+url.QueryEscape(fixture.root)+"&limit=2", nil)
	var detail sessionDetail
	if projected.Code != http.StatusOK || json.Unmarshal(projected.Body.Bytes(), &detail) != nil ||
		len(detail.Transcript) != 2 || detail.Transcript[1].Index != 5 || detail.HistoryRevision != value.HistoryRevision {
		t.Fatalf("history page IDs: %d %s", projected.Code, projected.Body)
	}

	deleted := runStudioResponseChange(t, fixture, server.URL, 3, "delete", "", *value.UpdatedAt)
	value = reload()
	if deleted.StatusDetail != "Response deleted" || len(value.Transcript) != 5 || value.Transcript[0].TextContent() != "Original prompt" ||
		value.Transcript[2].TextContent() != "existing tool output" || value.Transcript[4].TextContent() != "Later answer" ||
		len(value.ResponseUsage) != 1 || value.ResponseUsage[0].AssistantIndex != 1 || value.ResponseUsage[0].InputTokens != 40 ||
		len(value.TranscriptThoughts) != 1 || value.TranscriptThoughts[0].Before != 4 {
		t.Fatalf("unexpected delete result: %+v, %+v", deleted, value)
	}
}

func runStudioResponseChange(t *testing.T, fixture studioIntegration, serverURL string, index int, action, content string, updatedAt time.Time) studioRunSnapshot {
	t.Helper()
	base := "/api/v1/sessions/" + fixture.session.SessionID
	response := serveJSON(t, fixture.handler, http.MethodPost, base+"/responses", map[string]any{
		"workspace_root": fixture.root, "action": action, "index": index, "content": content, "updated_at": updatedAt,
	})
	var run studioRunSnapshot
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &run) != nil || run.Kind != responseChangeRunKind {
		t.Fatalf("response change = %d %s", response.Code, response.Body)
	}
	page := waitStudioPage(t, serverURL+base+"/runs/"+run.ID+"/events?workspace_root="+url.QueryEscape(fixture.root),
		func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
	return page.Run
}
