package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	qlsp "github.com/snowmerak/q/lsp"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
	"github.com/snowmerak/q/workspacememory"
)

const maximumIntegrationRequestSize = 1 << 20
const redactedConnectionSecret = "********"

type integrationService struct {
	mu      *sync.Mutex
	main    config.Store
	mcp     mcpconfig.Store
	runtime embeddingRuntime
}

type mcpSettingsResponse struct {
	ConfigPath string           `json:"config_path"`
	Config     mcpconfig.Config `json:"config"`
	Roles      []string         `json:"roles"`
}

type lspSettingsResponse struct {
	ConfigPath    string               `json:"config_path"`
	WorkspacePath string               `json:"workspace_path"`
	WorkspaceRoot string               `json:"workspace_root"`
	Global        qlsp.GlobalConfig    `json:"global"`
	Workspace     qlsp.WorkspaceConfig `json:"workspace"`
}

type lspSettingsUpdate struct {
	WorkspaceRoot string               `json:"workspace_root"`
	Global        qlsp.GlobalConfig    `json:"global"`
	Workspace     qlsp.WorkspaceConfig `json:"workspace"`
}

type lspDiscoveryResponse struct {
	Roots   []qlsp.RootConfig       `json:"roots"`
	Servers []qlsp.DiscoveredServer `json:"servers"`
}

type skillSettingsResponse struct {
	WorkspaceRoot string              `json:"workspace_root"`
	Skills        []studioSkill       `json:"skills"`
	Issues        []agentskills.Issue `json:"issues"`
}

type studioSkill struct {
	agentskills.Skill
	Active  bool `json:"active"`
	Managed bool `json:"managed"`
}

type skillOperationRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
	Scope         string `json:"scope,omitempty"`
	Repository    string `json:"repository,omitempty"`
	ID            string `json:"id,omitempty"`
}

type agentSettingsResponse struct {
	WorkspaceRoot string                                  `json:"workspace_root"`
	ConfigPath    string                                  `json:"config_path"`
	Connections   map[string]config.AgentConnectionConfig `json:"connections"`
	Bindings      map[string]string                       `json:"bindings"`
	ExternalRoles []string                                `json:"external_roles"`
	NativeRoles   []string                                `json:"native_roles"`
	Builtins      []studioAgentDefinition                 `json:"builtins"`
	Profiles      []studioProfileEntry                    `json:"profiles"`
	ToolNames     []string                                `json:"tool_names"`
}

type studioAgentDefinition struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Kind             string   `json:"kind"`
	Role             string   `json:"role"`
	MutatesWorkspace bool     `json:"mutates_workspace"`
	SystemPrompt     string   `json:"system_prompt"`
	Tools            []string `json:"tools"`
	Delegates        []string `json:"delegates"`
}

type studioProfileEntry struct {
	Profile  subagent.Profile `json:"profile"`
	Scope    string           `json:"scope"`
	Path     string           `json:"path"`
	Revision string           `json:"revision"`
	Shadowed bool             `json:"shadowed"`
	Error    string           `json:"error,omitempty"`
}

type agentConnectionsUpdate struct {
	WorkspaceRoot string                                  `json:"workspace_root"`
	Connections   map[string]config.AgentConnectionConfig `json:"connections"`
	Bindings      map[string]string                       `json:"bindings"`
}

type agentProbeRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
	ConnectionID  string `json:"connection_id"`
}

type profileUpdate struct {
	WorkspaceRoot string           `json:"workspace_root"`
	Scope         string           `json:"scope"`
	OriginalScope string           `json:"original_scope,omitempty"`
	Revision      string           `json:"revision,omitempty"`
	Profile       subagent.Profile `json:"profile"`
}

type ignoreSettingsResponse struct {
	WorkspaceRoot string `json:"workspace_root"`
	Path          string `json:"path"`
	Content       string `json:"content"`
	Revision      string `json:"revision"`
}

type ignoreUpdate struct {
	WorkspaceRoot string `json:"workspace_root"`
	Content       string `json:"content"`
	Revision      string `json:"revision"`
}

func newIntegrationService(store config.Store, runner sessionRunner, shared ...*sync.Mutex) *integrationService {
	mutex := &sync.Mutex{}
	if len(shared) > 0 && shared[0] != nil {
		mutex = shared[0]
	}
	service := &integrationService{main: store, mcp: mcpconfig.Store{Dir: store.Dir}, mu: mutex}
	if runtime, ok := runner.(embeddingRuntime); ok {
		service.runtime = runtime
	}
	return service
}

func (service *integrationService) serveMCP(writer http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if request.Method == http.MethodGet {
		value, err := service.mcp.LoadOrDefault()
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, mcpSettingsResponse{ConfigPath: service.mcp.Path(), Config: value, Roles: mcpconfig.RoleIDs()})
		return
	}
	if request.Method != http.MethodPut {
		writer.Header().Set("Allow", "GET, PUT")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var value mcpconfig.Config
	if err := decodeIntegrationRequest(writer, request, &value); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if err := service.mcp.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	value, _ = service.mcp.LoadOrDefault()
	writeJSON(writer, http.StatusOK, mcpSettingsResponse{ConfigPath: service.mcp.Path(), Config: value, Roles: mcpconfig.RoleIDs()})
}

func (service *integrationService) serveLSP(writer http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if request.Method == http.MethodGet {
		root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		service.writeLSP(writer, root)
		return
	}
	if request.Method != http.MethodPut {
		writer.Header().Set("Allow", "GET, PUT")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var input lspSettingsUpdate
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	global, err := input.Global.Normalized()
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	value.LSP = global
	if err := value.Validate(); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if err := input.Workspace.Validate(global); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if err := (workspace.Store{Root: root}).SaveLSP(input.Workspace, global); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, fmt.Errorf("global LSP settings saved, but workspace settings failed: %w", err))
		return
	}
	service.writeLSP(writer, root)
}

func (service *integrationService) writeLSP(writer http.ResponseWriter, root string) {
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	store := workspace.Store{Root: root}
	workspaceConfig, err := store.LoadLSP()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, lspSettingsResponse{
		ConfigPath: service.main.Path(), WorkspacePath: store.LSPPath(), WorkspaceRoot: root,
		Global: value.LSP, Workspace: workspaceConfig,
	})
}

func (service *integrationService) serveLSPDiscover(writer http.ResponseWriter, request *http.Request) {
	var input workspaceRequest
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	roots, err := (workspace.Store{Root: root}).DiscoverLSPRootsContext(request.Context())
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	languages := make([]string, 0, len(roots))
	for _, root := range roots {
		languages = append(languages, root.Language)
	}
	writeJSON(writer, http.StatusOK, lspDiscoveryResponse{Roots: roots, Servers: qlsp.DiscoverServers(languages)})
}

func (service *integrationService) serveSkills(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		service.writeSkills(writer, root)
	case http.MethodPost:
		var input skillOperationRequest
		if err := decodeIntegrationRequest(writer, request, &input); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		root, registry, err := service.skillRegistry(input.WorkspaceRoot)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		if _, err := registry.InstallGit(request.Context(), input.Scope, input.Repository); err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		if err := service.reindexSkills(request.Context(), root, registry); err != nil {
			writeAPIError(writer, http.StatusBadGateway, fmt.Errorf("skill installed, but reindexing failed: %w", err))
			return
		}
		service.writeSkills(writer, root)
	default:
		writer.Header().Set("Allow", "GET, POST")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (service *integrationService) serveSkillItem(writer http.ResponseWriter, request *http.Request) {
	var input skillOperationRequest
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, registry, err := service.skillRegistry(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	id := request.PathValue("skill")
	switch request.Method {
	case http.MethodPost:
		_, err = registry.UpdateGit(request.Context(), id)
	case http.MethodDelete:
		_, err = registry.RemoveGit(id)
	default:
		writer.Header().Set("Allow", "POST, DELETE")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if err := service.reindexSkills(request.Context(), root, registry); err != nil {
		writeAPIError(writer, http.StatusBadGateway, fmt.Errorf("skill changed, but reindexing failed: %w", err))
		return
	}
	service.writeSkills(writer, root)
}

func (service *integrationService) serveSkillReindex(writer http.ResponseWriter, request *http.Request) {
	var input skillOperationRequest
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, registry, err := service.skillRegistry(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if err := service.reindexSkills(request.Context(), root, registry); err != nil {
		writeAPIError(writer, http.StatusBadGateway, err)
		return
	}
	service.writeSkills(writer, root)
}

func (service *integrationService) skillRegistry(rawRoot string) (string, *agentskills.Registry, error) {
	root, err := canonicalWorkspaceDirectory(rawRoot)
	if err != nil {
		return "", nil, err
	}
	registry, err := agentskills.Discover(root)
	return root, registry, err
}

func (service *integrationService) writeSkills(writer http.ResponseWriter, root string) {
	registry, err := agentskills.Discover(root)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	active := make(map[string]bool)
	for _, skill := range registry.Skills() {
		active[skill.ID] = true
	}
	entries := registry.Entries()
	result := make([]studioSkill, 0, len(entries))
	for _, skill := range entries {
		managed := skill.Source == agentskills.SourceUserQ || skill.Source == agentskills.SourceProjectQ
		if managed {
			_, err = os.Stat(filepath.Join(skill.Directory, ".git"))
			managed = err == nil
		}
		result = append(result, studioSkill{Skill: skill, Active: active[skill.ID], Managed: managed})
	}
	writeJSON(writer, http.StatusOK, skillSettingsResponse{WorkspaceRoot: root, Skills: result, Issues: registry.Issues()})
}

func (service *integrationService) reindexSkills(ctx context.Context, root string, registry *agentskills.Registry) (returnErr error) {
	service.mu.Lock()
	value, err := service.main.Load()
	service.mu.Unlock()
	if err != nil {
		return err
	}
	vector := sessionstore.VectorConfig{}
	if value.Embedding.Model != "" {
		vector = sessionstore.VectorConfig{Model: value.Embedding.Model, Dimensions: value.Embedding.Dimensions}
	}
	memoryRuntime, err := workspacememory.Ensure(ctx, service.main.Dir)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, memoryRuntime.Close()) }()
	memoryStore, err := memoryRuntime.Client().OpenWorkspace(ctx, root, vector)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, memoryStore.Close()) }()
	if err := registry.SyncRecordsForScopes(ctx, memoryStore, "project"); err != nil {
		return err
	}
	libraryRuntime, err := qlibrary.Ensure(ctx, service.main.Dir)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, libraryRuntime.Close()) }()
	if _, err := libraryRuntime.Client().ReloadSkills(ctx); err != nil {
		return err
	}
	if service.runtime != nil {
		return service.runtime.SyncEmbeddings(ctx, root, value)
	}
	return nil
}

func (service *integrationService) serveAgents(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		service.writeAgents(writer, root)
		return
	}
	if request.Method != http.MethodPut {
		writer.Header().Set("Allow", "GET, PUT")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var input agentConnectionsUpdate
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if input.Connections == nil {
		input.Connections = make(map[string]config.AgentConnectionConfig)
	}
	if value.Agents.Roles == nil {
		value.Agents.Roles = make(map[string]config.AgentConfig)
	}
	for id, connection := range input.Connections {
		previous := value.Agents.Connections[id]
		resolved := make(map[string]string, len(connection.Env))
		for name, secret := range connection.Env {
			if secret == redactedConnectionSecret {
				secret = previous.Env[name]
			}
			resolved[name] = secret
		}
		connection.Env = resolved
		input.Connections[id] = connection
	}
	value.Agents.Connections = input.Connections
	for _, role := range config.ExternalAgentRoles() {
		assignment := value.Agents.Roles[role]
		assignment.Agent = strings.TrimSpace(input.Bindings[role])
		if assignment.Agent == "" {
			delete(value.Agents.Roles, role)
		} else {
			value.Agents.Roles[role] = assignment
		}
	}
	if err := validateAgentConnectionReferences(value, profileStore(service.main, root)); err != nil {
		writeAPIError(writer, http.StatusConflict, err)
		return
	}
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeAgentsUnlocked(writer, root, value)
}

func (service *integrationService) serveAgentProbe(writer http.ResponseWriter, request *http.Request) {
	var input agentProbeRequest
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	value, err := service.main.Load()
	service.mu.Unlock()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	connection, found := value.Agents.Connections[input.ConnectionID]
	if !found {
		writeAPIError(writer, http.StatusNotFound, errors.New("agent connection does not exist"))
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 45*time.Second)
	defer cancel()
	if err := app.ProbeACPAgentConnection(ctx, root, input.ConnectionID, connection); err != nil {
		writeAPIError(writer, http.StatusBadGateway, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "connected"})
}

func (service *integrationService) serveProfiles(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost && request.Method != http.MethodPut {
		writer.Header().Set("Allow", "POST, PUT")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var input profileUpdate
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	store := profileStore(service.main, root)
	var original *subagent.ProfileEntry
	if request.Method == http.MethodPut {
		originalScope := input.OriginalScope
		if originalScope == "" {
			originalScope = input.Scope
		}
		entry, found := findProfileEntry(store.List(), originalScope, input.Profile.Name)
		if !found {
			writeAPIError(writer, http.StatusNotFound, errors.New("subagent profile does not exist"))
			return
		}
		if revision(entry.Raw) != input.Revision {
			writeAPIError(writer, http.StatusConflict, errors.New("subagent profile changed externally; reload before saving"))
			return
		}
		original = &entry
	}
	if input.Profile.EffectiveKind() == subagent.AgentKindExternal {
		if connection, found := value.Agents.Connections[input.Profile.Agent]; !found || connection.Disabled {
			writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("external subagent requires an enabled ACP connection"))
			return
		}
	} else if !value.HasNativeRole(input.Profile.Role) {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("inner subagent references an unavailable model role"))
		return
	}
	if err := validateProfileGraph(store, input.Profile, input.Scope, original); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if err := store.Save(input.Profile, input.Scope, original); err != nil {
		writeAPIError(writer, http.StatusConflict, err)
		return
	}
	service.writeAgentsUnlocked(writer, root, value)
}

func (service *integrationService) serveProfileDelete(writer http.ResponseWriter, request *http.Request) {
	var input profileUpdate
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	store := profileStore(service.main, root)
	entry, found := findProfileEntry(store.List(), input.Scope, request.PathValue("profile"))
	if !found {
		writeAPIError(writer, http.StatusNotFound, errors.New("subagent profile does not exist"))
		return
	}
	if revision(entry.Raw) != input.Revision {
		writeAPIError(writer, http.StatusConflict, errors.New("subagent profile changed externally; reload before deleting"))
		return
	}
	id := subagent.CanonicalProfileID(entry.Scope, entry.Profile.Name)
	var references []string
	for _, candidate := range store.List() {
		if candidate.Err == nil && containsString(candidate.Profile.Delegates, id) {
			references = append(references, subagent.CanonicalProfileID(candidate.Scope, candidate.Profile.Name))
		}
	}
	if len(references) > 0 {
		sort.Strings(references)
		writeAPIError(writer, http.StatusConflict, fmt.Errorf("subagent profile is delegated by %s", strings.Join(references, ", ")))
		return
	}
	if err := store.Delete(entry); err != nil {
		writeAPIError(writer, http.StatusConflict, err)
		return
	}
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	service.writeAgentsUnlocked(writer, root, value)
}

func (service *integrationService) writeAgents(writer http.ResponseWriter, root string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	service.writeAgentsUnlocked(writer, root, value)
}

func (service *integrationService) writeAgentsUnlocked(writer http.ResponseWriter, root string, value config.Config) {
	if value.Agents.Connections == nil {
		value.Agents.Connections = make(map[string]config.AgentConnectionConfig)
	}
	connections := make(map[string]config.AgentConnectionConfig, len(value.Agents.Connections))
	for id, connection := range value.Agents.Connections {
		connection.Args = append([]string(nil), connection.Args...)
		connection.Env = make(map[string]string, len(value.Agents.Connections[id].Env))
		for name := range value.Agents.Connections[id].Env {
			connection.Env[name] = redactedConnectionSecret
		}
		connections[id] = connection
	}
	bindings := make(map[string]string)
	for _, role := range config.ExternalAgentRoles() {
		bindings[role] = value.Agents.Roles[role].Agent
	}
	builtins := make([]studioAgentDefinition, 0)
	toolSet := make(map[string]struct{})
	for _, definition := range subagent.PublicAgentDefinitions() {
		builtins = append(builtins, studioAgentDefinition{
			Name: definition.Info.Name, Description: definition.Info.Description, Kind: definition.Info.Kind,
			Role: definition.Info.Role, MutatesWorkspace: definition.Info.MutatesWorkspace,
			SystemPrompt: definition.SystemPrompt, Tools: definition.Tools, Delegates: definition.Delegates,
		})
		for _, name := range definition.Tools {
			if subagent.CustomToolAllowed(name) {
				toolSet[name] = struct{}{}
			}
		}
	}
	tools := make([]string, 0, len(toolSet))
	for name := range toolSet {
		tools = append(tools, name)
	}
	sort.Strings(tools)
	entries := profileStore(service.main, root).List()
	profiles := make([]studioProfileEntry, 0, len(entries))
	for _, entry := range entries {
		item := studioProfileEntry{Profile: entry.Profile, Scope: entry.Scope, Path: entry.Path, Revision: revision(entry.Raw), Shadowed: entry.Shadowed}
		if entry.Err != nil {
			item.Error = entry.Err.Error()
		}
		profiles = append(profiles, item)
	}
	writeJSON(writer, http.StatusOK, agentSettingsResponse{
		WorkspaceRoot: root, ConfigPath: service.main.Path(), Connections: connections,
		Bindings: bindings, ExternalRoles: config.ExternalAgentRoles(), NativeRoles: value.NativeRoles(),
		Builtins: builtins, Profiles: profiles, ToolNames: tools,
	})
}

func (service *integrationService) serveIgnore(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		service.writeIgnore(writer, root)
		return
	}
	if request.Method != http.MethodPut {
		writer.Header().Set("Allow", "GET, PUT")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var input ignoreUpdate
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	store := workspace.Store{Root: root}
	current, err := store.LoadIgnore()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if revision([]byte(current)) != input.Revision {
		writeAPIError(writer, http.StatusConflict, errors.New(".qignore changed externally; reload before saving"))
		return
	}
	if err := store.SaveIgnore(input.Content); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeIgnoreUnlocked(writer, root)
}

func (service *integrationService) writeIgnore(writer http.ResponseWriter, root string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.writeIgnoreUnlocked(writer, root)
}

func (service *integrationService) writeIgnoreUnlocked(writer http.ResponseWriter, root string) {
	store := workspace.Store{Root: root}
	content, err := store.LoadIgnore()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, ignoreSettingsResponse{WorkspaceRoot: root, Path: store.IgnorePath(), Content: content, Revision: revision([]byte(content))})
}

func profileStore(store config.Store, root string) subagent.ProfileStore {
	profiles := subagent.ProfileStore{Global: filepath.Join(store.Dir, "subagents")}
	if root != "" {
		profiles.Workspace = filepath.Join(root, workspace.DirectoryName, "subagents")
	}
	return profiles
}

func findProfileEntry(entries []subagent.ProfileEntry, scope, name string) (subagent.ProfileEntry, bool) {
	for _, entry := range entries {
		if entry.Scope == scope && entry.Profile.Name == name {
			return entry, true
		}
	}
	return subagent.ProfileEntry{}, false
}

func validateProfileGraph(store subagent.ProfileStore, candidate subagent.Profile, scope string, original *subagent.ProfileEntry) error {
	definitions := subagent.PublicAgentDefinitions()
	replaced := false
	for _, entry := range store.List() {
		if entry.Err != nil {
			return fmt.Errorf("subagent profile %s: %w", entry.Path, entry.Err)
		}
		if original != nil && entry.Path == original.Path {
			entry.Profile = candidate
			entry.Scope = scope
			replaced = true
		}
		definition, err := subagent.DefinitionForProfile(entry)
		if err != nil {
			return err
		}
		definitions = append(definitions, definition)
	}
	if !replaced {
		definition, err := subagent.DefinitionForProfile(subagent.ProfileEntry{Profile: candidate, Scope: scope})
		if err != nil {
			return err
		}
		definitions = append(definitions, definition)
	}
	_, err := subagent.NewRegistry(definitions)
	return err
}

func validateAgentConnectionReferences(value config.Config, profiles subagent.ProfileStore) error {
	for _, entry := range profiles.List() {
		if entry.Err != nil {
			continue
		}
		if entry.Profile.EffectiveKind() != subagent.AgentKindExternal {
			continue
		}
		connection, found := value.Agents.Connections[entry.Profile.Agent]
		if !found {
			return fmt.Errorf("connection %q is used by %s", entry.Profile.Agent, subagent.CanonicalProfileID(entry.Scope, entry.Profile.Name))
		}
		if connection.Disabled {
			continue
		}
	}
	return value.Validate()
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func revision(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func decodeIntegrationRequest(writer http.ResponseWriter, request *http.Request, output any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumIntegrationRequestSize)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode integration settings: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode integration settings: multiple JSON values")
		}
		return fmt.Errorf("decode integration settings: %w", err)
	}
	return nil
}
