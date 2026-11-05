package studio

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func TestSessionHistoryPagesKeepTurnsToolsThoughtsAndUsageTogether(t *testing.T) {
	root := t.TempDir()
	store, lock, err := workspace.CreateSession(root, "history")
	if err != nil {
		t.Fatal(err)
	}
	value := workspace.Session{ID: store.SessionID}
	for turn := range 3 {
		a, b := fmt.Sprintf("a-%d", turn), fmt.Sprintf("b-%d", turn)
		value.Transcript = append(value.Transcript,
			client.Message{Role: client.RoleUser, Content: fmt.Sprintf("turn %d", turn)},
			client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: a}, {ID: b}}},
			client.Message{Role: client.RoleTool, ToolCallID: b, Content: "second result first"},
			client.Message{Role: client.RoleTool, ToolCallID: a, Content: "first result second"},
			client.Message{Role: client.RoleAssistant, Content: fmt.Sprintf("answer %d", turn)},
		)
		value.TranscriptThoughts = append(value.TranscriptThoughts, workspace.TranscriptThought{Before: turn*5 + 4, Content: fmt.Sprintf("thought %d", turn)})
		value.ResponseUsage = append(value.ResponseUsage, workspace.ResponseUsage{AssistantIndex: turn*2 + 1, TokenUsage: workspace.TokenUsage{InputTokens: turn + 100}})
	}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(config.Store{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "/api/v1/sessions/" + store.SessionID + "?workspace_root=" + url.QueryEscape(root)
	get := func(query string) (sessionDetail, int) {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, endpoint+query, nil))
		var detail sessionDetail
		if response.Code == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
				t.Fatal(err)
			}
		}
		return detail, response.Code
	}
	before := ""
	for turn := 2; turn >= 0; turn-- {
		page, status := get("&limit=3" + before)
		if status != http.StatusOK || page.History == nil || page.History.Start != turn*5 || page.History.End != (turn+1)*5 || page.History.Total != 15 {
			t.Fatalf("page %d: status=%d history=%+v", turn, status, page.History)
		}
		if len(page.Transcript) != 5 || len(page.Transcript[1].ToolCalls) != 2 || page.Transcript[2].ToolCallID != page.Transcript[1].ToolCalls[1].ID || page.Transcript[3].ToolCallID != page.Transcript[1].ToolCalls[0].ID {
			t.Fatalf("tool pair split: %+v", page.Transcript)
		}
		if len(page.TranscriptThoughts) != 1 || page.TranscriptThoughts[0].Before != 4 || page.TranscriptThoughts[0].Content != fmt.Sprintf("thought %d", turn) {
			t.Fatalf("thought anchors=%+v", page.TranscriptThoughts)
		}
		if usage := page.Transcript[4].Usage; usage == nil || usage.InputTokens != turn+100 {
			t.Fatalf("usage=%+v", usage)
		}
		before = fmt.Sprintf("&before=%d", page.History.Start)
	}
	if empty, status := get("&limit=3&before=0"); status != 200 || len(empty.Transcript) != 0 {
		t.Fatal("oldest cursor did not return an empty page")
	}
	if legacy, status := get(""); status != 200 || legacy.History != nil || len(legacy.Transcript) != 15 {
		t.Fatal("unpaged callers changed")
	}
	if page, status := get("&limit=3&before=7"); status != 200 || page.History.End != 5 {
		t.Fatal("arbitrary cursor split a tool exchange")
	}
	for _, query := range []string{"&limit=0", "&limit=201", "&limit=oops", "&before=-1", "&before=oops"} {
		if _, status := get(query); status != http.StatusBadRequest {
			t.Fatalf("accepted malformed page %q", query)
		}
	}
}

func TestSessionHistoryKeepsUnfinishedCallsAndInterleavedGuidance(t *testing.T) {
	value := workspace.Session{Transcript: []client.Message{
		{Role: client.RoleUser, Content: "first task"},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "pending"}}},
		{Role: client.RoleUser, Content: "guidance while the tool runs"},
		{Role: client.RoleTool, ToolCallID: "pending", Content: "result"},
		{Role: client.RoleAssistant, Content: "done"},
		{Role: client.RoleUser, Content: "second task"},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "still-running"}}},
	}}
	page, err := requestedSessionHistory(httptest.NewRequest("GET", "/?limit=1", nil), "", workspace.Store{}, value)
	if err != nil || page.History.Start != 5 || len(page.Transcript) != 2 {
		t.Fatalf("unfinished tool call lost its user turn: %+v %v", page.History, err)
	}
	page, err = requestedSessionHistory(httptest.NewRequest("GET", "/?limit=1&before=5", nil), "", workspace.Store{}, value)
	if err != nil || page.History.Start != 0 || len(page.Transcript) != 5 {
		t.Fatalf("guidance split a pending call from its result: %+v %v", page.History, err)
	}
}
