package app

import (
	"context"
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
	value.Agents.Connections = map[string]config.AgentConnectionConfig{
		"search-agent": {Preset: "codex"},
		"web-agent":    {Preset: "codex"},
	}
	value.Agents.Roles = map[string]config.AgentConfig{
		config.AgentRoleSearch:            {Agent: "search-agent"},
		config.AgentRoleExternalWebTester: {Agent: "web-agent"},
	}
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
	for _, expected := range []string{subagent.BuiltinScoutID, subagent.BuiltinExecutorID, subagent.BuiltinCoderID, subagent.BuiltinWebSearchID, subagent.BuiltinWebTesterID, "global/reader"} {
		if !containsAgentName(names, expected) {
			t.Fatalf("missing %s from %#v", expected, listed)
		}
	}
	for _, info := range listed {
		if strings.HasPrefix(info.Name, "external/") {
			t.Fatalf("kind leaked into subagent ID = %#v", info)
		}
	}
}

func TestExternalDelegationUsesACPInvocationWithoutCallingNativeModel(t *testing.T) {
	registry, err := subagent.NewRegistry(subagent.PublicAgentDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	var received subagent.ExternalSearchInput
	var captured subagent.InvocationSource
	dispatcher := &delegationDispatcher{
		registry: registry,
		external: map[string]subagent.Invocation{
			subagent.BuiltinWebSearchID: {
				Tool: subagent.ExternalSearchTool(),
				Source: subagent.InvocationSource{
					Protocol: "acp", Name: "search-agent", Kind: "agent-result",
					MediaType: "application/vnd.q.agent-result+json",
				},
				Handler: func(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
					var parseErr error
					received, parseErr = subagent.ParseExternalSearchInput(call.Function.Arguments)
					return client.ToolResult{Content: `{"agent":"search-agent","summary":"found it"}`}, parseErr
				},
			},
		},
		capture: func(_ context.Context, source subagent.InvocationSource, _ client.ToolCall, result client.ToolResult) (client.ToolResult, error) {
			captured = source
			return result, nil
		},
	}
	runtime := &delegationRuntime{base: &fakeAgentTools{}, dispatcher: dispatcher}
	result, err := runtime.Call(t.Context(), client.ToolCall{
		ID: "delegate-external", Function: client.FunctionCall{Name: subagent.DelegateToolName,
			Arguments: `{"subagent_name":"builtin/web-search","prompt":"find current release notes"}`},
	})
	if err != nil || result.IsError {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if received.Query != "find current release notes" || captured.Protocol != "acp" || captured.Name != "search-agent" {
		t.Fatalf("input = %#v, source = %#v", received, captured)
	}
}

func TestExternalWebTesterDelegationUsesRequestAdapter(t *testing.T) {
	registry, err := subagent.NewRegistry(subagent.PublicAgentDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	var received subagent.ExternalWebTesterInput
	dispatcher := &delegationDispatcher{
		registry: registry,
		external: map[string]subagent.Invocation{
			subagent.BuiltinWebTesterID: {
				Tool: subagent.ExternalWebTesterTool(),
				Source: subagent.InvocationSource{
					Protocol: "acp", Name: "web-agent", Kind: "agent-result",
					MediaType: "application/vnd.q.agent-result+json",
				},
				Handler: func(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
					var parseErr error
					received, parseErr = subagent.ParseExternalWebTesterInput(call.Function.Arguments)
					return client.ToolResult{Content: `{"agent":"web-agent","outcome":"succeeded","summary":"verified"}`}, parseErr
				},
			},
		},
		capture: func(_ context.Context, _ subagent.InvocationSource, _ client.ToolCall, result client.ToolResult) (client.ToolResult, error) {
			return result, nil
		},
	}
	runtime := &delegationRuntime{base: &fakeAgentTools{}, dispatcher: dispatcher}
	result, err := runtime.Call(t.Context(), client.ToolCall{
		ID: "delegate-web", Function: client.FunctionCall{Name: subagent.DelegateToolName,
			Arguments: `{"subagent_name":"builtin/web-tester","prompt":"verify login"}`},
	})
	if err != nil || result.IsError || received.Request != "verify login" {
		t.Fatalf("result = %#v, input = %#v, err = %v", result, received, err)
	}
}

func TestCustomExternalDelegationUsesGenericACPAdapter(t *testing.T) {
	definition := subagent.AgentDefinition{
		Info:         subagent.DelegateInfo{Name: "global/researcher", Source: "global", Kind: subagent.AgentKindExternal},
		SystemPrompt: "Research primary sources.", Connection: "research-acp",
	}
	registry, err := subagent.NewRegistry(append(subagent.PublicAgentDefinitions(), definition))
	if err != nil {
		t.Fatal(err)
	}
	var received externalSubagentInput
	dispatcher := &delegationDispatcher{
		registry: registry,
		external: map[string]subagent.Invocation{
			definition.Info.Name: {
				Tool:   configuredExternalSubagentInvocation("", "research-acp", config.AgentConnectionConfig{Preset: "codex"}, definition).Tool,
				Source: subagent.InvocationSource{Protocol: "acp", Name: "research-acp", Kind: "agent-result"},
				Handler: func(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
					if err := decodeDelegationArguments(call.Function.Arguments, &received); err != nil {
						return client.ToolResult{}, err
					}
					return client.ToolResult{Content: "researched"}, nil
				},
			},
		},
		capture: func(_ context.Context, _ subagent.InvocationSource, _ client.ToolCall, result client.ToolResult) (client.ToolResult, error) {
			return result, nil
		},
	}
	runtime := &delegationRuntime{base: &fakeAgentTools{}, dispatcher: dispatcher}
	result, err := runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{
		Name: subagent.DelegateToolName, Arguments: `{"subagent_name":"global/researcher","prompt":"find the release"}`,
	}})
	if err != nil || result.IsError || received.Request != "find the release" {
		t.Fatalf("result = %#v, input = %#v, err = %v", result, received, err)
	}
	if prompt := externalSubagentPrompt(definition.SystemPrompt, received.Request); !strings.HasPrefix(prompt, definition.SystemPrompt+"\n\nRequest:\n") {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestConfiguredExternalDelegatesIncludesCustomProfileConnection(t *testing.T) {
	definition := subagent.AgentDefinition{
		Info:         subagent.DelegateInfo{Name: "workspace/researcher", Source: "workspace", Kind: subagent.AgentKindExternal},
		SystemPrompt: "Research the explicit request.", Connection: "research-acp",
	}
	registry, err := subagent.NewRegistry(append(subagent.PublicAgentDefinitions(), definition))
	if err != nil {
		t.Fatal(err)
	}
	value := config.Default()
	value.Agents.Connections = map[string]config.AgentConnectionConfig{"research-acp": {Preset: "codex"}}
	configured := configuredExternalDelegates(value, t.TempDir(), registry)
	invocation, found := configured[definition.Info.Name]
	if !found || invocation.Source.Protocol != "acp" || invocation.Source.Name != "research-acp" || invocation.Source.MediaType != "text/plain" {
		t.Fatalf("invocation = %#v, found = %v", invocation, found)
	}
	connection := value.Agents.Connections["research-acp"]
	connection.Disabled = true
	value.Agents.Connections["research-acp"] = connection
	if _, found = configuredExternalDelegates(value, t.TempDir(), registry)[definition.Info.Name]; found {
		t.Fatal("disabled custom external connection remained available")
	}
}

func TestUnconfiguredExternalDelegatesAreNotAdvertised(t *testing.T) {
	registry, err := subagent.NewRegistry(subagent.PublicAgentDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &delegationRuntime{
		base: &fakeAgentTools{},
		dispatcher: &delegationDispatcher{
			registry: registry, value: config.Default(), tools: &fakeAgentTools{},
			capture: func(_ context.Context, _ subagent.InvocationSource, _ client.ToolCall, result client.ToolResult) (client.ToolResult, error) {
				return result, nil
			},
		},
	}
	for _, info := range runtime.available() {
		if info.Kind == subagent.AgentKindExternal {
			t.Fatalf("unconfigured external delegate advertised: %#v", info)
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
