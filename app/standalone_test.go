package app

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func TestStandaloneRootScreensQuitOnEscape(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}

	modelScreen := newModel(context.Background(), store, nil)
	modelScreen.standalone = true
	modelScreen.standaloneRoot = screenModels
	modelScreen.screen = screenModels
	modelScreen.modelPickerStage = modelPickerTargets
	if _, command := modelScreen.updateModelTargetPicker(tea.KeyPressMsg{Code: tea.KeyEsc}); command == nil {
		t.Fatal("standalone model escape did not quit")
	}

	helpScreen := newModel(context.Background(), store, nil)
	helpScreen.standalone = true
	helpScreen.standaloneRoot = screenHelp
	helpScreen.screen = screenHelp
	if _, command := helpScreen.updateHelp(tea.KeyPressMsg{Code: tea.KeyEsc}); command == nil {
		t.Fatal("standalone help escape did not quit")
	}

	workspaceStore := workspace.Store{Root: t.TempDir()}
	ignoreScreen := newModel(context.Background(), store, nil)
	ignoreScreen.workspaceStore = &workspaceStore
	ignoreScreen.standalone = true
	ignoreScreen.standaloneRoot = screenIgnore
	updated, _ := ignoreScreen.enterIgnore()
	ignoreScreen = updated.(model)
	if _, command := ignoreScreen.updateIgnore(tea.KeyPressMsg{Code: tea.KeyEsc}); command == nil {
		t.Fatal("standalone ignore escape did not quit")
	}

	skillsScreen := newModel(context.Background(), store, nil)
	skillsScreen.standalone = true
	skillsScreen.standaloneRoot = screenSkills
	skillsScreen.screen = screenSkills
	if _, command := skillsScreen.updateSkills(tea.KeyPressMsg{Code: tea.KeyEsc}); command == nil {
		t.Fatal("standalone skills escape did not quit")
	}

	lspScreen := newModel(context.Background(), store, nil)
	lspScreen.standalone = true
	lspScreen.standaloneRoot = screenLSP
	lspScreen.screen = screenLSP
	if _, command := lspScreen.updateLSP(tea.KeyPressMsg{Code: tea.KeyEsc}); command == nil {
		t.Fatal("standalone LSP escape did not quit")
	}
}

func TestStandaloneHelpOpenedFromAnotherScreenReturnsToThatScreen(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.standalone = true
	m.standaloneRoot = screenModels
	m.screen = screenModels
	updated, _ := m.enterHelp()
	m = updated.(model)
	updated, _ = m.updateHelp(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	if m.screen != screenModels {
		t.Fatalf("help returned to screen %v", m.screen)
	}
}

func TestStandaloneDefaultModelSaveStaysInModelSettings(t *testing.T) {
	oldClient := &fakeClient{}
	newClient := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.standalone = true
	m.standaloneRoot = screenModels
	m.screen = screenModels
	m.client = oldClient
	value := config.Default()
	value.Provider.Model = "local/new-model"

	updated, _ := m.Update(configuredMsg{config: value, client: newClient, preserveHistory: true})
	m = updated.(model)
	if m.screen != screenModels || m.modelPickerStage != modelPickerTargets || m.client != newClient || !oldClient.closed {
		t.Fatalf("standalone model save state = screen %v, stage %v, client %#v, old closed %v", m.screen, m.modelPickerStage, m.client, oldClient.closed)
	}
	if !strings.Contains(m.status, "local/new-model") {
		t.Fatalf("standalone model status = %q", m.status)
	}
}

func TestStandaloneModelAttachesCurrentWorkspace(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	if err := attachStandaloneModelWorkspace(&m, workspace.Store{Root: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if got := m.workspaceModelSummary(defaultModelTarget); got == "unavailable" {
		t.Fatalf("standalone workspace model summary = %q", got)
	}
}

func TestStandaloneSkillsUsesLightweightRegistry(t *testing.T) {
	root := t.TempDir()
	registry, err := agentskills.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.skillRegistry = &standaloneSkillRegistry{registry: registry}
	updated, _ := m.enterSkills()
	m = updated.(model)
	if m.screen != screenSkills || m.toolRuntime != nil || m.currentSkillRuntime() == nil {
		t.Fatalf("standalone skills state = screen %v, tools %#v, skills %#v", m.screen, m.toolRuntime, m.currentSkillRuntime())
	}
}
