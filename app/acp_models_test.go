package app

import (
	"testing"

	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

func TestACPModelOptionsRespectGroupsAndSessionIdentity(t *testing.T) {
	groups := acp.SessionConfigSelectOptionsGrouped{{Group: "fast", Name: "Fast models", Options: []acp.SessionConfigSelectOption{{Value: "one", Name: "One"}}}, {Group: "deep", Name: "Deep models", Options: []acp.SessionConfigSelectOption{{Value: "two", Name: "Two"}}}}
	options := []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id: "server-model", Type: "select", Category: new(acp.SessionConfigOptionCategoryModel), CurrentValue: "one", Options: acp.SessionConfigSelectOptions{Grouped: &groups},
	}}}
	remote := &acpRemoteClient{sessionID: "session", configOptions: options}
	settings := remote.modelSettings()
	if !settings.Supported || settings.CurrentModel != "one" || len(settings.Models) != 2 || settings.Models[1].Group != "Deep models" {
		t.Fatalf("model settings = %+v", settings)
	}
	if err := remote.SessionUpdate(t.Context(), acp.SessionNotification{SessionId: "other-session", Update: acp.SessionUpdate{ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{}}}); err != nil {
		t.Fatal(err)
	}
	if !remote.modelSettings().Supported {
		t.Fatal("another session replaced the model options")
	}
	if err := remote.SessionUpdate(t.Context(), acp.SessionNotification{SessionId: "session", Update: acp.SessionUpdate{ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{}}}); err != nil {
		t.Fatal(err)
	}
	if remote.modelSettings().Supported {
		t.Fatal("model options removal was ignored")
	}
}
