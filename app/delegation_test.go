package app

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

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
	if _, found := delegation.dispatcher.registry.Get("workspace/state-reader"); !found {
		t.Fatal("state-root profile is missing from the ticket worker registry")
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
