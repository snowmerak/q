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
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/workspace"
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
	registry *agentskills.Registry
}

func (r *standaloneSkillRegistry) Skills() []agentskills.Skill       { return r.registry.Skills() }
func (r *standaloneSkillRegistry) SkillEntries() []agentskills.Skill { return r.registry.Entries() }
func (r *standaloneSkillRegistry) SkillIssues() []agentskills.Issue  { return r.registry.Issues() }
func (r *standaloneSkillRegistry) ReloadSkills() error               { return r.registry.Reload() }
func (r *standaloneSkillRegistry) InstallSkill(ctx context.Context, scope, repository string) (agentskills.Skill, error) {
	return r.registry.InstallGit(ctx, scope, repository)
}
func (r *standaloneSkillRegistry) UpdateSkill(ctx context.Context, identifier string) (agentskills.Skill, error) {
	return r.registry.UpdateGit(ctx, identifier)
}
func (r *standaloneSkillRegistry) RemoveSkill(_ context.Context, identifier string) (agentskills.Skill, error) {
	return r.registry.RemoveGit(identifier)
}

// RunSkills opens lightweight global/project skill management for the current workspace.
func RunSkills(ctx context.Context, store workspace.Store) error {
	lock, err := workspace.AcquireLock(store.Root, "q skills")
	if err != nil {
		return err
	}
	defer lock.Close()
	registry, err := agentskills.Discover(store.Root)
	if err != nil {
		return err
	}
	m := newModel(ctx, config.Store{}, nil)
	m.workspaceStore = &store
	m.skillRegistry = &standaloneSkillRegistry{registry: registry}
	updated, _ := m.enterSkills()
	m = updated.(model)
	if m.screen != screenSkills {
		return errors.New(m.status)
	}
	_, err = runStandalone(m, screenSkills)
	return err
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
