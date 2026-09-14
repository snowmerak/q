package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

func TestDelegationRuntimeListsOnlyDirectGrantsForCustomCaller(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(t.Context(), store, nil)
	m.config = value
	m.client = &fakeClient{models: []client.Model{{ID: "test-model"}}}
	m.toolRuntime = &fakeAgentTools{}
	workspaceStore := workspace.Store{Root: t.TempDir()}
	m.workspaceStore = &workspaceStore
	profile := subagent.Profile{
		Version: 1, Name: "reader", Role: config.AgentRoleScout,
		SystemPrompt: "Read.", Tools: []string{}, Delegates: []string{subagent.BuiltinScoutID},
	}
	if err := m.customStore().Save(profile, "workspace", nil); err != nil {
		t.Fatal(err)
	}
	caller := "workspace/reader"
	runtime, err := m.configuredDelegationRuntimeFor(m.toolRuntime, workspaceStore.Root, caller, []string{caller})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{
		Name: subagent.DelegateListToolName, Arguments: `{}`,
	}})
	if err != nil || result.IsError {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	var listed []subagent.DelegateInfo
	if err := json.Unmarshal([]byte(result.Content), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != subagent.BuiltinScoutID {
		t.Fatalf("listed = %#v", listed)
	}
	result, err = runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{
		Name:      subagent.DelegateToolName,
		Arguments: `{"subagent_name":"builtin/coder","prompt":"change the workspace"}`,
	}})
	if err != nil || !result.IsError {
		t.Fatalf("unauthorized result = %#v, err = %v", result, err)
	}
	if len(m.client.(*fakeClient).requests) != 0 {
		t.Fatal("unauthorized delegation reached the model")
	}
}

func TestRootDelegationListContainsBuiltinsAndCanonicalProfiles(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(t.Context(), store, nil)
	m.config = value
	m.client = &fakeClient{models: []client.Model{{ID: "test-model"}}}
	m.toolRuntime = &fakeAgentTools{}
	workspaceStore := workspace.Store{Root: t.TempDir()}
	m.workspaceStore = &workspaceStore
	if err := m.customStore().Save(subagent.Profile{
		Version: 1, Name: "reader", Role: config.AgentRoleScout,
		SystemPrompt: "Read.", Tools: []string{}, Delegates: []string{},
	}, "global", nil); err != nil {
		t.Fatal(err)
	}
	runtime, err := m.configuredDelegationRuntime(m.toolRuntime, workspaceStore.Root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{
		Name: subagent.DelegateListToolName, Arguments: `{}`,
	}})
	if err != nil || result.IsError {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	var listed []subagent.DelegateInfo
	if err := json.Unmarshal([]byte(result.Content), &listed); err != nil {
		t.Fatal(err)
	}
	names := delegateInfoNames(listed)
	for _, expected := range []string{subagent.BuiltinScoutID, subagent.BuiltinCoderID, "global/reader"} {
		if !containsAgentName(names, expected) {
			t.Fatalf("missing %s from %#v", expected, listed)
		}
	}
}

func TestAllowedDelegationRunsLifecycleAndCapturesResult(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "plan-model"
	configuredClient := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"inspect"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"found it","findings":["app/model.go"]}`)}},
	}}
	m := newModel(t.Context(), store, nil)
	m.config = value
	m.client = configuredClient
	m.toolRuntime = &fakeAgentTools{}
	workspaceStore := workspace.Store{Root: t.TempDir()}
	m.workspaceStore = &workspaceStore
	caller := "workspace/reader"
	if err := m.customStore().Save(subagent.Profile{
		Version: 1, Name: "reader", Role: config.AgentRoleScout,
		SystemPrompt: "Read.", Tools: []string{}, Delegates: []string{subagent.BuiltinScoutID},
	}, "workspace", nil); err != nil {
		t.Fatal(err)
	}
	runtime, err := m.configuredDelegationRuntimeFor(m.toolRuntime, workspaceStore.Root, caller, []string{caller})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Call(t.Context(), client.ToolCall{
		ID: "delegate-1", Function: client.FunctionCall{Name: subagent.DelegateToolName,
			Arguments: `{"subagent_name":"builtin/scout","prompt":"inspect model handling"}`},
	})
	if err != nil || result.IsError || !strings.Contains(result.Content, `"loom_ref"`) {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if len(configuredClient.requests) != 2 || len(configuredClient.terminalRequests) != 1 {
		t.Fatalf("requests = %#v, terminal = %#v", configuredClient.requests, configuredClient.terminalRequests)
	}
	for _, tool := range configuredClient.requests[0].Tools {
		if tool.Function.Name == "write_file" {
			t.Fatalf("Scout received a mutation tool: %#v", configuredClient.requests[0].Tools)
		}
	}
	for _, required := range []string{subagent.TaskStartToolName, subagent.TaskCompleteToolName} {
		if !requestHasTool(configuredClient.requests[0].Tools, required) {
			t.Fatalf("lifecycle tool %s missing: %#v", required, configuredClient.requests[0].Tools)
		}
	}
}

func delegateInfoNames(values []subagent.DelegateInfo) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.Name)
	}
	return result
}
