package config

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRetiredLearningRolesCannotReactivateFromSavedConfig(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	legacy := Default()
	legacy.Provider.Model = "test-model"
	legacy.Agents.Roles = map[string]AgentConfig{
		AgentRoleThinker: {Model: "old-thinker"}, AgentRoleLibrarian: {Model: "old-librarian"},
		AgentRoleResearch: {Model: "keep-research"},
	}
	body, err := yaml.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Agents.Roles[AgentRoleResearch].Model != "keep-research" || loaded.Agents.Roles[AgentRoleThinker].Model != "" || loaded.Agents.Roles[AgentRoleLibrarian].Model != "" {
		t.Fatalf("legacy roles: %+v %v", loaded.Agents.Roles, err)
	}
	if ValidCustomRoleName(AgentRoleThinker) || ValidCustomRoleName(AgentRoleLibrarian) || !ValidCustomRoleName("project-specialist") {
		t.Fatal("retired roles can be recreated as custom roles")
	}
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(store.Path())
	if err != nil || strings.Contains(string(saved), "old-thinker") || strings.Contains(string(saved), "old-librarian") {
		t.Fatalf("retired roles saved again: %s %v", saved, err)
	}
}
