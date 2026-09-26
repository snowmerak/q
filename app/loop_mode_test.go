package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	acp "github.com/snowmerak/q/third_party/acp-go-sdk"
	"github.com/snowmerak/q/workspace"
)

func TestDelegationModePersistsAndRestoresPrompt(t *testing.T) {
	root := t.TempDir()
	value := config.Default()
	value.Provider.Model = "test-model"
	makeModel := func() model {
		m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
		m.toolRuntime = &fakeAgentTools{}
		m.workspaceStore = &workspace.Store{Root: root}
		m.enterChat(value, &fakeClient{})
		return m
	}
	m := makeModel()
	if m.loopMode != loopModeDefault || countModePrompt(m.messages) != 0 || countModePolicy(m.messages) != 0 {
		t.Fatalf("initial mode = %q, prompt/policy counts = %d/%d", m.loopMode, countModePrompt(m.messages), countModePolicy(m.messages))
	}
	m.conversationID = "cache_previous_prompt"
	prior := client.Message{Role: client.RoleAssistant, Content: "prior answer", ResponseModel: "test-model", ResponseOutput: []json.RawMessage{json.RawMessage(`{"type":"message"}`)}}
	m.messages = append(m.messages, prior)
	m.memory.Append(prior)
	if err := m.setLoopMode(loopModeDelegation); err != nil {
		t.Fatal(err)
	}
	if m.conversationID != "" || len(m.messages[len(m.messages)-1].ResponseOutput) != 0 || len(m.memory.Messages()[len(m.memory.Messages())-1].ResponseOutput) != 0 {
		t.Fatal("mode change retained provider continuation state")
	}
	if countModePrompt(m.messages) != 1 || countModePrompt(m.memory.Messages()) != 1 || countModePolicy(m.messages) != 1 || countModePolicy(m.memory.Messages()) != 1 {
		t.Fatal("delegation prompt or policy is absent or duplicated")
	}
	saved, err := m.workspaceStore.Load()
	if err != nil || saved.LoopMode != loopModeDelegation {
		t.Fatalf("saved mode = %q, err = %v", saved.LoopMode, err)
	}
	restarted := makeModel()
	if restarted.loopMode != loopModeDelegation || countModePrompt(restarted.messages) != 1 || countModePrompt(restarted.memory.Messages()) != 1 || countModePolicy(restarted.messages) != 1 || countModePolicy(restarted.memory.Messages()) != 1 {
		t.Fatalf("restored mode = %q, prompt/policy counts = %d/%d, %d/%d", restarted.loopMode, countModePrompt(restarted.messages), countModePrompt(restarted.memory.Messages()), countModePolicy(restarted.messages), countModePolicy(restarted.memory.Messages()))
	}
	restarted.resetConversationState()
	if restarted.loopMode != loopModeDelegation || countModePrompt(restarted.messages) != 1 {
		t.Fatal("clearing a session did not preserve its mode")
	}
	if err := restarted.setLoopMode(loopModeDefault); err != nil {
		t.Fatal(err)
	}
	if countModePrompt(restarted.messages) != 0 || countModePrompt(restarted.memory.Messages()) != 0 || countModePolicy(restarted.messages) != 0 || countModePolicy(restarted.memory.Messages()) != 0 {
		t.Fatal("default mode retained the delegation prompt or policy")
	}
}

func TestLoopModeCommandAndNewSessionReset(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.toolRuntime = &fakeAgentTools{}
	m.workspaceStore = &workspace.Store{Root: t.TempDir()}
	m.enterChat(config.Default(), &fakeClient{})
	m.input.SetValue("/mode delegation")
	updated, _ := m.submitChat()
	m = updated.(model)
	if m.loopMode != loopModeDelegation || m.status != "Mode: delegation" {
		t.Fatalf("mode command result: mode=%q status=%q", m.loopMode, m.status)
	}
	m.activeTask = &workspace.ActiveTask{Objective: "finish current work"}
	if status := m.runLoopModeCommand("/mode default"); status != "Mode: default" || m.loopMode != loopModeDefault || m.activeTask == nil {
		t.Fatalf("active task mode change: status=%q mode=%q task=%#v", status, m.loopMode, m.activeTask)
	}
	m.activeTask = nil
	if status := m.runLoopModeCommand("/mode delegation"); status != "Mode: delegation" {
		t.Fatal(status)
	}
	if err := m.startNewWorkspaceSession(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.workspaceLock.Close() }()
	if m.loopMode != loopModeDefault || countModePrompt(m.messages) != 0 {
		t.Fatalf("new session inherited mode=%q, prompt count=%d", m.loopMode, countModePrompt(m.messages))
	}
}

func TestACPModeCommandPersistsForSession(t *testing.T) {
	agent, store, _ := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, store.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("/mode delegation")},
	})
	if err != nil || response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("ACP mode command response=%#v err=%v", response, err)
	}
	runtime := activeACPRuntime(t, agent, sessionID)
	saved, err := runtime.state.workspaceStore.Load()
	if err != nil || saved.LoopMode != loopModeDelegation || countModePrompt(runtime.state.memory.Messages()) != 1 {
		t.Fatalf("ACP saved mode=%q prompt count=%d err=%v", saved.LoopMode, countModePrompt(runtime.state.memory.Messages()), err)
	}
}

func TestDelegationModeReservesBuiltinToolsForSubagents(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.config = value
	m.client = &fakeClient{models: []client.Model{{ID: "test-model"}}}
	base := &fakeAgentTools{tools: []client.Tool{
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "read_file"}},
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "write_file"}},
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "residual_tool"}},
	}}
	m.toolRuntime = base
	root := t.TempDir()
	m.workspaceStore = &workspace.Store{Root: root}
	defaultRuntime, err := m.configuredDelegationRuntime(base, root)
	if err != nil {
		t.Fatal(err)
	}
	if !toolAvailable(defaultRuntime, "read_file") || !toolAvailable(defaultRuntime, "write_file") {
		t.Fatal("default mode changed its direct tools")
	}
	m.loopMode = loopModeDelegation
	runtime, err := m.configuredDelegationRuntime(base, root)
	if err != nil {
		t.Fatal(err)
	}
	if toolAvailable(runtime, "read_file") || toolAvailable(runtime, "write_file") || !toolAvailable(runtime, "residual_tool") || !toolAvailable(runtime, subagent.DelegateToolName) {
		t.Fatalf("unexpected delegation tools: %#v", runtime.Tools())
	}
	result, err := runtime.Call(context.Background(), client.ToolCall{Function: client.FunctionCall{Name: "write_file"}})
	if err != nil || !result.IsError || len(base.calls) != 0 {
		t.Fatalf("reserved tool reached base: result=%#v err=%v calls=%d", result, err, len(base.calls))
	}
	child, err := m.configuredDelegationRuntimeFor(base, root, subagent.BuiltinCoderID, []string{subagent.BuiltinCoderID})
	if err != nil {
		t.Fatal(err)
	}
	if !toolAvailable(child, "write_file") {
		t.Fatal("delegation mode removed a child tool")
	}
}

func countModePrompt(messages []client.Message) int {
	count := 0
	for _, message := range messages {
		if message.Name == delegationPromptName {
			count++
		}
	}
	return count
}

func countModePolicy(messages []client.Message) int {
	count := 0
	for _, message := range messages {
		if message.Name == delegationPolicyName {
			count++
		}
	}
	return count
}

func TestPriorTaskActionUsesLatestStartedTask(t *testing.T) {
	start := client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{Function: client.FunctionCall{Name: taskStartToolName}}}}
	list := client.Message{Role: client.RoleTool, Name: subagent.DelegateListToolName, Content: `[]`}
	delegate := client.Message{Role: client.RoleTool, Name: subagent.DelegateToolName, Content: `{"summary":"inspected"}`}
	failed := client.Message{Role: client.RoleTool, Name: subagent.DelegateToolName, Content: "Tool error: unavailable"}
	if priorTaskAction([]client.Message{start, list, failed}) {
		t.Fatal("listing and failed delegation counted as completed work")
	}
	if !priorTaskAction([]client.Message{start, delegate}) {
		t.Fatal("successful delegation was not retained across turns")
	}
	if priorTaskAction([]client.Message{start, delegate, start}) {
		t.Fatal("previous task's work counted for the next task")
	}
}
