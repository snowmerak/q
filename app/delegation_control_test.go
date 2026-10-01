package app

import (
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

func TestReconcileChildFollowupAfterSessionWriteGap(t *testing.T) {
	start := planToolCall(subagent.TaskStartToolName, `{"objective":"work"}`)
	complete := planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"done"}`)
	transcript := []client.Message{
		{Role: client.RoleSystem, Content: "system"}, {Role: client.RoleUser, Content: "work"},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{start}},
		client.ToolResultMessage(start, client.ToolResult{Content: `{"started":true}`}),
		{Role: client.RoleSystem, Content: "complete the work"},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{complete}},
		client.ToolResultMessage(complete, client.ToolResult{Content: `{"outcome":"succeeded","summary":"done"}`}),
		{Role: client.RoleAssistant, Content: "ack"},
		{Role: client.RoleUser, Name: subagent.FollowupMessageName, Content: "continue"},
	}
	old := workspace.DelegationState{Round: 2, Started: true, Reminders: 1}
	updated, changed, err := reconcileChildExecutionState(workspace.Session{Transcript: transcript}, old, subagent.Spec{})
	if err != nil || !changed || updated.Started || updated.Reminders != 0 || updated.TurnStartRound != 2 || updated.Round != 2 {
		t.Fatalf("reconciled = %#v, changed=%v, err=%v", updated, changed, err)
	}
	if _, changed, err := reconcileChildExecutionState(workspace.Session{Transcript: transcript}, updated, subagent.Spec{}); err != nil || changed {
		t.Fatalf("reconciliation is not stable: changed=%v, err=%v", changed, err)
	}
}
