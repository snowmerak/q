package app

import (
	"testing"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func TestSessionModelOverridesWorkspaceWithoutChangingSharedServices(t *testing.T) {
	for _, workspaceModel := range []string{"", "workspace-model"} {
		t.Run(workspaceModel, func(t *testing.T) {
			value := config.Default()
			value.Provider.Model = "global-model"
			state := model{
				conversationState: conversationState{config: value},
				sessionState:      sessionState{sessionOptions: &SessionOptions{Model: "session-model"}},
			}
			if workspaceModel != "" {
				state.workspaceModel.Overrides = map[string]workspace.ModelOverride{"default": {Model: workspaceModel}}
			}
			active := state.activeConfig()
			if active.Provider.Model != "session-model" {
				t.Fatalf("active model = %s", active.Provider.Model)
			}
			if len(value.Agents.Roles) != len(state.config.Agents.Roles) || state.config.Provider.Model != "global-model" {
				t.Fatal("session selection changed global settings")
			}
		})
	}
}
