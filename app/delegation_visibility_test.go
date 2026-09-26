package app

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
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

func TestNestedDelegationProgressCarriesParentTaskID(t *testing.T) {
	store, call := recoveryParent(t, "workspace/parent", "parent task")
	d, _, _ := recoveryDispatcher(t, []subagent.AgentDefinition{
		recoveryDefinition("workspace/parent", "workspace/child"), recoveryDefinition("workspace/child"),
	},
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"parent task"}`)}},
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.DelegateToolName, `{"subagent_name":"workspace/child","prompt":"child task"}`)}},
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"child task"}`)}},
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"child done"}`)}},
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"parent done"}`)}},
	)
	var progress []subagent.ProgressEvent
	var traces []subagent.TraceEvent
	d.progress = func(event subagent.ProgressEvent) { progress = append(progress, event) }
	d.trace = func(event subagent.TraceEvent) { traces = append(traces, event) }
	result, err := d.dispatch(t.Context(), "", nil, &store, "", call, delegateInput{SubagentName: "workspace/parent", Prompt: "parent task"})
	if err != nil || result.IsError {
		t.Fatalf("nested delegation = %#v, %v", result, err)
	}
	var parentID, childID string
	for _, event := range progress {
		if event.Action == subagent.ProgressStarted && event.Agent == "workspace/parent" {
			parentID = event.TaskID
		}
		if event.Action == subagent.ProgressStarted && event.Agent == "workspace/child" {
			childID = event.TaskID
			if event.ParentID != parentID {
				t.Fatalf("child parent ID = %q, want %q", event.ParentID, parentID)
			}
		}
	}
	if parentID == "" || childID == "" || childID == parentID {
		t.Fatalf("nested task IDs: progress=%#v", progress)
	}
	childTrace := false
	for _, trace := range traces {
		if trace.TaskID == childID && trace.ParentID == parentID && trace.Kind == subagent.TraceToolCall {
			childTrace = true
		}
	}
	if !childTrace {
		t.Fatalf("nested trace lineage missing: %#v", traces)
	}
}

func TestLiveTUIDelegationShowsChildBeforeParentResult(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "plan-model"
	configured := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.DelegateToolName, `{"subagent_name":"builtin/scout","prompt":"inspect"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"inspect"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"inspected"}`)}},
		{Role: client.RoleAssistant, Content: "Inspection complete."},
	}}
	store := workspace.Store{Root: t.TempDir()}
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &store
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configured)
	m.resize(100, 36)
	updated, command := m.startChatTurn("inspect", false)
	m = updated.(model)
	seenActive := false
	for i := 0; i < 50 && m.waiting; i++ {
		message := nextAgentMessage(t, command)
		if message.event.activity != nil && message.event.activity.Agent == subagent.BuiltinScoutID && message.event.activity.Action == subagent.ProgressStarted {
			parentResultSeen := false
			for _, saved := range m.messages {
				parentResultSeen = parentResultSeen || saved.Role == client.RoleTool && saved.Name == subagent.DelegateToolName
			}
			if parentResultSeen || !m.waiting {
				t.Fatalf("child started after parent result: messages=%#v", m.messages)
			}
			seenActive = true
		}
		updated, command = m.Update(message)
		m = updated.(model)
	}
	if m.waiting || !seenActive || len(m.agentTraces) == 0 || !strings.Contains(m.messages[len(m.messages)-1].Content, "Inspection complete") {
		t.Fatalf("live delegation missing: active=%v waiting=%v traces=%#v status=%q messages=%#v", seenActive, m.waiting, m.agentTraces, m.status, m.messages)
	}
}

func TestLiveTurnWithTwoDelegateCallsKeepsBothChildren(t *testing.T) {
	first := planToolCall(subagent.DelegateToolName, `{"subagent_name":"builtin/scout","prompt":"first task"}`)
	second := planToolCall(subagent.DelegateToolName, `{"subagent_name":"builtin/scout","prompt":"second task"}`)
	second.ID = first.ID
	value := config.Default()
	value.Provider.Model = "plan-model"
	configured := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{first, second}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"first task"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"first done"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"second task"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"second done"}`)}},
		{Role: client.RoleAssistant, Content: "Both complete."},
	}}
	store := workspace.Store{Root: t.TempDir()}
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &store
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configured)
	m.resize(100, 36)
	updated, command := m.startChatTurn("do both", false)
	m = updated.(model)
	for i := 0; i < 80 && m.waiting; i++ {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	bookmarks, err := m.workspaceStore.LoadDelegations()
	if err != nil || len(bookmarks) != 2 || bookmarks[0].InvocationID == bookmarks[1].InvocationID || len(m.agentStates) != 2 || m.waiting {
		t.Fatalf("two live delegates: bookmarks=%#v states=%#v waiting=%v status=%q err=%v", bookmarks, m.agentStates, m.waiting, m.status, err)
	}
	results := 0
	for _, message := range m.messages {
		if message.Role == client.RoleTool && message.Name == subagent.DelegateToolName && !strings.Contains(message.Content, "Tool error") {
			results++
		}
	}
	if results != 2 {
		t.Fatalf("saved parent results = %d, messages=%#v", results, m.messages)
	}
}

func TestACPDelegationProjectsChildToolLifecycle(t *testing.T) {
	configured := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.DelegateToolName, `{"subagent_name":"builtin/scout","prompt":"inspect"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"inspect"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"inspected"}`)}},
		{Role: client.RoleAssistant, Content: "Inspection complete."},
	}}
	agent, store, connection := testACPAgent(t, configured, &fakeAgentTools{})
	agent.state.config.Provider.Model = "plan-model"
	sessionID := openTestACPSession(t, agent, store.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("inspect")}})
	if err != nil || response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("ACP prompt = %#v, %v", response, err)
	}
	starts := make(map[acp.ToolCallId]bool)
	completed := make(map[acp.ToolCallId]bool)
	for _, notice := range connection.snapshot() {
		if call := notice.Update.ToolCall; call != nil && strings.HasPrefix(string(call.ToolCallId), "q-subagent-") {
			starts[call.ToolCallId] = true
		}
		if update := notice.Update.ToolCallUpdate; update != nil && strings.HasPrefix(string(update.ToolCallId), "q-subagent-") && update.Status != nil && *update.Status == acp.ToolCallStatusCompleted {
			completed[update.ToolCallId] = true
		}
	}
	if len(starts) < 2 || len(starts) != len(completed) {
		t.Fatalf("child ACP lifecycle: starts=%v completed=%v", starts, completed)
	}
}

func TestRecoveryStreamsChildProgressBeforeResumingParent(t *testing.T) {
	store, _ := recoveryParent(t, subagent.BuiltinScoutID, "inspect")
	value := config.Default()
	value.Provider.Model = "plan-model"
	configured := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"inspect"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"inspected"}`)}},
	}}
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &store
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configured)
	if !m.delegationRecoveryPending {
		t.Fatal("saved delegate was not scheduled for recovery")
	}
	command := m.startDelegationRecovery()
	seenLive := false
	finished := false
	for i := 0; i < 30 && !finished; i++ {
		message, ok := command().(delegationRecoveryMsg)
		if !ok {
			t.Fatal("recovery command returned wrong message type")
		}
		if message.activity != nil && message.activity.Action == subagent.ProgressStarted {
			if !m.waiting || len(m.agentActivities) != 0 {
				t.Fatal("recovery activity was not delivered before completion")
			}
			seenLive = true
		}
		finished = message.done
		updated, next := m.Update(message)
		m = updated.(model)
		command = next
	}
	if !seenLive || !finished || len(m.agentTraces) == 0 || m.delegationRecoveryPending {
		t.Fatalf("recovery visibility: started=%v done=%v traces=%d pending=%v status=%q", seenLive, finished, len(m.agentTraces), m.delegationRecoveryPending, m.status)
	}
}
