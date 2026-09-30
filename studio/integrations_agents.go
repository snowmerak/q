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
)

const redactedConnectionSecret = "********"

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
	Source           string   `json:"source"`
	Kind             string   `json:"kind"`
	Role             string   `json:"role"`
	Connection       string   `json:"connection,omitempty"`
	Available        bool     `json:"available"`
	MutatesWorkspace bool     `json:"mutates_workspace"`
	SystemPrompt     string   `json:"system_prompt"`
	Tools            []string `json:"tools"`
	Delegates        []string `json:"delegates"`
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
	for _, definition := range subagent.PublicAgentDefinitions() {
		available := definition.Info.Kind != subagent.AgentKindExternal
		connection := ""
		if definition.Info.Kind == subagent.AgentKindExternal {
			connection, _, available = value.ExternalAgentConnection(definition.Info.Role)
		}
		builtins = append(builtins, studioAgentDefinition{
			Name: definition.Info.Name, Description: definition.Info.Description, Source: definition.Info.Source,
			Kind: definition.Info.Kind, Role: definition.Info.Role, Connection: connection, Available: available,
			MutatesWorkspace: definition.Info.MutatesWorkspace,
			SystemPrompt:     definition.SystemPrompt,
			Tools:            append([]string{}, definition.Tools...),
			Delegates:        append([]string{}, definition.Delegates...),
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
		entry.Profile.Tools = append([]string{}, entry.Profile.Tools...)
		entry.Profile.Delegates = append([]string{}, entry.Profile.Delegates...)
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
