package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/subagent"
	acp "github.com/snowmerak/q/third_party/acp-go-sdk"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
	"gopkg.in/yaml.v3"
)

// Custom profiles select from the normal session catalog, including its connected
// MCP tools. Their model role does not apply a built-in runner's tool filter.
func (m model) customTools() agentToolRuntime {
	return customToolsFor(m.toolRuntime)
}

func customToolsFor(base agentToolRuntime) agentToolRuntime {
	if catalog, ok := base.(interface{ CustomTools() []client.Tool }); ok {
		return customCatalogRuntime{agentToolRuntime: base, catalog: catalog}
	}
	return ScopeTools(base, mcpconfig.RoleDefault)
}

type customCatalogRuntime struct {
	agentToolRuntime
	catalog interface{ CustomTools() []client.Tool }
}

func (r customCatalogRuntime) Tools() []client.Tool { return r.catalog.CustomTools() }

func (r customCatalogRuntime) UnwrapToolRuntime() agentToolRuntime { return r.agentToolRuntime }

func (r customCatalogRuntime) SearchSkillHints(ctx context.Context, query string, limit int) (qtools.SkillHintSearchResult, error) {
	if !toolAvailable(r, "search_skills") {
		return qtools.SkillHintSearchResult{}, fmt.Errorf("agent skills search is unavailable in the custom tool catalog")
	}
	searcher, ok := r.agentToolRuntime.(skillHintSearcher)
	if !ok {
		return qtools.SkillHintSearchResult{}, fmt.Errorf("agent skills hint search is unavailable")
	}
	return searcher.SearchSkillHints(ctx, query, limit)
}

func (m model) customStore() subagent.ProfileStore {
	root := ""
	if m.workspaceStore != nil {
		root = m.workspaceStore.Root
	}
	return m.customStoreAt(root)
}

func (m model) customStoreAt(workspaceStateRoot string) subagent.ProfileStore {
	s := subagent.ProfileStore{Global: filepath.Join(filepath.Dir(m.store.Path()), "subagents")}
	if workspaceStateRoot != "" {
		s.Workspace = filepath.Join(workspaceStateRoot, ".q", "subagents")
	}
	return s
}
func customCommand(command string) bool {
	return command == "/subagents" || strings.HasPrefix(command, "/subagents ") || command == "/subagent" || strings.HasPrefix(command, "/subagent ")
}
func (m model) resolvePublicAgent(name string) (subagent.AgentDefinition, error) {
	name = strings.TrimSpace(name)
	registry, err := buildSubagentRegistry(m.customStore())
	if err != nil {
		return subagent.AgentDefinition{}, err
	}
	if definition, found := registry.Get(name); found {
		return definition, nil
	}
	entry, err := m.customStore().Get(name)
	if err != nil {
		return subagent.AgentDefinition{}, err
	}
	return definitionForStoredProfile(entry)
}

func (m model) customInfo(command string) string {
	if command == "/subagents" || command == "/subagents list" {
		var b strings.Builder
		b.WriteString("Subagents\n")
		registry, err := buildSubagentRegistry(m.customStore())
		if err != nil {
			return err.Error()
		}
		for _, info := range registry.List() {
			access := "read-only"
			if info.MutatesWorkspace {
				access = "mutates workspace"
			}
			if info.Kind == subagent.AgentKindExternal {
				definition, _ := registry.Get(info.Name)
				if connection, _, available := externalDefinitionConnection(m.activeConfig(), definition); available {
					access += " · ACP " + connection
				} else {
					access += " · unavailable"
				}
			}
			definition, _ := registry.Get(info.Name)
			target := info.Role
			if info.Kind == subagent.AgentKindExternal {
				target = "ACP " + definition.Connection
			}
			fmt.Fprintf(&b, "%s · %s · %s · %s · %s\n  %s\n", info.Name, info.Kind, target, info.Source, access, info.Description)
			if len(definition.Delegates) > 0 {
				fmt.Fprintf(&b, "  delegates: %s\n", strings.Join(definition.Delegates, ", "))
			}
		}
		for _, entry := range m.customStore().List() {
			if entry.Err != nil {
				fmt.Fprintf(&b, "%s: %v\n", entry.Path, entry.Err)
			}
		}
		return b.String()
	}
	if after, ok := strings.CutPrefix(command, "/subagents show "); ok {
		name := strings.TrimSpace(after)
		definition, err := m.resolvePublicAgent(name)
		if err != nil {
			return err.Error()
		}
		if definition.Info.Source == "builtin" {
			connection, _, _ := externalDefinitionConnection(m.activeConfig(), definition)
			raw, _ := yaml.Marshal(map[string]any{
				"name": definition.Info.Name, "description": definition.Info.Description,
				"kind": definition.Info.Kind, "role": definition.Info.Role, "mutates_workspace": definition.Info.MutatesWorkspace,
				"agent": connection, "system_prompt": definition.SystemPrompt,
				"tools": definition.Tools, "delegates": definition.Delegates,
			})
			state := "editable profile"
			if definition.Info.Source == "builtin" {
				state = "builtin"
				if definition.Info.Kind == subagent.AgentKindExternal {
					state += " (ACP binding editable)"
				} else {
					state += " (fixed)"
				}
			}
			return state + "\n" + string(raw)
		}
		profileName := strings.TrimPrefix(definition.Info.Name, definition.Info.Source+"/")
		var entry subagent.ProfileEntry
		found := false
		for _, candidate := range m.customStore().List() {
			if candidate.Scope == definition.Info.Source && candidate.Profile.Name == profileName {
				entry, found = candidate, true
				break
			}
		}
		if !found {
			return fmt.Sprintf("unknown subagent %q", name)
		}
		if entry.Err != nil {
			return entry.Err.Error()
		}
		raw, _ := yaml.Marshal(entry.Profile)
		return entry.Path + "\n" + string(raw)
	}
	return "Usage: /subagents list | /subagents show <name>"
}
func (m model) startCustom(command string) (tea.Model, tea.Cmd) {
	if command == "/subagents" {
		return m.enterCustom()
	}
	if !strings.HasPrefix(command, "/subagent ") {
		m.input.Reset()
		m.custom.detail = m.customInfo(command)
		m.custom.detailOffset = 0
		m.status = ""
		m.screen = screenCustom
		return m, nil
	}
	m.input.Reset()
	m.status = directDelegationRetired
	return m, nil
}
func (a *acpAgent) runACPCustom(ctx context.Context, command string) (acp.PromptResponse, error) {
	return acp.PromptResponse{}, fmt.Errorf("%s", directDelegationRetired)
}

// RunSubagents opens subagent profiles, external bindings, and ACP connection
// management without starting the chat runtime.
func RunSubagents(ctx context.Context, store config.Store, workspaceStore workspace.Store) error {
	value, err := store.Load()
	if err != nil {
		return err
	}
	m := newModel(ctx, store, nil)
	m.config = value
	m.workspaceStore = &workspaceStore
	updated, _ := m.enterCustom()
	m = updated.(model)
	if m.screen != screenCustom {
		return fmt.Errorf("open subagents: %s", m.status)
	}
	_, err = runStandalone(m, screenCustom)
	return err
}

func RunSubagentsDefault(ctx context.Context) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	workspaceStore, err := workspace.DefaultStore()
	if err != nil {
		return err
	}
	if err := RunSubagents(ctx, store, workspaceStore); err != nil {
		return fmt.Errorf("q subagents: %w", err)
	}
	return nil
}
