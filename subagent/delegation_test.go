package subagent

import (
	"strings"
	"testing"

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
	want := []string{BuiltinCoderID, BuiltinGrillerID, BuiltinPlannerID, BuiltinReviewerID, BuiltinScoutID}
	listed := registry.List()
	for index, name := range want {
		if listed[index].Name != name {
			t.Fatalf("builtin order = %#v", listed)
		}
	}
	if got := delegateNames(registry.Allowed(BuiltinCoderID)); strings.Join(got, ",") != BuiltinReviewerID+","+BuiltinScoutID {
		t.Fatalf("coder grants = %v", got)
	}
	for _, name := range []string{BuiltinGrillerID, BuiltinPlannerID, BuiltinReviewerID} {
		if got := delegateNames(registry.Allowed(name)); len(got) != 1 || got[0] != BuiltinScoutID {
			t.Fatalf("%s grants = %v", name, got)
		}
	}
	if len(registry.Allowed(BuiltinScoutID)) != 0 {
		t.Fatal("scout unexpectedly delegates")
	}
	for _, name := range []string{BuiltinGrillerID, BuiltinPlannerID, BuiltinReviewerID} {
		definition, found := registry.Get(name)
		if !found {
			t.Fatalf("missing definition %q", name)
		}
		for _, tool := range definition.Tools {
			if tool == "read_file" || tool == "list_directory" {
				t.Fatalf("%s directly reads the workspace with %q", name, tool)
			}
		}
	}
	for _, name := range []string{BuiltinScoutID, BuiltinCoderID} {
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
		if info.MutatesWorkspace != (info.Name == BuiltinCoderID) {
			t.Fatalf("mutation capability = %#v", info)
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
	for _, name := range []string{BuiltinGrillerID, BuiltinPlannerID, BuiltinReviewerID} {
		if got := delegateNames(registry.Allowed(name)); !strings.Contains(strings.Join(got, ","), BuiltinWebSearchID) {
			t.Fatalf("%s grants = %v", name, got)
		}
	}
}

func TestRegistryRejectsInvalidDelegationGraphs(t *testing.T) {
	definition := func(name string, delegates ...string) AgentDefinition {
		return AgentDefinition{
			Info:         DelegateInfo{Name: name, Role: config.AgentRoleScout},
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
		{"invalid-kind", []AgentDefinition{{Info: DelegateInfo{Name: "global/a", Kind: "remote", Role: config.AgentRoleScout}, SystemPrompt: "Inspect."}}, "invalid kind"},
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
		Delegates: []string{BuiltinScoutID, BuiltinWebSearchID},
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
		"findings":["one"],
		"artifacts":["file.go"],
		"verification":["go test ./..."],
		"blocker":""
	}`)
	if err != nil || result.Summary != "done" {
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

func TestGeneralRunnerRejectsOversizedPromptBeforeCallingModel(t *testing.T) {
	configuredClient := &fakeScoutClient{}
	definition, _ := NewRegistry(BuiltinAgentDefinitions())
	scout, _ := definition.Get(BuiltinScoutID)
	_, err := (GeneralRunner{
		Client: configuredClient, Spec: Spec{Role: config.AgentRoleScout, Model: "test"}, Definition: scout,
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
