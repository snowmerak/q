package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workspace"
)

type fakeStandaloneSkillLibrary struct {
	reloads int
}

func (f *fakeStandaloneSkillLibrary) ReloadSkills(context.Context) (qlibrary.SkillReloadResponse, error) {
	f.reloads++
	return qlibrary.SkillReloadResponse{Active: 1}, nil
}

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

func TestStandaloneModelEmbeddingAssignmentBackfillsSkillsAndWorkspace(t *testing.T) {
	home := t.TempDir()
	store := config.Store{Dir: filepath.Join(home, ".q")}
	workspaceStore := workspace.Store{Root: t.TempDir()}
	skillDir := filepath.Join(home, ".agents", "skills", "animal-care")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(
		"---\nname: animal-care\ndescription: Care for cats.\n---\n\n# Animal care\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	value := config.Default()
	value.Embedding = config.EmbeddingConfig{Model: "embed-model", Dimensions: 3}
	m := newModel(context.Background(), store, nil)
	m.client = &extractionClient{}
	closeEmbedding, err := attachStandaloneModelEmbedding(context.Background(), &m, workspaceStore, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeEmbedding() })
	workspaceRecord, err := m.archiveSearch.Save(sessionstore.Record{
		Kind: sessionstore.KindSkill, Summary: "workspace-animal-care", Content: "Care for dogs.",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, command := m.Update(modelTargetConfiguredMsg{config: value, target: embeddingModelTarget})
	if command == nil {
		t.Fatal("standalone embedding assignment did not start indexing")
	}
	m = updated.(model)
	result, ok := command().(archiveEmbeddingConfiguredMsg)
	if !ok || result.err != nil || result.globalSkills != 1 || result.stats.Embedded != 1 {
		t.Fatalf("standalone embedding result = %#v", result)
	}
	reloaded, err := m.archiveSearch.Get(workspaceRecord.ID)
	if err != nil || reloaded.Embedding == nil || reloaded.Embedding.Model != "embed-model" {
		t.Fatalf("workspace embedding = %#v, err = %v", reloaded.Embedding, err)
	}
	workspaceHits, err := m.archiveSearch.Search(context.Background(), sessionstore.SearchOptions{
		Text: "feline", Filters: sessionstore.Filters{Kinds: []string{sessionstore.KindSkill}}, Limit: 1,
	})
	if err != nil || len(workspaceHits.Hits) != 1 || workspaceHits.Hits[0].VectorScore == 0 {
		t.Fatalf("workspace semantic search = %#v, err = %v", workspaceHits, err)
	}
	global, err := m.libraryClient.SearchSkills(context.Background(), qlibrary.SkillSearchRequest{Query: "feline"})
	if err != nil || len(global.Hits) != 1 || global.Hits[0].Title != "animal-care" {
		t.Fatalf("global skill search = %#v, err = %v", global, err)
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

func TestStandaloneSkillsReloadSynchronizesWorkspaceAndGlobalIndexes(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	directory := filepath.Join(root, ".agents", "skills", "workspace-skill")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: workspace-skill\ndescription: Indexed workspace instructions.\n---\n\n# Instructions\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := agentskills.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	workspaceStore, err := sessionstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	global := &fakeStandaloneSkillLibrary{}
	runtime := &standaloneSkillRegistry{
		registry: registry, workspaceStore: workspaceStore, globalSkills: global,
	}
	if err := runtime.ReloadSkills(); err != nil {
		t.Fatal(err)
	}
	result, err := workspaceStore.Search(context.Background(), sessionstore.SearchOptions{
		Filters: sessionstore.Filters{
			Kinds: []string{sessionstore.KindSkill}, Scopes: []string{"project"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Hits) != 1 || result.Hits[0].Record.Summary != "workspace-skill" {
		t.Fatalf("workspace skill index = %#v", result)
	}
	if global.reloads != 1 {
		t.Fatalf("global skill reloads = %d", global.reloads)
	}
}
