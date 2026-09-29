package subagent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func TestBuiltinAgentDefinitionsArePublicAndBounded(t *testing.T) {
	definitions := BuiltinAgentDefinitions()
	if len(definitions) != 5 {
		t.Fatalf("builtins = %#v", definitions)
	}
	registry, err := NewRegistry(definitions)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{BuiltinInterviewerID, BuiltinJuniorDeveloperID, BuiltinManagerID, BuiltinResearchID, BuiltinSeniorDeveloperID}
	listed := registry.List()
	if _, found := registry.Get("builtin/reviewer"); found {
		t.Fatal("separate reviewer subagent is still registered")
	}
	if slices.Contains(delegateNames(registry.Allowed("")), BuiltinJuniorDeveloperID) {
		t.Fatal("junior developer must be assigned by a senior developer")
	}
	for index, name := range want {
		if listed[index].Name != name {
			t.Fatalf("builtin order = %#v", listed)
		}
	}
	for name, wantGrants := range map[string][]string{
		BuiltinInterviewerID:     {BuiltinResearchID},
		BuiltinManagerID:         {BuiltinInterviewerID, BuiltinResearchID, BuiltinSeniorDeveloperID},
		BuiltinSeniorDeveloperID: {BuiltinJuniorDeveloperID, BuiltinResearchID},
		BuiltinJuniorDeveloperID: {},
		BuiltinResearchID:        {},
	} {
		got := delegateNames(registry.Allowed(name))
		if !slices.Equal(got, wantGrants) {
			t.Fatalf("%s delegates = %v; want %v", name, got, wantGrants)
		}
	}
	junior, found := registry.Get(BuiltinJuniorDeveloperID)
	if !found || junior.Info.Role != config.AgentRoleCoder || !slices.Contains(junior.Tools, "write_file") || !slices.Contains(junior.Tools, "run_command") {
		t.Fatalf("junior implementation surface = %#v", junior)
	}
	senior, found := registry.Get(BuiltinSeniorDeveloperID)
	if !found || senior.Info.Role != config.AgentRoleReviewer || !senior.Info.MutatesWorkspace {
		t.Fatalf("senior developer definition = %#v", senior)
	}
	for _, required := range []string{"loom_read", "edit_file", "write_file", "create_directory", "move_path", "copy_path", "remove_path", "run_command"} {
		if !slices.Contains(senior.Tools, required) {
			t.Fatalf("senior developer missing workspace tool %q", required)
		}
	}
	for _, name := range want {
		definition, found := registry.Get(name)
		if !found {
			t.Fatalf("missing definition %q", name)
		}
		for _, required := range []string{"read_file", "list_directory"} {
			found := false
			for _, tool := range definition.Tools {
				if tool == required {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%s missing direct workspace tool %q", name, required)
			}
		}
	}
	for _, info := range listed {
		if info.Kind != AgentKindInner {
			t.Fatalf("builtin kind = %#v", info)
		}
		wantMutation := info.Name == BuiltinJuniorDeveloperID || info.Name == BuiltinSeniorDeveloperID || info.Name == BuiltinManagerID
		if info.MutatesWorkspace != wantMutation {
			t.Fatalf("mutation capability = %#v; want %v", info, wantMutation)
		}
	}
}

func TestPublicAgentDefinitionsIncludeExternalACPAdapters(t *testing.T) {
	registry, err := NewRegistry(PublicAgentDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{BuiltinWebSearchID, BuiltinWebTesterID} {
		definition, found := registry.Get(name)
		if !found || definition.Info.Kind != AgentKindExternal || definition.Info.Source != "builtin" {
			t.Fatalf("external definition %q = %#v, %v", name, definition, found)
		}
		if definition.SystemPrompt == "" || len(definition.Tools) != 0 || len(definition.Delegates) != 0 {
			t.Fatalf("external definition uses inner execution fields: %#v", definition)
		}
	}
	if got := delegateNames(registry.Allowed(BuiltinSeniorDeveloperID)); !slices.Contains(got, BuiltinWebSearchID) {
		t.Fatalf("senior developer web search grant = %v", got)
	}
	if got := delegateNames(registry.Allowed(BuiltinResearchID)); !slices.Contains(got, BuiltinWebSearchID) {
		t.Fatalf("research web search grant = %v", got)
	}
}

func TestSeniorDeveloperToolSurfaceAllowsDirectMutation(t *testing.T) {
	registry, err := NewRegistry(PublicAgentDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	senior, found := registry.Get(BuiltinSeniorDeveloperID)
	if !found {
		t.Fatal("senior developer definition is missing")
	}
	runtime := &fakeScoutTools{available: append(append(append(DelegateTools(), ChangeRequestTools()...), skillTestTools()...),
		client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "read_file"}},
		client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "loom_read"}},
		client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "edit_file"}},
		client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "write_file"}},
		client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "run_command"}},
	)}
	tools, err := selectDefinitionTools(senior, runtime)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{DelegateListToolName, DelegateToolName, ChangeRequestReadToolName, ChangeRequestMergeToolName, ChangeRequestCloseToolName, TaskStartToolName, TaskCompleteToolName, "read_file", "loom_read", "edit_file", "write_file", "run_command"} {
		if !hasTool(tools, required) {
			t.Fatalf("senior developer runtime is missing %q: %#v", required, tools)
		}
	}
}

func TestRegistryPropagatesMutationCapabilityThroughDelegates(t *testing.T) {
	definitions := append(BuiltinAgentDefinitions(), AgentDefinition{
		Info:         DelegateInfo{Name: "global/implementer", Role: config.AgentRoleCoder, MutatesWorkspace: true},
		SystemPrompt: "Implement.", Tools: []string{"write_file"},
	}, AgentDefinition{
		Info:         DelegateInfo{Name: "global/coordinator", Role: config.AgentRoleResearch},
		SystemPrompt: "Coordinate.", Delegates: []string{"global/implementer"},
	})
	registry, err := NewRegistry(definitions)
	if err != nil {
		t.Fatal(err)
	}
	definition, found := registry.Get("global/coordinator")
	if !found || !definition.Info.MutatesWorkspace {
		t.Fatalf("transitive mutation capability = %#v, %v", definition, found)
	}
}

func TestRegistryRejectsInvalidDelegationGraphs(t *testing.T) {
	definition := func(name string, delegates ...string) AgentDefinition {
		return AgentDefinition{
			Info:         DelegateInfo{Name: name, Role: config.AgentRoleResearch},
			SystemPrompt: "Inspect.", Delegates: delegates,
		}
	}
	tests := []struct {
		name        string
		definitions []AgentDefinition
		want        string
	}{
		{"unknown", []AgentDefinition{definition("global/a", "global/missing")}, "unknown delegate"},
		{"self", []AgentDefinition{definition("global/a", "global/a")}, "cannot delegate to itself"},
		{"cycle", []AgentDefinition{definition("global/a", "global/b"), definition("global/b", "global/a")}, "delegation cycle"},
		{"global-to-workspace", []AgentDefinition{definition("global/a", "workspace/b"), definition("workspace/b")}, "cannot delegate to workspace"},
		{"duplicate", []AgentDefinition{definition("global/a"), definition("global/a")}, "duplicate agent ID"},
		{"invalid-kind", []AgentDefinition{{Info: DelegateInfo{Name: "global/a", Kind: "remote", Role: config.AgentRoleResearch}, SystemPrompt: "Inspect."}}, "invalid kind"},
		{"external-missing-prompt", []AgentDefinition{{Info: DelegateInfo{Name: "global/a", Kind: AgentKindExternal, Role: config.AgentRoleSearch}}}, "requires a system prompt"},
		{"external-tools", []AgentDefinition{{Info: DelegateInfo{Name: "global/a", Kind: AgentKindExternal, Role: config.AgentRoleSearch}, SystemPrompt: "Inspect.", Tools: []string{"read_file"}}}, "cannot use q tools"},
		{"external-bad-connection", []AgentDefinition{{Info: DelegateInfo{Name: "global/a", Kind: AgentKindExternal}, SystemPrompt: "Inspect.", Connection: "bad id"}}, "invalid ACP connection"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewRegistry(test.definitions); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDefinitionForProfileUsesCanonicalScope(t *testing.T) {
	profile := Profile{
		Version: 1, Name: "implementer", Role: config.AgentRoleCoder,
		SystemPrompt: "Implement.", Tools: []string{"read_file", "write_file"},
		Delegates: []string{BuiltinSeniorDeveloperID, BuiltinWebSearchID},
	}
	definition, err := DefinitionForProfile(ProfileEntry{Profile: profile, Scope: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if definition.Info.Name != "workspace/implementer" || !definition.Info.MutatesWorkspace || !definition.StrictTools ||
		definition.Info.Kind != AgentKindInner || len(definition.Delegates) != 2 || definition.Delegates[1] != BuiltinWebSearchID {
		t.Fatalf("definition = %#v", definition)
	}
}

func TestCommonTaskCompleteSchemaRejectsExecutorFields(t *testing.T) {
	result, err := parseGeneralTaskComplete(`{
		"outcome":"succeeded",
		"summary":"done",
		"report":"## Analysis\n\nDetailed final explanation.",
		"findings":["one"],
		"artifacts":["file.go"],
		"verification":["go test ./..."],
		"blocker":""
	}`)
	if err != nil || result.Summary != "done" || !strings.Contains(result.Report, "Detailed final explanation") {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	for _, arguments := range []string{
		`{"outcome":"succeeded","summary":"done","executor":"coder"}`,
		`{"outcome":"succeeded","summary":"done","evidence":[]}`,
	} {
		if _, err := parseGeneralTaskComplete(arguments); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("arguments %s: %v", arguments, err)
		}
	}
}

func TestCommonTaskCompleteAllowsBoundedLongReport(t *testing.T) {
	arguments, err := json.Marshal(taskCompleteInput{
		Outcome: "succeeded", Summary: "done", Report: strings.Repeat("r", maximumTaskReportBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := parseGeneralTaskComplete(string(arguments))
	if err != nil || len(result.Report) != maximumTaskReportBytes {
		t.Fatalf("report bytes = %d, err = %v", len(result.Report), err)
	}
	arguments, err = json.Marshal(taskCompleteInput{
		Outcome: "succeeded", Summary: "done", Report: strings.Repeat("r", maximumTaskReportBytes+1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseGeneralTaskComplete(string(arguments)); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("oversized report = %v", err)
	}
}

func TestGeneralRunnerRejectsOversizedPromptBeforeCallingModel(t *testing.T) {
	configuredClient := &fakeScoutClient{}
	definition, _ := NewRegistry(BuiltinAgentDefinitions())
	senior, _ := definition.Get(BuiltinSeniorDeveloperID)
	_, err := (GeneralRunner{
		Client: configuredClient, Spec: Spec{Role: config.AgentRoleAdvisor, Model: "test"}, Definition: senior,
	}).Run(t.Context(), strings.Repeat("x", MaximumDelegatePromptBytes+1))
	if err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Fatalf("error = %v", err)
	}
	if len(configuredClient.requests) != 0 {
		t.Fatal("oversized prompt reached the model")
	}
}

func delegateNames(values []DelegateInfo) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.Name)
	}
	return result
}
