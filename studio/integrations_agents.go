package studio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

const redactedConnectionSecret = "********"

type agentSettingsResponse struct {
	WorkspaceRoot string                                  `json:"workspace_root"`
	ConfigPath    string                                  `json:"config_path"`
	DefaultModel  string                                  `json:"default_model"`
	Connections   map[string]config.AgentConnectionConfig `json:"connections"`
	Bindings      map[string]string                       `json:"bindings"`
	ExternalRoles []string                                `json:"external_roles"`
	NativeRoles   []string                                `json:"native_roles"`
	Builtins      []studioAgentDefinition                 `json:"builtins"`
	Profiles      []studioProfileEntry                    `json:"profiles"`
	ToolNames     []string                                `json:"tool_names"`
}

type studioAgentDefinition struct {
	Name             string                 `json:"name"`
	Description      string                 `json:"description"`
	Source           string                 `json:"source"`
	Kind             string                 `json:"kind"`
	Role             string                 `json:"role"`
	Connection       string                 `json:"connection,omitempty"`
	Target           *config.SubagentTarget `json:"target,omitempty"`
	ExternalOnly     bool                   `json:"external_only,omitempty"`
	Available        bool                   `json:"available"`
	MutatesWorkspace bool                   `json:"mutates_workspace"`
	SystemPrompt     string                 `json:"system_prompt"`
	Tools            []string               `json:"tools"`
	Delegates        []string               `json:"delegates"`
}

type subagentTargetUpdate struct {
	WorkspaceRoot string                 `json:"workspace_root"`
	Scope         string                 `json:"scope,omitempty"`
	ID            string                 `json:"id"`
	Target        *config.SubagentTarget `json:"target"`
}

func (service *integrationService) serveSubagentTarget(writer http.ResponseWriter, request *http.Request) {
	var input subagentTargetUpdate
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := optionalCanonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	var profile subagent.AgentDefinition
	for _, definition := range subagent.PublicAgentDefinitions() {
		if definition.Info.Name == input.ID {
			profile = definition
			break
		}
	}
	if input.Scope == "workspace" && profile.Info.Name == "" && root != "" {
		scope, name, _ := strings.Cut(input.ID, "/")
		if scope == "global" {
			entry, found := findProfileEntry(profileStore(service.main, root).List(), scope, name)
			if found {
				profile, _ = subagent.DefinitionForProfile(entry)
			}
		}
	}
	if profile.Info.Name == "" {
		writeAPIError(writer, http.StatusNotFound, errors.New("unknown subagent profile"))
		return
	}
	if input.Target != nil && profile.Info.Kind == subagent.AgentKindExternal && input.Target.Agent == "" {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("this built-in subagent requires an ACP connection"))
		return
	}
	value, err := service.main.Load()
	if errors.Is(err, config.ErrNotFound) {
		value, err = config.Default(), nil
		value.UseManagedGateway()
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if err := validateSubagentTarget(value, input.Target); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if input.Scope == "workspace" {
		if root == "" || profile.Info.Kind != subagent.AgentKindInner {
			writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("a repository and native profile are required for a repository override"))
			return
		}
		store := workspace.Store{Root: root}
		targets, err := store.LoadSubagentTargets()
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		if input.Target == nil {
			delete(targets, input.ID)
		} else {
			targets[input.ID] = *input.Target
		}
		if err := store.SaveSubagentTargets(targets); err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		service.writeAgentsUnlocked(writer, root, value)
		return
	}
	if input.Scope != "" && input.Scope != "global" {
		writeAPIError(writer, http.StatusBadRequest, errors.New("invalid subagent assignment scope"))
		return
	}
	if profile.Info.Kind == subagent.AgentKindExternal {
		if input.Target == nil {
			delete(value.Agents.Roles, profile.Info.Role)
		} else {
			if value.Agents.Roles == nil {
				value.Agents.Roles = make(map[string]config.AgentConfig)
			}
			value.Agents.Roles[profile.Info.Role] = config.AgentConfig{Agent: input.Target.Agent}
		}
	} else if input.Target == nil {
		delete(value.Agents.SubagentTargets, input.ID)
	} else {
		if value.Agents.SubagentTargets == nil {
			value.Agents.SubagentTargets = make(map[string]config.SubagentTarget)
		}
		value.Agents.SubagentTargets[input.ID] = *input.Target
	}
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeAgentsUnlocked(writer, root, value)
}

func validateSubagentTarget(value config.Config, target *config.SubagentTarget) error {
	if target == nil {
		return nil
	}
	if err := target.Validate(); err != nil {
		return err
	}
	if group, grouped := strings.CutPrefix(target.Model, "group/"); grouped {
		if _, found := value.ModelGroups[group]; !found {
			return fmt.Errorf("model group %q does not exist", group)
		}
	}
	if target.Agent != "" {
		if connection, found := value.Agents.Connections[target.Agent]; !found || connection.Disabled {
			return errors.New("select an enabled ACP connection")
		}
	}
	return nil
}

// studioSubagentTarget presents assignments saved by older versions in the
// profile editor. Explicit selections take precedence over that compatibility
// path, including a selection of the default model.
func studioSubagentTarget(value config.Config, definition subagent.AgentDefinition, models workspace.ModelConfig) *config.SubagentTarget {
	if definition.Target != nil {
		return definition.Target
	}
	if definition.Info.Kind == subagent.AgentKindExternal {
		if definition.Connection == "" {
			return nil
		}
		return &config.SubagentTarget{Agent: definition.Connection}
	}
	role := definition.Info.Role
	assignment := value.ConfiguredAgent(role)
	if override, found := models.Overrides[role]; found {
		assignment = value.Agents.Roles[role]
		assignment.Model, assignment.Group = override.Model, ""
	}
	if assignment.Group != "" {
		return &config.SubagentTarget{Model: "group/" + assignment.Group, ReasoningEffort: assignment.ReasoningEffort}
	}
	if assignment.Model != "" || assignment.ReasoningEffort != "" {
		return &config.SubagentTarget{Model: assignment.Model, Default: assignment.Model == "", ReasoningEffort: assignment.ReasoningEffort}
	}
	return nil
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

func (service *integrationService) serveAgents(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		root, err := optionalCanonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
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
	root, err := optionalCanonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if errors.Is(err, config.ErrNotFound) {
		value, err = config.Default(), nil
		value.UseManagedGateway()
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if input.Connections == nil {
		input.Connections = value.Agents.Connections
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
		if input.Bindings == nil {
			if assignment := value.Agents.Roles[role]; assignment.Agent != "" {
				if _, found := input.Connections[assignment.Agent]; !found {
					delete(value.Agents.Roles, role)
				}
			}
			continue
		}
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

func (service *integrationService) writeAgents(writer http.ResponseWriter, root string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if errors.Is(err, config.ErrNotFound) {
		value, err = config.Default(), nil
		value.UseManagedGateway()
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	service.writeAgentsUnlocked(writer, root, value)
}

func (service *integrationService) writeAgentsUnlocked(writer http.ResponseWriter, root string, value config.Config) {
	var workspaceTargets map[string]config.SubagentTarget
	var workspaceModels workspace.ModelConfig
	if root != "" {
		store := workspace.Store{Root: root}
		var err error
		workspaceTargets, err = store.LoadSubagentTargets()
		if err == nil {
			workspaceModels, err = store.LoadModelConfig()
		}
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
	}
	defaultModel := value.Provider.Model
	if override, found := workspaceModels.Overrides["default"]; found {
		defaultModel = override.Model
	}
	if value.Agents.Connections == nil {
		value.Agents.Connections = make(map[string]config.AgentConnectionConfig)
	}
	connections := make(map[string]config.AgentConnectionConfig, len(value.Agents.Connections))
	for id, connection := range value.Agents.Connections {
		connection.Args = append([]string{}, connection.Args...)
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
	for _, base := range subagent.PublicAgentDefinitions() {
		definition := subagent.ExecutionDefinition(value, base, workspaceTargets)
		available := definition.Info.Kind != subagent.AgentKindExternal
		connection := definition.Connection
		target := studioSubagentTarget(value, definition, workspaceModels)
		if definition.Info.Kind == subagent.AgentKindExternal {
			settings, found := value.Agents.Connections[connection]
			available = found && !settings.Disabled
		}
		builtins = append(builtins, studioAgentDefinition{
			Name: definition.Info.Name, Description: definition.Info.Description, Source: definition.Info.Source,
			Kind: definition.Info.Kind, Role: definition.Info.Role, Connection: connection, Available: available,
			Target: target, ExternalOnly: base.Info.Kind == subagent.AgentKindExternal,
			MutatesWorkspace: definition.Info.MutatesWorkspace,
			SystemPrompt:     definition.SystemPrompt,
			Tools:            append([]string{}, definition.Tools...),
			Delegates:        append([]string{}, definition.Delegates...),
		})
		for _, name := range base.Tools {
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
		entry.Profile.Tools = append([]string{}, entry.Profile.Tools...)
		entry.Profile.Delegates = append([]string{}, entry.Profile.Delegates...)
		item := studioProfileEntry{Profile: entry.Profile, Scope: entry.Scope, Path: entry.Path, Revision: revision(entry.Raw), Shadowed: entry.Shadowed}
		if entry.Err != nil {
			item.Error = entry.Err.Error()
		} else if definition, err := subagent.DefinitionForProfile(entry); err == nil {
			definition = subagent.ExecutionDefinition(value, definition, workspaceTargets)
			item.EffectiveTarget = studioSubagentTarget(value, definition, workspaceModels)
		}
		profiles = append(profiles, item)
	}
	writeJSON(writer, http.StatusOK, agentSettingsResponse{
		WorkspaceRoot: root, ConfigPath: service.main.Path(), DefaultModel: defaultModel, Connections: connections,
		Bindings: bindings, ExternalRoles: config.ExternalAgentRoles(), NativeRoles: value.NativeRoles(),
		Builtins: builtins, Profiles: profiles, ToolNames: tools,
	})
}

func validateAgentConnectionReferences(value config.Config, profiles subagent.ProfileStore) error {
	for _, entry := range profiles.List() {
		if entry.Err != nil {
			continue
		}
		id := entry.Profile.ACPConnection()
		if id == "" {
			continue
		}
		if _, found := value.Agents.Connections[id]; !found {
			return fmt.Errorf("connection %q is used by %s", id, subagent.CanonicalProfileID(entry.Scope, entry.Profile.Name))
		}
	}
	return value.Validate()
}
