package studio

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

func TestStudioLegacyInnerHistoryIsReadableButCannotStartFollowups(t *testing.T) {
	f := newStudioIntegration(t)
	bookmark := workspace.DelegationBookmark{InvocationID: "legacy-child", CallID: "legacy-call", Agent: "builtin/research", Prompt: "previous work", RunID: "previous-run"}
	if _, err := f.session.AddDelegation(bookmark); err != nil {
		t.Fatal(err)
	}
	child, err := f.session.ChildStore(bookmark.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Save(workspace.Session{Transcript: []client.Message{{Role: client.RoleAssistant, Content: "Saved evidence"}}}); err != nil {
		t.Fatal(err)
	}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: bookmark.Agent, Kind: "inner", Prompt: bookmark.Prompt, RunID: bookmark.RunID, Status: "completed", Result: &client.ToolResult{Content: "Saved evidence"}}); err != nil {
		t.Fatal(err)
	}
	before, err := child.Load()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "/api/v1/sessions/" + f.session.SessionID + "/delegations/session?workspace_root=" + url.QueryEscape(f.root) + "&path=" + bookmark.InvocationID
	response := serveJSON(t, f.handler, http.MethodGet, endpoint, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "Saved evidence") {
		t.Fatalf("history: %d %s", response.Code, response.Body.String())
	}
	response = serveJSON(t, f.handler, http.MethodPost, "/api/v1/sessions/"+f.session.SessionID+"/delegations/commands", delegationCommandRequest{WorkspaceRoot: f.root, Path: bookmark.InvocationID, Action: "message", Content: "revise"})
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "ticket_create") {
		t.Fatalf("followup: %d %s", response.Code, response.Body.String())
	}
	after, err := child.Load()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("retired followup modified history: %v", err)
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
