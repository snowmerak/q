package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/workspace"
)

func TestStudioRealRuntimeQuestionControlsRejectStaleAnswers(t *testing.T) {
	fixture := newStudioIntegration(t)
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	run := fixture.start(t, "wait for guidance")
	base := "/api/v1/sessions/" + fixture.session.SessionID + "/runs/" + run.ID
	query := "?workspace_root=" + url.QueryEscape(fixture.root)
	page := waitStudioPage(t, server.URL+base+"/events"+query, func(page studioRunPage) bool { return page.Run.Status == "waiting" })
	callID := page.Run.PendingQuestion.CallID
	command := func(action, answerID, answer string, status int) {
		t.Helper()
		response := serveJSON(t, fixture.handler, http.MethodPost, base+"/commands", studioRunCommandRequest{WorkspaceRoot: fixture.root, Action: action, CallID: answerID, Answer: answer})
		if response.Code != status {
			t.Fatalf("%s = %d %s", action, response.Code, response.Body.String())
		}
	}
	command("answer", "stale-question", "blue", http.StatusConflict)
	command("pause", "", "", http.StatusOK)
	waitStudioPage(t, server.URL+base+"/events"+query, func(page studioRunPage) bool { return page.Run.Status == "paused" })
	command("resume", "", "", http.StatusOK)
	command("answer", callID, "blue", http.StatusOK)
	page = waitStudioPage(t, server.URL+base+"/events"+query, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
	if page.Run.Status != "completed" || page.Run.PendingQuestion != nil {
		t.Fatalf("answered run = %#v", page.Run)
	}
	command("answer", callID, "green", http.StatusConflict)
	session, err := fixture.session.Load()
	if err != nil {
		t.Fatal(err)
	}
	var answerFound bool
	for _, message := range session.Transcript {
		if message.ToolCallID == callID && strings.Contains(message.Content, `"selected_choice_id":"blue"`) {
			answerFound = true
		}
	}
	if !answerFound {
		t.Fatalf("answer was not persisted: %#v", session.Transcript)
	}
}

func TestStudioRunRecoveryRepairsTailAndRebuildsStaleSnapshot(t *testing.T) {
	for _, tail := range []string{"", `{"cursor":4,"event":`, "valid without newline"} {
		t.Run(tail, func(t *testing.T) {
			root := t.TempDir()
			store, lock, err := workspace.CreateSession(root, "recovery")
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			run, err := createStudioRun(root, store, "interrupted-run")
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range []app.SessionEvent{{Type: "session", SessionID: store.SessionID}, {Type: "question", CallID: "old-question", Question: "Pending before restart"}} {
				if err := run.append(event); err != nil {
					t.Fatal(err)
				}
			}
			if err := run.close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.SessionDir(), studioRunsDirectory, run.snapshot.ID+".ndjson")
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate events reaching disk before the snapshot, then a crash
			// halfway through the next event (or after its closing JSON brace).
			record := studioRunEvent{Cursor: 3, At: time.Now().UTC(), Event: app.SessionEvent{Type: "stream", Kind: "response", Content: "durable prefix"}}
			body, _ := json.Marshal(record)
			body = append(body, '\n')
			if tail == "valid without newline" {
				record.Cursor = 4
				record.Event.Content = "complete final record"
				encoded, _ := json.Marshal(record)
				body = append(body, encoded...)
			} else {
				body = append(body, tail...)
			}
			if _, err := file.Write(body); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			for restart := range 2 {
				service := newSessionRunService(t.Context(), nil)
				recovered, loadErr := service.latest(root, store.SessionID)
				if loadErr != nil {
					_ = service.Close()
					t.Fatalf("restart %d: %v", restart, loadErr)
				}
				page := recovered.page(0, maximumRunEventPage)
				if page.Run.Status != "interrupted" || page.Run.PendingQuestion != nil {
					t.Fatalf("restart %d: %#v", restart, page.Run)
				}
				count := 0
				for i, record := range page.Events {
					if record.Cursor != int64(i+1) {
						t.Fatalf("non-contiguous events: %#v", page.Events)
					}
					if record.Event.Type == "recovered" {
						count++
					}
				}
				if count != 1 || page.Events[2].Event.Content != "durable prefix" {
					t.Fatalf("restart %d lost/duplicated events: %#v", restart, page.Events)
				}
				if err := service.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestStudioRunRecoveryRejectsCorruptionBeforeTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.ndjson")
	if err := os.WriteFile(path, []byte("{broken}\n{\"cursor\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readStudioRunEvents(path); err == nil {
		t.Fatal("corrupt complete record was silently discarded")
	}
	for _, suffix := range []string{"\n", ""} {
		if err := os.WriteFile(path, []byte("{\"cursor\":1}\n{\"cursor\":3}"+suffix), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readStudioRunEvents(path); err == nil || !strings.Contains(err.Error(), "contiguous") {
			t.Fatalf("cursor gap with suffix %q = %v", suffix, err)
		}
	}
}
