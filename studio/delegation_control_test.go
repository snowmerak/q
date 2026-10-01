package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

func TestStudioInnerDelegationControlAndFollowup(t *testing.T) {
	for _, action := range []string{"guidance", "cancel"} {
		t.Run(action, func(t *testing.T) { exerciseStudioDelegation(t, action) })
	}
}

func exerciseStudioDelegation(t *testing.T, firstAction string) {
	f := newStudioIntegration(t)
	server := httptest.NewServer(f.handler)
	defer server.Close()
	rootRun := f.start(t, "delegate controlled work")
	base := "/api/v1/sessions/" + f.session.SessionID
	var bookmark workspace.DelegationBookmark
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		items, err := f.session.LoadDelegations()
		if err == nil && len(items) > 0 {
			bookmark = items[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if bookmark.InvocationID == "" {
		t.Fatal("child never appeared")
	}
	path := bookmark.InvocationID
	endpoint := base + "/delegations/session?workspace_root=" + url.QueryEscape(f.root) + "&path=" + path
	poll := func(ready func(delegationSessionResponse) bool) delegationSessionResponse {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		var value delegationSessionResponse
		for time.Now().Before(deadline) {
			response := serveJSON(t, f.handler, http.MethodGet, endpoint, nil)
			if response.Code == 200 {
				if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
					t.Fatal(err)
				}
				if ready(value) {
					return value
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("child did not reach expected state: %#v", value)
		return value
	}
	command := func(id, action, content string, status int) delegationSessionResponse {
		t.Helper()
		response := serveJSON(t, f.handler, http.MethodPost, base+"/delegations/commands", delegationCommandRequest{WorkspaceRoot: f.root, Path: path, RunID: id, Action: action, Content: content})
		if response.Code != status {
			t.Fatalf("%s = %d: %s", action, response.Code, response.Body.String())
		}
		var value delegationSessionResponse
		if status < 300 {
			if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
		}
		return value
	}
	first := poll(func(v delegationSessionResponse) bool {
		return v.Run != nil && v.Run.Status == "running" && v.Node.State.Started
	})
	if first.Kind != "inner" {
		t.Fatalf("kind = %s", first.Kind)
	}
	command("stale-id", "cancel", "", http.StatusConflict)
	paused := command(first.Run.ID, "pause", "", http.StatusOK)
	if paused.Run.Status != "paused" {
		t.Fatalf("pause = %#v", paused.Run)
	}
	command(first.Run.ID, firstAction, "finish controlled child", http.StatusOK)
	poll(func(v delegationSessionResponse) bool { return terminalRunStatus(v.Run.Status) })
	page := waitStudioPage(t, server.URL+base+"/runs/"+rootRun.ID+"/events?workspace_root="+url.QueryEscape(f.root), func(p studioRunPage) bool { return terminalRunStatus(p.Run.Status) })
	if page.Run.Status != "completed" {
		t.Fatalf("parent = %#v", page.Run)
	}
	parentBefore, err := f.session.Load()
	if err != nil {
		t.Fatal(err)
	}

	second := command("", "message", "controlled child", http.StatusAccepted)
	if second.Run.ID == first.Run.ID {
		t.Fatal("followup reused execution identity")
	}
	poll(func(v delegationSessionResponse) bool {
		return v.Run.ID == second.Run.ID && v.Node.State.Started && v.Node.State.TurnStartRound > 0
	})
	command(first.Run.ID, "cancel", "", http.StatusConflict)
	command(second.Run.ID, "pause", "", http.StatusOK)
	command(second.Run.ID, "resume", "", http.StatusOK)
	command(second.Run.ID, "cancel", "", http.StatusOK)
	poll(func(v delegationSessionResponse) bool { return v.Run.Status == "cancelled" })
	// The execution releases the root session lock after its final checkpoint.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lock, err := workspace.AcquireSessionLock(f.root, f.session.SessionID, "test")
		if err == nil {
			_ = lock.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	third := command("", "message", "finish controlled child", http.StatusAccepted)
	final := poll(func(v delegationSessionResponse) bool { return v.Run.ID == third.Run.ID && v.Run.Status == "completed" })
	if final.Node.State.Round <= first.Node.State.Round {
		t.Fatal("followup did not advance the saved conversation")
	}
	child, err := f.session.ChildStore(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := child.Load()
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, message := range saved.Transcript {
		if message.Role == client.RoleUser {
			users++
		}
		for _, call := range message.ToolCalls {
			if call.Function.Name == "write_file" {
				t.Fatal("followup escaped the research role")
			}
		}
	}
	wantUsers := 4
	if firstAction == "cancel" {
		wantUsers = 3
	}
	if users != wantUsers {
		t.Fatalf("user history = %d, want %d", users, wantUsers)
	}
	parentAfter, err := f.session.Load()
	if err != nil || !reflect.DeepEqual(parentBefore, parentAfter) {
		t.Fatalf("followup rewrote parent: %v", err)
	}
}

func TestStudioExternalDelegationRejectsAllCommands(t *testing.T) {
	f := newStudioIntegration(t)
	bookmark := workspace.DelegationBookmark{InvocationID: "external-child", CallID: "external", Agent: "builtin/web-search", Prompt: "search", RunID: "run"}
	if _, err := f.session.AddDelegation(bookmark); err != nil {
		t.Fatal(err)
	}
	child, err := f.session.ChildStore(bookmark.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Save(workspace.Session{Transcript: []client.Message{{Role: client.RoleUser, Content: "search"}}}); err != nil {
		t.Fatal(err)
	}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: bookmark.Agent, Kind: "external", Prompt: "search", Status: "completed", Result: &client.ToolResult{Content: "done"}}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"message", "guidance", "pause", "resume", "cancel"} {
		response := serveJSON(t, f.handler, http.MethodPost, "/api/v1/sessions/"+f.session.SessionID+"/delegations/commands", delegationCommandRequest{WorkspaceRoot: f.root, Path: bookmark.InvocationID, Action: action, Content: "followup"})
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s = %d: %s", action, response.Code, response.Body.String())
		}
	}
}
