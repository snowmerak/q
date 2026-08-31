package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workspace"
	"github.com/snowmerak/q/workspacememory"
)

func (m model) isStandaloneScreen(value screen) bool {
	return m.standalone && m.standaloneRoot == value
}

func runStandalone(initial model, root screen) (model, error) {
	initial.standalone = true
	initial.standaloneRoot = root
	final, err := tea.NewProgram(initial).Run()
	if result, ok := final.(model); ok {
		return result, err
	}
	return initial, err
}

// RunHelp opens the full help screen without loading configuration or workspace services.
func RunHelp(ctx context.Context) error {
	m := newModel(ctx, config.Store{}, nil)
	m.screen = screenHelp
	m.helpReturn = screenHelp
	m.refreshHelp(true)
	_, err := runStandalone(m, screenHelp)
	return err
}

// RunIgnore opens the current workspace's .qignore editor without starting Gateway or chat services.
func RunIgnore(ctx context.Context, store workspace.Store) error {
	m := newModel(ctx, config.Store{}, nil)
	m.workspaceStore = &store
	updated, _ := m.enterIgnore()
	m = updated.(model)
	if m.screen != screenIgnore {
		return errors.New(m.status)
	}
	_, err := runStandalone(m, screenIgnore)
	return err
}

func RunIgnoreDefault(ctx context.Context) error {
	store, err := workspace.DefaultStore()
	if err != nil {
		return err
	}
	if err := RunIgnore(ctx, store); err != nil {
		return fmt.Errorf("q ignore: %w", err)
	}
	return nil
}

type standaloneSkillRegistry struct {
	registry       *agentskills.Registry
	workspaceStore agentskills.RecordStore
	globalSkills   interface {
		ReloadSkills(context.Context) (qlibrary.SkillReloadResponse, error)
	}
	globalErr error
}

func (r *standaloneSkillRegistry) Skills() []agentskills.Skill       { return r.registry.Skills() }
func (r *standaloneSkillRegistry) SkillEntries() []agentskills.Skill { return r.registry.Entries() }
func (r *standaloneSkillRegistry) SkillIssues() []agentskills.Issue  { return r.registry.Issues() }
func (r *standaloneSkillRegistry) ReloadSkills() error {
	if err := r.registry.Reload(); err != nil {
		return err
	}
	ctx := context.Background()
	return errors.Join(r.syncWorkspaceSkills(ctx), r.reloadGlobalSkills(ctx))
}
func (r *standaloneSkillRegistry) InstallSkill(ctx context.Context, scope, repository string) (agentskills.Skill, error) {
	skill, err := r.registry.InstallGit(ctx, scope, repository)
	if err != nil {
		return agentskills.Skill{}, err
	}
	if err := r.reconcile(ctx, skill.Scope); err != nil {
		return agentskills.Skill{}, err
	}
	return skill, nil
}
func (r *standaloneSkillRegistry) UpdateSkill(ctx context.Context, identifier string) (agentskills.Skill, error) {
	skill, err := r.registry.UpdateGit(ctx, identifier)
	if err != nil {
		return agentskills.Skill{}, err
	}
	if err := r.reconcile(ctx, skill.Scope); err != nil {
		return agentskills.Skill{}, err
	}
	return skill, nil
}
func (r *standaloneSkillRegistry) RemoveSkill(ctx context.Context, identifier string) (agentskills.Skill, error) {
	skill, err := r.registry.RemoveGit(identifier)
	if err != nil {
		return agentskills.Skill{}, err
	}
	if err := r.reconcile(ctx, skill.Scope); err != nil {
		return agentskills.Skill{}, err
	}
	return skill, nil
}

func (r *standaloneSkillRegistry) reconcile(ctx context.Context, scope string) error {
	if scope == "global" {
		return r.reloadGlobalSkills(ctx)
	}
	return r.syncWorkspaceSkills(ctx)
}

func (r *standaloneSkillRegistry) syncWorkspaceSkills(ctx context.Context) error {
	if r.workspaceStore == nil {
		return nil
	}
	return r.registry.SyncRecordsForScopes(ctx, r.workspaceStore, "project")
}

func (r *standaloneSkillRegistry) reloadGlobalSkills(ctx context.Context) error {
	if r.globalSkills != nil {
		_, err := r.globalSkills.ReloadSkills(ctx)
		return err
	}
	return r.globalErr
}

// RunSkills opens global/workspace skill management and keeps both derived
// skill indexes synchronized for the current workspace.
func RunSkills(ctx context.Context, store workspace.Store) error {
	configStore, err := config.DefaultStore()
	if err != nil {
		return err
	}
	return runSkills(ctx, store, configStore)
}

func runSkills(ctx context.Context, store workspace.Store, configStore config.Store) error {
	lock, err := workspace.AcquireLock(store.Root, "q skills")
	if err != nil {
		return err
	}
	defer lock.Close()
	registry, err := agentskills.Discover(store.Root)
	if err != nil {
		return err
	}
	loaded, configErr := configStore.Load()
	if configErr != nil && !errors.Is(configErr, config.ErrNotFound) {
		return configErr
	}
	vectorConfig := sessionstore.VectorConfig{}
	if configErr == nil && loaded.Embedding.Model != "" {
		vectorConfig = sessionstore.VectorConfig{
			Model: loaded.Embedding.Model, Dimensions: loaded.Embedding.Dimensions,
		}
	}
	memoryRuntime, err := workspacememory.Ensure(ctx, configStore.Dir)
	if err != nil {
		return err
	}
	workspaceStore, err := memoryRuntime.Client().OpenWorkspace(ctx, store.Root, vectorConfig)
	if err != nil {
		return errors.Join(err, memoryRuntime.Close())
	}
	libraryRuntime, libraryErr := qlibrary.Ensure(ctx, configStore.Dir)
	registryRuntime := &standaloneSkillRegistry{
		registry: registry, workspaceStore: workspaceStore, globalErr: libraryErr,
	}
	if libraryRuntime != nil {
		registryRuntime.globalSkills = libraryRuntime.Client()
	}
	if err := registryRuntime.syncWorkspaceSkills(ctx); err != nil {
		return errors.Join(err, closeStandaloneSkillServices(workspaceStore, memoryRuntime, libraryRuntime))
	}
	m := newModel(ctx, config.Store{}, nil)
	m.workspaceStore = &store
	m.skillRegistry = registryRuntime
	updated, _ := m.enterSkills()
	m = updated.(model)
	if m.screen != screenSkills {
		return errors.Join(errors.New(m.status), closeStandaloneSkillServices(workspaceStore, memoryRuntime, libraryRuntime))
	}
	_, runErr := runStandalone(m, screenSkills)
	return errors.Join(runErr, closeStandaloneSkillServices(workspaceStore, memoryRuntime, libraryRuntime))
}

func closeStandaloneSkillServices(
	workspaceStore *workspacememory.Workspace,
	memoryRuntime *workspacememory.Runtime,
	libraryRuntime *qlibrary.Runtime,
) error {
	var libraryCloseErr error
	if libraryRuntime != nil {
		libraryCloseErr = libraryRuntime.Close()
	}
	return errors.Join(workspaceStore.Close(), libraryCloseErr, memoryRuntime.Close())
}

func RunSkillsDefault(ctx context.Context) error {
	store, err := workspace.DefaultStore()
	if err != nil {
		return err
	}
	if err := RunSkills(ctx, store); err != nil {
		return fmt.Errorf("q skills: %w", err)
	}
	return nil
}

// RunModel opens model selection using only personal config and q's managed Gateway.
func RunModel(ctx context.Context, store config.Store) error {
	runtimeContext, cancelRuntime := context.WithCancel(ctx)
	defer cancelRuntime()
	workspaceStore, err := workspace.DefaultStore()
	if err != nil {
		return err
	}

	loaded, configErr := store.Load()
	if configErr != nil && !errors.Is(configErr, config.ErrNotFound) {
		return configErr
	}
	manager, err := providerhost.NewManager(runtimeContext, providerhost.Store{Dir: store.Dir})
	if err != nil {
		return err
	}
	defer manager.Close()

	startupErr := manager.LoadAndStart(runtimeContext)
	if errors.Is(startupErr, providerhost.ErrNotFound) && configErr == nil && !loaded.Provider.Managed {
		legacy, legacyErr := providerhost.LegacyProvider(
			"default", loaded.Provider.BaseURL, loaded.Provider.APIKeyEnv, loaded.Provider.APIKey,
		)
		if legacyErr != nil {
			return legacyErr
		}
		if err := manager.Apply(runtimeContext, gateway.Config{Providers: []gateway.ProviderConfig{legacy}}); err != nil {
			return err
		}
		if !strings.HasPrefix(loaded.Provider.Model, "default/") {
			loaded.Provider.Model = "default/" + loaded.Provider.Model
		}
		loaded.UseManagedGateway()
		if err := store.Save(loaded); err != nil {
			return err
		}
		startupErr = nil
	}
	if startupErr != nil {
		if errors.Is(startupErr, providerhost.ErrNotFound) {
			return errors.New("no Gateway providers configured; run `q gateway config`")
		}
		return startupErr
	}
	if manager.Endpoint() == "" {
		return errors.New("managed Gateway is not running; run `q gateway config`")
	}
	if errors.Is(configErr, config.ErrNotFound) {
		loaded = config.Default()
		loaded.Provider.Model = "model-discovery"
		loaded.UseManagedGateway()
	} else if !loaded.Provider.Managed {
		loaded.UseManagedGateway()
	}

	factory := managedClientFactory(manager)
	configuredClient, err := factory(loaded)
	if err != nil {
		return err
	}
	models, err := configuredClient.ListModels(runtimeContext)
	if err != nil {
		_ = configuredClient.Close()
		return fmt.Errorf("load models: %w", err)
	}
	if len(models) == 0 {
		_ = configuredClient.Close()
		return errors.New("Gateway returned no models")
	}

	m := newManagedModel(runtimeContext, store, factory, manager)
	m.config = loaded
	m.client = configuredClient
	m.gatewayConfig = manager.Config()
	if err := attachStandaloneModelWorkspace(&m, workspaceStore); err != nil {
		_ = configuredClient.Close()
		return err
	}
	m.modelReturn = screenChat
	m.modelChooseTarget = true
	m.enterModelPicker(loaded, models)
	final, runErr := runStandalone(m, screenModels)
	cancelRuntime()
	clientToClose := chatClient(configuredClient)
	if final.client != nil {
		clientToClose = final.client
	}
	closeErr := clientToClose.Close()
	return errors.Join(runErr, closeErr)
}

func attachStandaloneModelWorkspace(m *model, store workspace.Store) error {
	m.workspaceStore = &store
	return m.restoreWorkspaceModel()
}

func RunModelDefault(ctx context.Context) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	if err := RunModel(ctx, store); err != nil {
		return fmt.Errorf("q model: %w", err)
	}
	return nil
}
