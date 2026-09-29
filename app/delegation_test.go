package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/change"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/lsp"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/subagent"
	qtools "github.com/snowmerak/q/tools"
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
		Version: 1, Name: "reader", Role: config.AgentRoleResearch,
		SystemPrompt: "Read.", Tools: []string{}, Delegates: []string{subagent.BuiltinSeniorDeveloperID},
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
	if len(listed) != 1 || listed[0].Name != subagent.BuiltinSeniorDeveloperID {
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
		Version: 1, Name: "reader", Role: config.AgentRoleResearch,
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
	for _, expected := range []string{subagent.BuiltinSeniorDeveloperID, subagent.BuiltinWebSearchID, subagent.BuiltinWebTesterID, "global/reader"} {
		if !containsAgentName(names, expected) {
			t.Fatalf("missing %s from %#v", expected, listed)
		}
	}
	for _, removed := range []string{"builtin/scout", "builtin/griller", "builtin/planner", "builtin/executor", "builtin/coder"} {
		if containsAgentName(names, removed) {
			t.Fatalf("removed subagent %s remains in %#v", removed, listed)
		}
	}
	for _, info := range listed {
		if strings.HasPrefix(info.Name, "external/") {
			t.Fatalf("kind leaked into subagent ID = %#v", info)
		}
	}
}

func TestDelegationExecutionWorkspaceSeparatesSessionStateAndCheckout(t *testing.T) {
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.config = config.Default()
	m.config.Provider.Model = "test-model"
	m.client = &fakeClient{models: []client.Model{{ID: "test-model"}}}
	m.toolRuntime = &fakeAgentTools{}
	targetTools := &fakeAgentTools{}
	sessionStore := workspace.Store{Root: t.TempDir()}
	stateRoot := t.TempDir()
	checkoutRoot := t.TempDir()
	m.workspaceStore = &sessionStore
	if err := m.customStoreAt(stateRoot).Save(subagent.Profile{
		Version: 1, Name: "state-reader", Role: config.AgentRoleResearch,
		SystemPrompt: "Read the target repository.", Tools: []string{}, Delegates: []string{},
	}, "workspace", nil); err != nil {
		t.Fatal(err)
	}

	targetRuntime, err := configuredAgentToolRuntime(targetTools, mcpconfig.RoleDefault, m.activeConfig(), checkoutRoot)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := m.configuredDelegationRuntimeIn(targetRuntime, executionWorkspace{
		sessionStore: &sessionStore, workspaceStateRoot: stateRoot, checkoutRoot: checkoutRoot,
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	delegation, ok := runtime.(*delegationRuntime)
	if !ok {
		t.Fatalf("runtime = %T", runtime)
	}
	got := delegation.dispatcher.workspace
	if got.sessionStore != &sessionStore || got.workspaceStateRoot != stateRoot || got.checkoutRoot != checkoutRoot {
		t.Fatalf("execution workspace = %#v", got)
	}
	if delegation.dispatcher.capture == nil {
		t.Fatal("target runtime lost Loom capture through tool scopes")
	}
	if _, err := delegation.dispatcher.tools.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{Name: "write_file"}}); err != nil {
		t.Fatal(err)
	}
	if len(targetTools.calls) != 1 || len(m.toolRuntime.(*fakeAgentTools).calls) != 0 {
		t.Fatalf("target calls = %d, session calls = %d", len(targetTools.calls), len(m.toolRuntime.(*fakeAgentTools).calls))
	}
	result, err := runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{
		Name: subagent.DelegateListToolName, Arguments: `{}`,
	}})
	if err != nil || result.IsError || !strings.Contains(result.Content, "workspace/state-reader") {
		t.Fatalf("state-root profile list = %#v, err = %v", result, err)
	}
}

func TestStoredProfileRetiredGrantsAreInactiveButNewGrantsAreRejected(t *testing.T) {
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspace.Store{Root: t.TempDir()}
	legacy := subagent.Profile{
		Version: 1, Name: "legacy-reader", Role: config.AgentRoleAdvisor,
		SystemPrompt: "Read.", Tools: []string{},
		Delegates: []string{"builtin/scout", "builtin/griller", "builtin/planner", "builtin/executor", "builtin/coder", "builtin/reviewer", subagent.BuiltinSeniorDeveloperID},
	}
	if err := m.customStore().Save(legacy, "workspace", nil); err != nil {
		t.Fatal(err)
	}
	registry, err := buildSubagentRegistry(m.customStore())
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Get("builtin/scout"); found {
		t.Fatal("retired builtin is still registered")
	}
	if grants := delegateInfoNames(registry.Allowed("workspace/legacy-reader")); len(grants) != 1 || grants[0] != subagent.BuiltinSeniorDeveloperID {
		t.Fatalf("effective legacy grants = %v", grants)
	}
	for _, retired := range legacy.Delegates[:len(legacy.Delegates)-1] {
		newProfile := legacy
		newProfile.Name = "new-reader"
		newProfile.Delegates = []string{retired}
		if err := m.validateCustomDelegates(newProfile, "workspace", nil); err == nil || !strings.Contains(err.Error(), "unknown delegate") {
			t.Fatalf("new retired grant %s was accepted: %v", retired, err)
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
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"found it","report":"## Analysis\n\nThe model path preserves the selected provider.","findings":["app/model.go"]}`)}},
	}}
	m := newModel(t.Context(), store, nil)
	m.config = value
	m.client = configuredClient
	m.toolRuntime = &fakeAgentTools{}
	workspaceStore := workspace.Store{Root: t.TempDir()}
	m.workspaceStore = &workspaceStore
	caller := "workspace/reader"
	if err := m.customStore().Save(subagent.Profile{
		Version: 1, Name: "reader", Role: config.AgentRoleResearch,
		SystemPrompt: "Read.", Tools: []string{}, Delegates: []string{subagent.BuiltinSeniorDeveloperID},
	}, "workspace", nil); err != nil {
		t.Fatal(err)
	}
	runtime, err := m.configuredDelegationRuntimeFor(m.toolRuntime, workspaceStore.Root, caller, []string{caller})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Call(t.Context(), client.ToolCall{
		ID: "delegate-1", Function: client.FunctionCall{Name: subagent.DelegateToolName,
			Arguments: `{"subagent_name":"builtin/senior-developer","prompt":"inspect model handling"}`},
	})
	if err != nil || result.IsError || !strings.Contains(result.Content, `"loom_ref"`) || !strings.Contains(result.Content, `The model path preserves`) {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if len(configuredClient.requests) != 2 || len(configuredClient.terminalRequests) != 1 {
		t.Fatalf("requests = %#v, terminal = %#v", configuredClient.requests, configuredClient.terminalRequests)
	}
	if !requestHasTool(configuredClient.requests[0].Tools, "write_file") {
		t.Fatalf("senior developer is missing write_file: %#v", configuredClient.requests[0].Tools)
	}
	for _, required := range []string{subagent.TaskStartToolName, subagent.TaskCompleteToolName} {
		if !requestHasTool(configuredClient.requests[0].Tools, required) {
			t.Fatalf("lifecycle tool %s missing: %#v", required, configuredClient.requests[0].Tools)
		}
	}
}

func TestMutatingDelegationUsesWorktreeAndMergesThroughChangeRequest(t *testing.T) {
	repository := t.TempDir()
	delegationGit(t, repository, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, repository, "add", "README.md")
	delegationGit(t, repository, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "initial")

	settings := config.Store{Dir: t.TempDir()}
	workspaceStore := workspace.Store{Root: repository}
	if err := workspaceStore.EnsureQGitIgnored(t.Context()); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, repository, "add", ".gitignore")
	delegationGit(t, repository, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "ignore q metadata")
	sessionID, err := workspace.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	workspaceStore.SessionID = sessionID
	call := client.ToolCall{ID: "delegate-worktree", Type: client.ToolTypeFunction, Function: client.FunctionCall{
		Name: subagent.DelegateToolName, Arguments: `{"subagent_name":"builtin/senior-developer","prompt":"create child.txt"}`,
	}}
	if err := workspaceStore.Save(workspace.Session{RunID: "run-worktree", Transcript: []client.Message{{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}}}}); err != nil {
		t.Fatal(err)
	}

	configuredClient := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"create child.txt"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "write-child", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "write_file", Arguments: `{"path":"child.txt","content":"isolated worktree"}`}}}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"Add child file"}`)}},
	}}
	toolRuntime, err := qtools.NewRuntimeWithRoots(t.Context(), qtools.RuntimeRoots{
		WorkspaceStateRoot: repository, CheckoutRoot: repository,
	}, nil, loom.StoreOptions{}, lsp.GlobalConfig{}, lsp.WorkspaceConfig{}, qlibrary.NewClient("http://127.0.0.1:1", "", time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = toolRuntime.Close() })

	m := newModel(t.Context(), settings, nil)
	m.config = config.Default()
	m.config.Provider.Model = "plan-model"
	m.client = configuredClient
	m.toolRuntime = toolRuntime
	m.workspaceStore = &workspaceStore
	m.runID = "run-worktree"
	runtime, err := m.configuredDelegationRuntime(toolRuntime, repository)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Call(t.Context(), call)
	if err != nil || result.IsError {
		t.Fatalf("delegate result = %#v, err = %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(repository, "child.txt")); !os.IsNotExist(err) {
		t.Fatalf("delegated file leaked into parent before merge: %v", err)
	}
	bookmarks, err := workspaceStore.LoadDelegations()
	if err != nil || len(bookmarks) != 1 {
		t.Fatalf("bookmarks = %#v, err = %v", bookmarks, err)
	}
	child, err := workspaceStore.ChildStore(bookmarks[0].InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := child.LoadDelegationState()
	if err != nil {
		t.Fatal(err)
	}
	if state.ChangeRequest == nil || state.ChangeRequest.Status != change.StatusOpen || state.ChangeRequest.HeadCommit == "" {
		t.Fatalf("change request = %#v", state.ChangeRequest)
	}
	delegationGit(t, state.ChangeRequest.WorktreePath, "cat-file", "-t", state.ChangeRequest.BaseCommit)
	delegationGit(t, state.ChangeRequest.WorktreePath, "cat-file", "-t", state.ChangeRequest.HeadCommit)
	delegationGit(t, state.ChangeRequest.WorktreePath, "diff", "--stat", state.ChangeRequest.BaseCommit, state.ChangeRequest.HeadCommit, "--")

	read, err := runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{
		Name: subagent.ChangeRequestReadToolName, Arguments: `{"change_request_id":"` + state.ChangeRequest.ID + `"}`,
	}})
	if err != nil || read.IsError || !strings.Contains(read.Content, "child.txt") {
		t.Fatalf("read change request = %#v, err = %v", read, err)
	}
	merged, err := runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{
		Name: subagent.ChangeRequestMergeToolName, Arguments: `{"change_request_id":"` + state.ChangeRequest.ID + `"}`,
	}})
	if err != nil || merged.IsError {
		t.Fatalf("merge change request = %#v, err = %v", merged, err)
	}
	body, err := os.ReadFile(filepath.Join(repository, "child.txt"))
	if err != nil || strings.TrimSpace(string(body)) != "isolated worktree" {
		t.Fatalf("merged body = %q, err = %v", body, err)
	}
	state, err = child.LoadDelegationState()
	if err != nil || state.ChangeRequest == nil || state.ChangeRequest.Status != change.StatusMerged {
		t.Fatalf("merged state = %#v, err = %v", state.ChangeRequest, err)
	}
}

func TestNestedMutatingDelegationMergesJuniorIntoSeniorWorktree(t *testing.T) {
	repository := t.TempDir()
	delegationGit(t, repository, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, repository, "add", "README.md")
	delegationGit(t, repository, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "initial")
	workspaceStore := workspace.Store{Root: repository}
	if err := workspaceStore.EnsureQGitIgnored(t.Context()); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, repository, "add", ".gitignore")
	delegationGit(t, repository, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "ignore q metadata")
	sessionID, err := workspace.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	workspaceStore.SessionID = sessionID
	call := client.ToolCall{ID: "delegate-nested-worktree", Type: client.ToolTypeFunction, Function: client.FunctionCall{
		Name: subagent.DelegateToolName, Arguments: `{"subagent_name":"builtin/senior-developer","prompt":"delegate nested.txt to the junior and review it"}`,
	}}
	if err := workspaceStore.Save(workspace.Session{RunID: "run-nested-worktree", Transcript: []client.Message{{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}}}}); err != nil {
		t.Fatal(err)
	}

	toolRuntime, err := qtools.NewRuntimeWithRoots(t.Context(), qtools.RuntimeRoots{
		WorkspaceStateRoot: repository, CheckoutRoot: repository,
	}, nil, loom.StoreOptions{}, lsp.GlobalConfig{}, lsp.WorkspaceConfig{}, qlibrary.NewClient("http://127.0.0.1:1", "", time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = toolRuntime.Close() })
	configuredClient := &nestedWorktreeClient{}
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.config = config.Default()
	m.config.Provider.Model = "plan-model"
	m.client = configuredClient
	m.toolRuntime = toolRuntime
	m.workspaceStore = &workspaceStore
	m.runID = "run-nested-worktree"
	runtime, err := m.configuredDelegationRuntime(toolRuntime, repository)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Call(t.Context(), call)
	if err != nil || result.IsError {
		t.Fatalf("nested delegate result = %#v, err = %v", result, err)
	}
	var outerResult subagent.TaskResult
	outerResult, err = decodeDelegatedTaskResult(result.Content)
	if err != nil || outerResult.ChangeRequest == nil || outerResult.ChangeRequest.Status != change.StatusOpen {
		t.Fatalf("outer task result = %#v, err = %v", outerResult, err)
	}
	if _, err := os.Stat(filepath.Join(repository, "nested.txt")); !os.IsNotExist(err) {
		t.Fatalf("nested change leaked into root before outer review: %v", err)
	}
	rootBookmarks, err := workspaceStore.LoadDelegations()
	if err != nil || len(rootBookmarks) != 1 {
		t.Fatalf("root bookmarks = %#v, err = %v", rootBookmarks, err)
	}
	seniorStore, err := workspaceStore.ChildStore(rootBookmarks[0].InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	juniorBookmarks, err := seniorStore.LoadDelegations()
	if err != nil || len(juniorBookmarks) != 1 {
		t.Fatalf("junior bookmarks = %#v, err = %v", juniorBookmarks, err)
	}
	juniorStore, err := seniorStore.ChildStore(juniorBookmarks[0].InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	juniorState, err := juniorStore.LoadDelegationState()
	if err != nil || juniorState.ChangeRequest == nil || juniorState.ChangeRequest.Status != change.StatusMerged {
		t.Fatalf("junior change request = %#v, err = %v", juniorState.ChangeRequest, err)
	}

	merged, err := runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{
		Name: subagent.ChangeRequestMergeToolName, Arguments: `{"change_request_id":"` + outerResult.ChangeRequest.ID + `"}`,
	}})
	if err != nil || merged.IsError {
		t.Fatalf("merge outer change request = %#v, err = %v", merged, err)
	}
	if body, err := os.ReadFile(filepath.Join(repository, "nested.txt")); err != nil || strings.TrimSpace(string(body)) != "nested worktree" {
		t.Fatalf("merged nested file = %q, err = %v", body, err)
	}
}

type nestedWorktreeClient struct {
	mu         sync.Mutex
	seniorStep int
	juniorStep int
	changeID   string
}

func (c *nestedWorktreeClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if request.ToolChoice == client.ToolChoiceNone && len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Role == client.RoleTool {
		return terminalAcknowledgment(""), nil
	}
	senior := false
	for _, tool := range request.Tools {
		if tool.Function.Name == subagent.DelegateToolName {
			senior = true
			break
		}
	}
	var message client.Message
	if senior {
		switch c.seniorStep {
		case 0:
			message = client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "senior-start", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: subagent.TaskStartToolName, Arguments: `{"objective":"review junior implementation"}`}}}}
		case 1:
			message = client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "senior-delegate", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: subagent.DelegateToolName, Arguments: `{"subagent_name":"builtin/junior-developer","prompt":"create nested.txt containing nested worktree"}`}}}}
		case 2:
			if len(request.Messages) == 0 {
				return nil, errors.New("nested test: missing junior result")
			}
			content := request.Messages[len(request.Messages)-1].Content
			result, err := decodeDelegatedTaskResult(content)
			if err != nil {
				return nil, fmt.Errorf("nested test: decode junior change request %q: %w", content, err)
			}
			if result.ChangeRequest == nil {
				return nil, fmt.Errorf("nested test: junior result has no change request: %s", content)
			}
			c.changeID = result.ChangeRequest.ID
			message = client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "senior-read", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: subagent.ChangeRequestReadToolName, Arguments: `{"change_request_id":"` + c.changeID + `"}`}}}}
		case 3:
			message = client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "senior-merge", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: subagent.ChangeRequestMergeToolName, Arguments: `{"change_request_id":"` + c.changeID + `"}`}}}}
		case 4:
			message = client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "senior-complete", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: subagent.TaskCompleteToolName, Arguments: `{"outcome":"succeeded","summary":"Reviewed and merged junior change"}`}}}}
		default:
			return nil, errors.New("nested test: unexpected senior round")
		}
		c.seniorStep++
	} else {
		switch c.juniorStep {
		case 0:
			message = client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "junior-start", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: subagent.TaskStartToolName, Arguments: `{"objective":"create nested.txt"}`}}}}
		case 1:
			message = client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "junior-write", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "write_file", Arguments: `{"path":"nested.txt","content":"nested worktree"}`}}}}
		case 2:
			message = client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "junior-complete", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: subagent.TaskCompleteToolName, Arguments: `{"outcome":"succeeded","summary":"Add nested file"}`}}}}
		default:
			return nil, errors.New("nested test: unexpected junior round")
		}
		c.juniorStep++
	}
	return &client.ChatResponse{Choices: []client.Choice{{Message: message}}}, nil
}

func (c *nestedWorktreeClient) ListModels(context.Context) ([]client.Model, error) {
	return []client.Model{{ID: "plan-model"}}, nil
}

func (c *nestedWorktreeClient) Close() error { return nil }

func decodeDelegatedTaskResult(content string) (subagent.TaskResult, error) {
	var direct subagent.TaskResult
	if err := json.Unmarshal([]byte(content), &direct); err != nil {
		return subagent.TaskResult{}, err
	}
	if direct.Outcome != "" {
		return direct, nil
	}
	var receipt struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(content), &receipt); err != nil || len(receipt.Result) == 0 {
		return subagent.TaskResult{}, errors.New("captured task result is missing its result")
	}
	if err := json.Unmarshal(receipt.Result, &direct); err != nil {
		return subagent.TaskResult{}, err
	}
	return direct, nil
}

func delegationGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, arguments...)...)
	body, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, body)
	}
	return strings.TrimSpace(string(body))
}

func delegateInfoNames(values []subagent.DelegateInfo) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.Name)
	}
	return result
}
