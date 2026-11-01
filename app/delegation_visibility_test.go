package app

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

func TestSiblingDelegationsKeepSeparateProgressAndBookmarks(t *testing.T) {
	first := planToolCall(subagent.DelegateToolName, `{"subagent_name":"workspace/worker","prompt":"first task"}`)
	second := planToolCall(subagent.DelegateToolName, `{"subagent_name":"workspace/worker","prompt":"second task"}`)
	// A provider may reuse its call ID; the parent ordinal still identifies each child.
	second.ID = first.ID
	store := workspace.Store{Root: t.TempDir()}
	parent := workspace.Session{RunID: "run-test", Transcript: []client.Message{{Role: client.RoleUser, Content: "both"}, {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{first, second}}}}
	parent.Context = append([]client.Message(nil), parent.Transcript...)
	if err := store.Save(parent); err != nil {
		t.Fatal(err)
	}
	d, _, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/worker")},
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"first task"}`)}},
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"first done"}`)}},
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"second task"}`)}},
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"second done"}`)}},
	)
	var progress []subagent.ProgressEvent
	var traces []subagent.TraceEvent
	d.progress = func(event subagent.ProgressEvent) { progress = append(progress, event) }
	d.trace = func(event subagent.TraceEvent) { traces = append(traces, event) }
	for _, call := range []client.ToolCall{first, second} {
		var input delegateInput
		if err := decodeDelegationArguments(call.Function.Arguments, &input); err != nil {
			t.Fatal(err)
		}
		result, err := d.dispatch(t.Context(), "", nil, &store, "", call, input)
		if err != nil || result.IsError {
			t.Fatalf("delegate %s: %#v, %v", input.Prompt, result, err)
		}
		parent.Transcript = append(parent.Transcript, client.ToolResultMessage(call, result))
		parent.Context = append(parent.Context, client.ToolResultMessage(call, result))
		if err := store.Save(parent); err != nil {
			t.Fatal(err)
		}
	}
	bookmarks, err := store.LoadDelegations()
	if err != nil || len(bookmarks) != 2 || bookmarks[0].InvocationID == bookmarks[1].InvocationID || bookmarks[0].ToolIndex == bookmarks[1].ToolIndex {
		t.Fatalf("sibling bookmarks = %#v, %v", bookmarks, err)
	}
	states := make(map[string]map[string]bool)
	for _, event := range progress {
		if states[event.TaskID] == nil {
			states[event.TaskID] = make(map[string]bool)
		}
		states[event.TaskID][event.Action] = true
	}
	if len(states) != 2 {
		t.Fatalf("progress task identities = %#v", progress)
	}
	for _, bookmark := range bookmarks {
		if !states[bookmark.InvocationID][subagent.ProgressStarted] || !states[bookmark.InvocationID][subagent.ProgressCompleted] {
			t.Fatalf("missing lifecycle for %s: %#v", bookmark.InvocationID, progress)
		}
	}
	for _, trace := range traces {
		if trace.TaskID != bookmarks[0].InvocationID && trace.TaskID != bookmarks[1].InvocationID {
			t.Fatalf("trace has wrong child identity: %#v", trace)
		}
	}
	m := model{}
	for _, event := range progress {
		m.appendAgentActivity(agentActivity{Agent: event.Agent, TaskID: event.TaskID, Action: event.Action})
	}
	if len(m.agentStates) != 2 || !strings.Contains(agentSummary(m.agentStates), shortAgentTaskID(bookmarks[0].InvocationID)) || !strings.Contains(agentSummary(m.agentStates), shortAgentTaskID(bookmarks[1].InvocationID)) {
		t.Fatalf("sibling summary = %q", agentSummary(m.agentStates))
	}
}
