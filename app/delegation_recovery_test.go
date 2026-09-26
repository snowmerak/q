package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

func recoveryDefinition(name string, delegates ...string) subagent.AgentDefinition {
	return subagent.AgentDefinition{
		Info:         subagent.DelegateInfo{Name: name, Kind: subagent.AgentKindInner, Role: config.AgentRoleScout},
		SystemPrompt: "Complete the request.", Tools: []string{"write_file"}, Delegates: delegates, StrictTools: true,
	}
}

func recoveryDispatcher(t *testing.T, definitions []subagent.AgentDefinition, responses ...client.Message) (*delegationDispatcher, *planningClient, *fakeAgentTools) {
	t.Helper()
	registry, err := subagent.NewRegistry(definitions)
	if err != nil {
		t.Fatal(err)
	}
	value := config.Default()
	value.Provider.Model = "plan-model"
	configured := &planningClient{responses: responses}
	tools := &fakeAgentTools{}
	return &delegationDispatcher{registry: registry, client: configured, tools: tools, value: value, workingDirectory: t.TempDir(), runID: "run-test"}, configured, tools
}

func recoveryParent(t *testing.T, agent, prompt string) (workspace.Store, client.ToolCall) {
	t.Helper()
	store := workspace.Store{Root: t.TempDir()}
	call := planToolCall(subagent.DelegateToolName, `{"subagent_name":"`+agent+`","prompt":"`+prompt+`"}`)
	if err := store.Save(workspace.Session{RunID: "run-test", Transcript: []client.Message{{Role: client.RoleUser, Content: "parent request"}, {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}}}, Context: []client.Message{{Role: client.RoleUser, Content: "parent request"}, {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}}}}); err != nil {
		t.Fatal(err)
	}
	return store, call
}

func addRecoveryBookmark(t *testing.T, parent workspace.Store, call client.ToolCall, agent, prompt, id string) workspace.Store {
	t.Helper()
	if _, err := parent.AddDelegation(workspace.DelegationBookmark{InvocationID: id, CallIndex: 1, ToolIndex: 0, CallID: call.ID, Agent: agent, Prompt: prompt, RunID: "run-test"}); err != nil {
		t.Fatal(err)
	}
	child, err := parent.ChildStore(id)
	if err != nil {
		t.Fatal(err)
	}
	return child
}

func TestStoredDelegationReusesCompletedChildAcrossParentWriteGap(t *testing.T) {
	parent, call := recoveryParent(t, "workspace/worker", "do work")
	d, configured, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/worker")},
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"do work"}`)}},
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"done"}`)}},
	)
	input := delegateInput{SubagentName: "workspace/worker", Prompt: "do work"}
	first, err := d.dispatch(t.Context(), "", nil, &parent, "", call, input)
	if err != nil || first.IsError {
		t.Fatalf("first = %#v, %v", first, err)
	}
	items, err := parent.LoadDelegations()
	if err != nil || len(items) != 1 {
		t.Fatalf("bookmarks = %#v, %v", items, err)
	}
	child, err := parent.ChildStore(items[0].InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	childSession, err := child.Load()
	if err != nil || childSession.Version != workspace.CurrentVersion || len(childSession.Transcript) < 6 {
		t.Fatalf("child session = %#v, %v", childSession, err)
	}
	state, err := child.LoadDelegationState()
	if err != nil || state.Status != "completed" || state.Result == nil {
		t.Fatalf("child state = %#v, %v", state, err)
	}
	requests := len(configured.requests)
	second, err := d.dispatch(t.Context(), "", nil, &parent, "", call, input)
	if err != nil || second != first || len(configured.requests) != requests {
		t.Fatalf("replayed child: %#v, %v, requests=%d", second, err, len(configured.requests))
	}
}

func TestRecoveryCreatesChildFromExistingBookmarkAndBootstrapState(t *testing.T) {
	for _, bootstrap := range []bool{false, true} {
		t.Run(map[bool]string{false: "bookmark_only", true: "state_without_session"}[bootstrap], func(t *testing.T) {
			parent, call := recoveryParent(t, "workspace/worker", "do work")
			child := addRecoveryBookmark(t, parent, call, "workspace/worker", "do work", "fixed-child")
			if bootstrap {
				if err := child.SaveDelegationState(workspace.DelegationState{Agent: "workspace/worker", Prompt: "do work", RunID: "run-test", Model: "plan-model", Status: "running"}); err != nil {
					t.Fatal(err)
				}
			}
			d, _, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/worker")},
				client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"do work"}`)}},
				client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"done"}`)}},
			)
			result, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/worker", Prompt: "do work"})
			if err != nil || result.IsError {
				t.Fatalf("result = %#v, %v", result, err)
			}
			if _, err := child.Load(); err != nil {
				t.Fatal(err)
			}
			items, _ := parent.LoadDelegations()
			if len(items) != 1 || items[0].InvocationID != "fixed-child" {
				t.Fatalf("bookmarks = %#v", items)
			}
		})
	}
}

func TestRecoveryMarksRunningOrdinaryToolUnknownWithoutReexecution(t *testing.T) {
	parent, call := recoveryParent(t, "workspace/worker", "do work")
	child := addRecoveryBookmark(t, parent, call, "workspace/worker", "do work", "child-unknown")
	start := planToolCall(subagent.TaskStartToolName, `{"objective":"do work"}`)
	write := planToolCall("write_file", `{"path":"test.txt"}`)
	messages := []client.Message{{Role: client.RoleSystem, Content: "system"}, {Role: client.RoleUser, Content: "do work"}, {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{start}}, client.ToolResultMessage(start, client.ToolResult{Content: `{"started":true}`}), {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{write}}}
	if err := child.Save(workspace.Session{RunID: "run-test", Transcript: messages, Context: messages}); err != nil {
		t.Fatal(err)
	}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: "workspace/worker", Prompt: "do work", RunID: "run-test", Model: "plan-model", Round: 2, Started: true, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	d, configured, tools := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/worker")}, client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"blocked","summary":"cannot verify write","blocker":"write result unknown"}`)}})
	result, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/worker", Prompt: "do work"})
	if err != nil || result.IsError || len(tools.calls) != 0 {
		t.Fatalf("result = %#v, %v; tool calls = %#v", result, err, tools.calls)
	}
	saved, err := child.Load()
	if err != nil {
		t.Fatal(err)
	}
	unknowns := 0
	for _, message := range saved.Transcript {
		if message.Role == client.RoleTool && message.ToolCallID == write.ID && strings.Contains(message.Content, `"status":"unknown"`) {
			unknowns++
		}
	}
	if unknowns != 1 || len(configured.requests) != 1 || !strings.Contains(configured.requests[0].Messages[len(configured.requests[0].Messages)-1].Content, `"status":"unknown"`) {
		t.Fatalf("unknowns=%d requests=%#v", unknowns, configured.requests)
	}
	state, err := child.LoadDelegationState()
	if err != nil || len(state.UnknownTools) != 1 || state.UnknownTools[0].CallID != write.ID {
		t.Fatalf("unknown tool state=%#v err=%v", state, err)
	}
}

func TestRecoveryTrustsSavedToolResultWhenStateFileLags(t *testing.T) {
	parent, call := recoveryParent(t, "workspace/worker", "do work")
	child := addRecoveryBookmark(t, parent, call, "workspace/worker", "do work", "lagging-state")
	start := planToolCall(subagent.TaskStartToolName, `{"objective":"do work"}`)
	write := planToolCall("write_file", `{"path":"out.txt"}`)
	messages := []client.Message{{Role: client.RoleSystem, Content: "system"}, {Role: client.RoleUser, Content: "do work"}, {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{start}}, client.ToolResultMessage(start, client.ToolResult{Content: `{"started":true}`}), {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{write}}, client.ToolResultMessage(write, client.ToolResult{Content: "already wrote"})}
	if err := child.Save(workspace.Session{RunID: "run-test", Transcript: messages, Context: messages, ResponseAffinity: &workspace.ResponseAffinity{Model: "plan-model", Key: "cache_latest", APIMode: "responses"}}); err != nil {
		t.Fatal(err)
	}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: "workspace/worker", Prompt: "do work", RunID: "run-test", Model: "plan-model", APIMode: "responses", ConversationID: "cache_old", Round: 1, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	d, configured, tools := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/worker")}, client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"done"}`)}})
	d.apiMode = func(string) string { return "responses" }
	result, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/worker", Prompt: "do work"})
	if err != nil || result.IsError || len(tools.calls) != 0 || len(configured.requests) != 1 || configured.requests[0].ConversationID != "cache_latest" {
		t.Fatalf("result=%#v err=%v tools=%#v requests=%#v", result, err, tools.calls, configured.requests)
	}
	state, err := child.LoadDelegationState()
	if err != nil || !state.Started || state.Round != 3 || state.Status != "completed" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestNestedRecoveryConsumesGrandchildResultBeforeResumingParent(t *testing.T) {
	parent, call := recoveryParent(t, "workspace/parent", "parent task")
	child := addRecoveryBookmark(t, parent, call, "workspace/parent", "parent task", "child-parent")
	start := planToolCall(subagent.TaskStartToolName, `{"objective":"parent task"}`)
	grandCall := planToolCall(subagent.DelegateToolName, `{"subagent_name":"workspace/child","prompt":"child task"}`)
	messages := []client.Message{{Role: client.RoleSystem, Content: "system"}, {Role: client.RoleUser, Content: "parent task"}, {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{start}}, client.ToolResultMessage(start, client.ToolResult{Content: `{"started":true}`}), {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{grandCall}}}
	if err := child.Save(workspace.Session{RunID: "run-test", Transcript: messages, Context: messages}); err != nil {
		t.Fatal(err)
	}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: "workspace/parent", Prompt: "parent task", RunID: "run-test", TaskID: "child-parent", Model: "plan-model", Round: 2, Started: true, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if _, err := child.AddDelegation(workspace.DelegationBookmark{InvocationID: "grandchild", CallIndex: 4, ToolIndex: 0, CallID: grandCall.ID, Agent: "workspace/child", Prompt: "child task", RunID: "run-test"}); err != nil {
		t.Fatal(err)
	}
	grandchild, err := child.ChildStore("grandchild")
	if err != nil {
		t.Fatal(err)
	}
	if err := grandchild.Save(workspace.Session{RunID: "run-test", Transcript: []client.Message{{Role: client.RoleUser, Content: "child task"}}}); err != nil {
		t.Fatal(err)
	}
	completed := client.ToolResult{Content: `{"outcome":"succeeded","summary":"child done"}`}
	if err := grandchild.SaveDelegationState(workspace.DelegationState{Agent: "workspace/child", Prompt: "child task", RunID: "run-test", Model: "plan-model", Status: "completed", Result: &completed}); err != nil {
		t.Fatal(err)
	}
	d, configured, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/parent", "workspace/child"), recoveryDefinition("workspace/child")}, client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"parent done"}`)}})
	result, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/parent", Prompt: "parent task"})
	if err != nil || result.IsError || !strings.Contains(result.Content, "parent done") || len(configured.requests) != 1 {
		t.Fatalf("result=%#v err=%v requests=%#v", result, err, configured.requests)
	}
	saved, err := child.Load()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range saved.Transcript {
		if message.Role == client.RoleTool && message.ToolCallID == grandCall.ID && message.Content == completed.Content {
			found = true
		}
	}
	if !found {
		t.Fatalf("grandchild result missing from parent: %#v", saved.Transcript)
	}
}

func TestExternalRecoveryReturnsUnknownWithoutRepeatingInvocation(t *testing.T) {
	name := "workspace/external"
	parent, call := recoveryParent(t, name, "research")
	child := addRecoveryBookmark(t, parent, call, name, "research", "external-child")
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: name, Prompt: "research", RunID: "run-test", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	registry, err := subagent.NewRegistry([]subagent.AgentDefinition{{Info: subagent.DelegateInfo{Name: name, Kind: subagent.AgentKindExternal, Role: config.AgentRoleSearch}, SystemPrompt: "Research."}})
	if err != nil {
		t.Fatal(err)
	}
	invocations := 0
	d := &delegationDispatcher{registry: registry, external: map[string]subagent.Invocation{name: {Tool: subagent.ExternalSearchTool(), Handler: func(context.Context, client.ToolCall) (client.ToolResult, error) {
		invocations++
		return client.ToolResult{Content: "unexpected"}, nil
	}}}, capture: func(_ context.Context, _ subagent.InvocationSource, _ client.ToolCall, result client.ToolResult) (client.ToolResult, error) {
		return result, nil
	}}
	result, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: name, Prompt: "research"})
	if err != nil || !result.IsError || !strings.Contains(result.Content, `"status":"unknown"`) || invocations != 0 {
		t.Fatalf("result=%#v err=%v invocations=%d", result, err, invocations)
	}
	state, err := child.LoadDelegationState()
	if err != nil || state.Status != "unknown" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestDelegationPositionUsesOrdinalWithRepeatedCallID(t *testing.T) {
	call := planToolCall(subagent.DelegateToolName, `{"subagent_name":"workspace/worker","prompt":"same"}`)
	messages := []client.Message{{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}}, client.ToolResultMessage(call, client.ToolResult{Content: "first"}), {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call, call}}, client.ToolResultMessage(call, client.ToolResult{Content: "second"})}
	messageIndex, toolIndex, err := pendingDelegationPosition(messages, call)
	if err != nil || messageIndex != 2 || toolIndex != 1 {
		t.Fatalf("position=%d,%d err=%v", messageIndex, toolIndex, err)
	}
}

func TestPersistedDelegationRejectsMissingCallIDBeforeExecution(t *testing.T) {
	parent, call := recoveryParent(t, "workspace/worker", "do work")
	call.ID = ""
	d, configured, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/worker")})
	_, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/worker", Prompt: "do work"})
	if err == nil || len(configured.requests) != 0 {
		t.Fatalf("err=%v requests=%#v", err, configured.requests)
	}
	bookmarks, loadErr := parent.LoadDelegations()
	if loadErr != nil || len(bookmarks) != 0 {
		t.Fatalf("bookmarks=%#v err=%v", bookmarks, loadErr)
	}
}

func TestInterruptedOrdinaryCallsUseOrdinalWhenIDsRepeat(t *testing.T) {
	call := planToolCall("write_file", `{"path":"same"}`)
	messages := []client.Message{{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call, call}}, client.ToolResultMessage(call, client.ToolResult{Content: "first"})}
	recovered, count := reconcileInterruptedToolCalls(messages)
	if count != 1 || len(recovered) != 3 || recovered[2].ToolCallID != call.ID || !strings.Contains(recovered[2].Content, "outcome unknown") {
		t.Fatalf("count=%d recovered=%#v", count, recovered)
	}
}

func TestWorkspaceRestartRestoresCompletedDelegateExactlyOnce(t *testing.T) {
	store, call := recoveryParent(t, subagent.BuiltinScoutID, "inspect")
	child := addRecoveryBookmark(t, store, call, subagent.BuiltinScoutID, "inspect", "saved-scout")
	result := client.ToolResult{Content: `{"outcome":"succeeded","summary":"inspected"}`}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: subagent.BuiltinScoutID, Prompt: "inspect", RunID: "run-test", Model: "plan-model", Status: "completed", Result: &result}); err != nil {
		t.Fatal(err)
	}
	value := config.Default()
	value.Provider.Model = "plan-model"
	configured := &planningClient{}
	restart := func() model {
		m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
		m.workspaceStore = &store
		m.toolRuntime = &fakeAgentTools{}
		m.enterChat(value, configured)
		return m
	}
	first := restart()
	if !first.delegationRecoveryPending {
		t.Fatal("delegation recovery was not scheduled")
	}
	command := first.startDelegationRecovery()
	if command == nil || !first.waiting {
		t.Fatal("delegation recovery did not start asynchronously")
	}
	updated, resume := first.Update(command())
	first = updated.(model)
	if len(first.messages) < 3 || first.messages[len(first.messages)-1].ToolCallID != call.ID || first.messages[len(first.messages)-1].Content != result.Content {
		t.Fatalf("recovered messages=%#v status=%q", first.messages, first.status)
	}
	if resume == nil || !first.waiting || first.recoverDelegationTurn {
		t.Fatal("parent continuation did not enter a turn")
	}
	saved, err := store.Load()
	if err != nil || len(saved.Transcript) != 3 || len(saved.Context) != 3 {
		t.Fatalf("saved=%#v err=%v", saved, err)
	}
	second := restart()
	if len(second.messages) != len(first.messages) || len(configured.requests) != 0 {
		t.Fatalf("repeated recovery: %#v requests=%#v", second.messages, configured.requests)
	}
}

func TestRecoveryReloadsParentResultSavedBeforeUIAcknowledgment(t *testing.T) {
	store, call := recoveryParent(t, subagent.BuiltinScoutID, "inspect")
	value := config.Default()
	value.Provider.Model = "plan-model"
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &store
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, &planningClient{})
	if !m.delegationRecoveryPending {
		t.Fatal("missing recovery")
	}
	// Simulate a previous recovery that durably wrote the parent result but
	// exited before the UI consumed its completion event.
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	result := client.ToolResult{Content: `{"outcome":"succeeded","summary":"done"}`}
	saved.Transcript = append(saved.Transcript, client.ToolResultMessage(call, result))
	saved.Context = append(saved.Context, client.ToolResultMessage(call, result))
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	command := m.startDelegationRecovery()
	updated, resume := m.Update(command())
	m = updated.(model)
	if resume == nil || !m.waiting || m.delegationRecoveryPending || m.messages[len(m.messages)-1].Content != result.Content {
		t.Fatalf("recovery state waiting=%v pending=%v messages=%#v", m.waiting, m.delegationRecoveryPending, m.messages)
	}
}

func TestCancelledRecoveryIgnoresLateEventAndReusesCompletedChild(t *testing.T) {
	store, call := recoveryParent(t, subagent.BuiltinScoutID, "inspect")
	child := addRecoveryBookmark(t, store, call, subagent.BuiltinScoutID, "inspect", "cancel-child")
	result := client.ToolResult{Content: `{"outcome":"succeeded","summary":"done"}`}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: subagent.BuiltinScoutID, Prompt: "inspect", RunID: "run-test", Model: "plan-model", Status: "completed", Result: &result}); err != nil {
		t.Fatal(err)
	}
	value := config.Default()
	value.Provider.Model = "plan-model"
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &store
	m.toolRuntime = &fakeAgentTools{}
	configured := &planningClient{}
	m.enterChat(value, configured)
	first := m.startDelegationRecovery()
	late := first()
	updated, _ := m.interruptTurn()
	m = updated.(model)
	before := len(m.messages)
	updated, _ = m.Update(late)
	m = updated.(model)
	if len(m.messages) != before || !m.delegationRecoveryPending {
		t.Fatalf("late event applied: messages=%#v pending=%v", m.messages, m.delegationRecoveryPending)
	}
	retry := m.startDelegationRecovery()
	updated, resume := m.Update(retry())
	m = updated.(model)
	if resume == nil || m.messages[len(m.messages)-1].Content != result.Content || len(configured.requests) != 0 {
		t.Fatalf("retry failed: messages=%#v requests=%#v", m.messages, configured.requests)
	}
}

func TestRecoveryFinalizesSavedTerminalAcknowledgmentWithoutModelReplay(t *testing.T) {
	parent, call := recoveryParent(t, "workspace/worker", "do work")
	child := addRecoveryBookmark(t, parent, call, "workspace/worker", "do work", "terminal-child")
	start := planToolCall(subagent.TaskStartToolName, `{"objective":"do work"}`)
	complete := planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"done"}`)
	messages := []client.Message{{Role: client.RoleSystem, Content: "system"}, {Role: client.RoleUser, Content: "do work"}, {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{start}}, client.ToolResultMessage(start, client.ToolResult{Content: `{"started":true}`}), {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{complete}}, client.ToolResultMessage(complete, client.ToolResult{Content: `{"outcome":"succeeded","summary":"done"}`}), {Role: client.RoleAssistant, Content: "acknowledged"}}
	if err := child.Save(workspace.Session{RunID: "run-test", Transcript: messages, Context: messages}); err != nil {
		t.Fatal(err)
	}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: "workspace/worker", Prompt: "do work", RunID: "run-test", Model: "plan-model", Round: 2, Started: true, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	d, configured, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/worker")})
	result, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/worker", Prompt: "do work"})
	if err != nil || result.IsError || !strings.Contains(result.Content, "done") || len(configured.requests) != 0 {
		t.Fatalf("result=%#v err=%v requests=%#v", result, err, configured.requests)
	}
}

func TestRecoveryAddsMissingReminderAfterSavedTextReply(t *testing.T) {
	parent, call := recoveryParent(t, "workspace/worker", "do work")
	child := addRecoveryBookmark(t, parent, call, "workspace/worker", "do work", "reminder-child")
	messages := []client.Message{{Role: client.RoleSystem, Content: "system"}, {Role: client.RoleUser, Content: "do work"}, {Role: client.RoleAssistant, Content: "I will do it"}}
	if err := child.Save(workspace.Session{RunID: "run-test", Transcript: messages, Context: messages}); err != nil {
		t.Fatal(err)
	}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: "workspace/worker", Prompt: "do work", RunID: "run-test", Model: "plan-model", Round: 1, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	d, configured, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/worker")},
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"do work"}`)}},
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"done"}`)}},
	)
	result, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/worker", Prompt: "do work"})
	if err != nil || result.IsError || len(configured.requests) != 2 {
		t.Fatalf("result=%#v err=%v requests=%#v", result, err, configured.requests)
	}
	requestMessages := configured.requests[0].Messages
	if requestMessages[len(requestMessages)-1].Role != client.RoleSystem || !strings.Contains(requestMessages[len(requestMessages)-1].Content, "task_start") {
		t.Fatalf("missing reminder: %#v", requestMessages)
	}
}

func TestRecoveryBlocksWhenSavedModelIsUnavailable(t *testing.T) {
	parent, call := recoveryParent(t, "workspace/worker", "do work")
	child := addRecoveryBookmark(t, parent, call, "workspace/worker", "do work", "model-child")
	if err := child.Save(workspace.Session{RunID: "run-test", Transcript: []client.Message{{Role: client.RoleSystem, Content: "system"}, {Role: client.RoleUser, Content: "do work"}}}); err != nil {
		t.Fatal(err)
	}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: "workspace/worker", Prompt: "do work", RunID: "run-test", Model: "old-model", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	d, _, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/worker")})
	result, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/worker", Prompt: "do work"})
	if err != nil || !result.IsError || !strings.Contains(result.Content, "unavailable") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	state, err := child.LoadDelegationState()
	if err != nil || state.Status != "blocked" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestRecoveryBlocksWhenAgentDefinitionIsRemoved(t *testing.T) {
	parent, call := recoveryParent(t, "workspace/worker", "do work")
	child := addRecoveryBookmark(t, parent, call, "workspace/worker", "do work", "removed-agent")
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: "workspace/worker", Prompt: "do work", RunID: "run-test", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	d, configured, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/other")})
	result, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/worker", Prompt: "do work"})
	if err != nil || !result.IsError || !strings.Contains(result.Content, "no longer") || len(configured.requests) != 0 {
		t.Fatalf("result=%#v err=%v requests=%#v", result, err, configured.requests)
	}
	state, err := child.LoadDelegationState()
	if err != nil || state.Status != "blocked" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestRecoveryBlocksCorruptChildCheckpointWithoutRunningModel(t *testing.T) {
	for _, missingSession := range []bool{false, true} {
		t.Run(map[bool]string{false: "state_ahead", true: "missing_session"}[missingSession], func(t *testing.T) {
			parent, call := recoveryParent(t, "workspace/worker", "do work")
			child := addRecoveryBookmark(t, parent, call, "workspace/worker", "do work", "corrupt-child")
			if !missingSession {
				if err := child.Save(workspace.Session{RunID: "run-test", Transcript: []client.Message{{Role: client.RoleSystem, Content: "system"}, {Role: client.RoleUser, Content: "do work"}}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := child.SaveDelegationState(workspace.DelegationState{Agent: "workspace/worker", Prompt: "do work", RunID: "run-test", Model: "plan-model", Round: 1, Status: "running"}); err != nil {
				t.Fatal(err)
			}
			d, configured, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/worker")})
			result, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/worker", Prompt: "do work"})
			if err != nil || !result.IsError || len(configured.requests) != 0 {
				t.Fatalf("result=%#v err=%v requests=%#v", result, err, configured.requests)
			}
			state, err := child.LoadDelegationState()
			if err != nil || state.Status != "blocked" {
				t.Fatalf("state=%#v err=%v", state, err)
			}
		})
	}
}

func TestResponsesChildRestoresReplayAndCacheAffinity(t *testing.T) {
	parent, call := recoveryParent(t, "workspace/worker", "do work")
	child := addRecoveryBookmark(t, parent, call, "workspace/worker", "do work", "responses-child")
	start := planToolCall(subagent.TaskStartToolName, `{"objective":"do work"}`)
	messages := []client.Message{{Role: client.RoleSystem, Content: "system"}, {Role: client.RoleUser, Content: "do work"}, {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{start}}, client.ToolResultMessage(start, client.ToolResult{Content: `{"started":true}`})}
	if err := child.Save(workspace.Session{RunID: "run-test", Transcript: messages, Context: messages, ResponseReplay: []workspace.ResponseReplayItem{{Index: 2, Model: "plan-model", Output: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"saved"}`)}}}, ResponseAffinity: &workspace.ResponseAffinity{Model: "plan-model", Key: "cache_saved"}}); err != nil {
		t.Fatal(err)
	}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: "workspace/worker", Prompt: "do work", RunID: "run-test", Model: "plan-model", APIMode: "responses", ConversationID: "cache_saved", Round: 1, Started: true, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	d, configured, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/worker")}, client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"done"}`)}})
	d.apiMode = func(string) string { return "responses" }
	result, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/worker", Prompt: "do work"})
	if err != nil || result.IsError || len(configured.requests) != 1 {
		t.Fatalf("result=%#v err=%v requests=%#v", result, err, configured.requests)
	}
	request := configured.requests[0]
	if request.ConversationID != "cache_saved" || len(request.Messages[2].ResponseOutput) != 1 {
		t.Fatalf("lost Responses state: %#v", request)
	}
}

func TestRecoveryBlocksWhenSavedAPIModeChanges(t *testing.T) {
	parent, call := recoveryParent(t, "workspace/worker", "do work")
	child := addRecoveryBookmark(t, parent, call, "workspace/worker", "do work", "api-child")
	if err := child.Save(workspace.Session{RunID: "run-test", Transcript: []client.Message{{Role: client.RoleSystem, Content: "system"}, {Role: client.RoleUser, Content: "do work"}}}); err != nil {
		t.Fatal(err)
	}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: "workspace/worker", Prompt: "do work", RunID: "run-test", Model: "plan-model", APIMode: "responses", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	d, configured, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/worker")})
	d.apiMode = func(string) string { return "chat_completions" }
	result, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/worker", Prompt: "do work"})
	if err != nil || !result.IsError || len(configured.requests) != 0 {
		t.Fatalf("result=%#v err=%v requests=%#v", result, err, configured.requests)
	}
	state, err := child.LoadDelegationState()
	if err != nil || state.Status != "blocked" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestSavedDelegationCountEnforcesCallBudgetWithoutBlockingExistingChild(t *testing.T) {
	parent, call := recoveryParent(t, "workspace/worker", "do work")
	child := addRecoveryBookmark(t, parent, call, "workspace/worker", "do work", "existing")
	result := client.ToolResult{Content: `{"outcome":"succeeded","summary":"done"}`}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: "workspace/worker", Prompt: "do work", RunID: "run-test", Status: "completed", Result: &result}); err != nil {
		t.Fatal(err)
	}
	for index := 1; index < maximumDelegationCalls; index++ {
		id := fmt.Sprintf("saved-%d", index)
		if _, err := parent.AddDelegation(workspace.DelegationBookmark{InvocationID: id, CallIndex: index + 100, Agent: "workspace/worker", Prompt: "prior", RunID: "run-test"}); err != nil {
			t.Fatal(err)
		}
	}
	count, err := countSavedDelegations(parent, "run-test", 0)
	if err != nil || count != maximumDelegationCalls {
		t.Fatalf("count=%d err=%v", count, err)
	}
	d, _, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/worker")})
	d.calls = count
	got, err := d.dispatch(t.Context(), "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/worker", Prompt: "do work"})
	if err != nil || got != result {
		t.Fatalf("existing child blocked: %#v, %v", got, err)
	}
	newCall := planToolCall(subagent.DelegateToolName, `{"subagent_name":"workspace/worker","prompt":"new"}`)
	session, err := parent.Load()
	if err != nil {
		t.Fatal(err)
	}
	session.Transcript = append(session.Transcript, client.ToolResultMessage(call, result), client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{newCall}})
	if err := parent.Save(session); err != nil {
		t.Fatal(err)
	}
	blocked, err := d.dispatch(t.Context(), "", nil, &parent, "", newCall, delegateInput{SubagentName: "workspace/worker", Prompt: "new"})
	if err != nil || !blocked.IsError || !strings.Contains(blocked.Content, "limit") {
		t.Fatalf("budget result=%#v err=%v", blocked, err)
	}
}
