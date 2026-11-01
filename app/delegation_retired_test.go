package app

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

func TestRetiredDelegationToolsCannotReachBaseRuntimeOrModels(t *testing.T) {
	for _, caller := range []string{"", subagent.BuiltinSeniorDeveloperID} {
		t.Run(caller, func(t *testing.T) {
			m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
			m.config = config.Default()
			m.config.Provider.Model = "test-model"
			c := &fakeClient{}
			m.client = c
			base := &fakeAgentTools{tools: []client.Tool{
				{Function: client.FunctionDefinition{Name: subagent.DelegateListToolName}},
				{Function: client.FunctionDefinition{Name: subagent.DelegateToolName}},
				{Function: client.FunctionDefinition{Name: "workspace_members"}},
				{Function: client.FunctionDefinition{Name: "ticket_create"}},
			}}
			runtime, err := m.configuredDelegationRuntimeFor(base, t.TempDir(), caller, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{subagent.DelegateListToolName, subagent.DelegateToolName} {
				if toolAvailable(runtime, name) {
					t.Fatalf("retired %s advertised", name)
				}
				out, err := runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{Name: name, Arguments: `{"subagent_name":"builtin/research","prompt":"run","working_directory":"missing-directory"}`}})
				if err != nil || !out.IsError || !strings.Contains(out.Content, "ticket_create") {
					t.Fatalf("retired call: %+v %v", out, err)
				}
			}
			if len(c.requests) != 0 || len(base.calls) != 0 {
				t.Fatal("retired call reached a model or base tool")
			}
			for _, name := range []string{"workspace_members", "ticket_create"} {
				if !toolAvailable(runtime, name) {
					t.Fatalf("ticket workflow tool %s missing", name)
				}
			}
		})
	}
}

func TestRetiredPendingDelegationDoesNotCreateAChild(t *testing.T) {
	parent, _ := recoveryParent(t, subagent.BuiltinResearchID, "inspect")
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &parent
	session, err := parent.Load()
	if err != nil {
		t.Fatal(err)
	}
	n, err := m.recoverPendingWorkspaceCalls(t.Context(), &session, nil)
	if err != nil || n != 1 {
		t.Fatalf("recovery: %d %v", n, err)
	}
	last := session.Transcript[len(session.Transcript)-1]
	if !strings.Contains(last.Content, "ticket_create") {
		t.Fatalf("missing ticket guidance: %+v", last)
	}
	bookmarks, err := parent.LoadDelegations()
	if err != nil || len(bookmarks) != 0 {
		t.Fatalf("recovery created a child: %+v %v", bookmarks, err)
	}
	n, err = m.recoverPendingWorkspaceCalls(t.Context(), &session, nil)
	if err != nil || n != 0 {
		t.Fatalf("retired result was appended twice: %d %v", n, err)
	}
}

func TestRetiredDelegationRecoveryPreservesSavedResultWithoutRuntime(t *testing.T) {
	parent, call := recoveryParent(t, "builtin/scout", "inspect")
	child := addRecoveryBookmark(t, parent, call, "builtin/scout", "inspect", "previous-worker")
	result := client.ToolResult{Content: `{"outcome":"succeeded","summary":"saved evidence"}`}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: "builtin/scout", Prompt: "inspect", RunID: "run-test", Status: "completed", Result: &result}); err != nil {
		t.Fatal(err)
	}
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &parent
	session, err := parent.Load()
	if err != nil {
		t.Fatal(err)
	}
	n, err := m.recoverPendingWorkspaceCalls(t.Context(), &session, nil)
	if err != nil || n != 1 || session.Transcript[len(session.Transcript)-1].Content != result.Content {
		t.Fatalf("saved result lost: %d %v %+v", n, err, session.Transcript)
	}
}

func TestLegacyDelegationFollowupRequiresATicket(t *testing.T) {
	host := &SessionHost{}
	_, err := host.StartDelegation(t.Context(), workspace.Store{Root: t.TempDir()}, "old-child", "revise")
	if err == nil || !strings.Contains(err.Error(), "ticket_create") {
		t.Fatalf("legacy followup accepted: %v", err)
	}
}

func TestUnfinishedLegacyWorkersAreNotResumed(t *testing.T) {
	for _, kind := range []string{"inner", "external"} {
		t.Run(kind, func(t *testing.T) {
			parent, call := recoveryParent(t, "workspace/old-worker", "inspect")
			child := addRecoveryBookmark(t, parent, call, "workspace/old-worker", "inspect", "interrupted-worker")
			before := workspace.DelegationState{Agent: "workspace/old-worker", Kind: kind, Prompt: "inspect", RunID: "run-test", Status: "running", Model: "missing-old-model"}
			if err := child.SaveDelegationState(before); err != nil {
				t.Fatal(err)
			}
			m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
			m.workspaceStore = &parent
			c := &fakeClient{}
			m.client = c
			session, err := parent.Load()
			if err != nil {
				t.Fatal(err)
			}
			n, err := m.recoverPendingWorkspaceCalls(t.Context(), &session, nil)
			if err != nil || n != 1 || len(c.requests) != 0 || !strings.Contains(session.Transcript[len(session.Transcript)-1].Content, "ticket_create") {
				t.Fatalf("unfinished worker resumed: %d %v requests=%d", n, err, len(c.requests))
			}
			after, err := child.LoadDelegationState()
			if err != nil || after.Result != nil || after.Status != before.Status || after.Model != before.Model {
				t.Fatalf("legacy checkpoint changed: %+v %v", after, err)
			}
		})
	}
}
