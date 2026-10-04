package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/lsp"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/systemoneconfig"
)

func (state *transferState) merge(section, id string, data json.RawMessage) error {
	kind, name, _ := strings.Cut(id, "/")
	switch section {
	case "models":
		switch kind {
		case "default":
			if id != kind {
				break
			}
			value, err := transferValue[transferDefaultModel](data)
			if err != nil {
				return err
			}
			state.main.Provider.Model, state.main.Provider.ReasoningEffort = value.Model, value.ReasoningEffort
			state.main.Provider.ContextWindow = value.ContextWindow
			return nil
		case "embedding":
			if id != kind {
				break
			}
			value, err := transferValue[transferEmbedding](data)
			if err != nil {
				return err
			}
			state.main.Embedding = config.EmbeddingConfig(value)
			return nil
		case "role":
			if !config.IsAgentRole(name) && !config.ValidCustomRoleName(name) {
				break
			}
			value, err := transferValue[transferRole](data)
			if err != nil {
				return err
			}
			if state.main.Agents.Roles == nil {
				state.main.Agents.Roles = make(map[string]config.AgentConfig)
			}
			state.main.Agents.Roles[name] = config.AgentConfig{Model: value.Model, Group: value.Group, ReasoningEffort: value.ReasoningEffort}
			return nil
		case "group":
			value, err := transferValue[[]modelCandidateSettings](data)
			if err != nil {
				return err
			}
			group := config.ModelGroupConfig{}
			for _, candidate := range value {
				var timeout time.Duration
				if candidate.Timeout != "" {
					timeout, err = time.ParseDuration(candidate.Timeout)
					if err != nil {
						return err
					}
				}
				group.Candidates = append(group.Candidates, config.ModelCandidateConfig{Model: candidate.Model, ReasoningEffort: candidate.ReasoningEffort, Timeout: timeout})
			}
			if state.main.ModelGroups == nil {
				state.main.ModelGroups = make(map[string]config.ModelGroupConfig)
			}
			state.main.ModelGroups[name] = group
			return nil
		case "api-mode":
			value, err := transferValue[string](data)
			if err != nil {
				return err
			}
			if state.main.ModelAPIModes == nil {
				state.main.ModelAPIModes = make(map[string]string)
			}
			state.main.ModelAPIModes[name] = value
			return nil
		}
	case "providers":
		if kind == "provider" && name != "" {
			value, err := transferValue[gateway.ProviderConfig](data)
			if err != nil {
				return err
			}
			if value.ID != name {
				return errors.New("provider ID does not match item ID")
			}
			if !reflect.DeepEqual(value, portableProvider(value)) {
				return errors.New("provider secrets and opaque credential maps cannot be imported")
			}
			index := providerIndex(state.providers, name)
			if index < 0 {
				state.providers.Providers = append(state.providers.Providers, value)
				return nil
			}
			previous := state.providers.Providers[index]
			value.APIKey, value.Headers, value.Body = previous.APIKey, previous.Headers, previous.Body
			value.Codex.Environment, value.Codex.ThreadStart = previous.Codex.Environment, previous.Codex.ThreadStart
			value.Codex.ConversationCache.Redis.Password = previous.Codex.ConversationCache.Redis.Password
			state.providers.Providers[index] = value
			return nil
		}
	case "system-one":
		switch kind {
		case "default":
			if id != kind {
				break
			}
			value, err := transferValue[string](data)
			if err != nil {
				return err
			}
			state.systemOne.DefaultModel = value
			return nil
		case "role":
			value, err := transferValue[string](data)
			if err != nil {
				return err
			}
			if state.systemOne.RoleModels == nil {
				state.systemOne.RoleModels = make(map[string]string)
			}
			state.systemOne.RoleModels[name] = value
			return nil
		case "provider":
			value, err := transferValue[systemoneconfig.ProviderConfig](data)
			if err != nil {
				return err
			}
			if value.ID != name {
				return errors.New("provider ID does not match item ID")
			}
			for index, provider := range state.systemOne.Providers {
				if provider.ID == name {
					state.systemOne.Providers[index] = value
					return nil
				}
			}
			state.systemOne.Providers = append(state.systemOne.Providers, value)
			return nil
		}
	case "runtime":
		switch id {
		case "parallel-agents":
			value, err := transferValue[int](data)
			if err != nil {
				return err
			}
			state.main.Agents.MaxParallel = value
			return nil
		case "context":
			value, err := transferValue[contextSettings](data)
			if err != nil {
				return err
			}
			state.main.Context = config.ContextConfig(value)
			return nil
		case "loom":
			value, err := transferValue[transferLoom](data)
			if err != nil {
				return err
			}
			state.main.Loom = config.LoomConfig{MaximumArtifactMiB: value.MaximumArtifactMiB, MaximumStoreMiB: value.MaximumStoreMiB, GC: config.LoomGCConfig(value.GC)}
			return nil
		case "system-prompt":
			value, err := transferValue[string](data)
			if err != nil {
				return err
			}
			state.main.Provider.SystemPrompt = value
			return nil
		}
	case "services":
		if id == "gateway" || id == "system-one" {
			value, err := transferValue[serviceUpdate](data)
			if err != nil {
				return err
			}
			if id == "gateway" {
				state.gateway.Server.Host, state.gateway.Server.Port = value.Host, value.Port
			} else {
				state.systemOne.Server.Host, state.systemOne.Server.Port = value.Host, value.Port
			}
			return nil
		}
		value, err := transferValue[transferLocalService](data)
		if err != nil {
			return err
		}
		switch id {
		case "library":
			state.library.Host, state.library.Port, state.library.ProbeHost = value.Host, value.Port, value.ProbeHost
			return nil
		case "workspace-memory":
			state.memory.Host, state.memory.Port, state.memory.ProbeHost = value.Host, value.Port, value.ProbeHost
			return nil
		case "usage":
			state.usage.Host, state.usage.Port, state.usage.ProbeHost = value.Host, value.Port, value.ProbeHost
			return nil
		}
	case "subagents":
		switch kind {
		case "connection":
			value, err := transferValue[config.AgentConnectionConfig](data)
			if err != nil {
				return err
			}
			if len(value.Env) != 0 {
				return errors.New("ACP environment values cannot be imported")
			}
			if state.main.Agents.Connections == nil {
				state.main.Agents.Connections = make(map[string]config.AgentConnectionConfig)
			}
			value.Env = state.main.Agents.Connections[name].Env
			state.main.Agents.Connections[name] = value
			return nil
		case "binding":
			if !config.IsExternalAgentRole(name) {
				break
			}
			value, err := transferValue[string](data)
			if err != nil {
				return err
			}
			if state.main.Agents.Roles == nil {
				state.main.Agents.Roles = make(map[string]config.AgentConfig)
			}
			state.main.Agents.Roles[name] = config.AgentConfig{Agent: value}
			return nil
		case "profile":
			value, err := transferValue[subagent.Profile](data)
			if err != nil {
				return err
			}
			if value.Name != name {
				return errors.New("profile name does not match item ID")
			}
			if err := value.Validate(); err != nil {
				return err
			}
			entry := state.profiles[name]
			entry.Profile, entry.Scope = value, "global"
			state.profiles[name] = entry
			return nil
		}
	case "integrations":
		switch kind {
		case "mcp-server":
			value, err := transferValue[mcpconfig.ServerConfig](data)
			if err != nil {
				return err
			}
			if state.mcp.Servers == nil {
				state.mcp.Servers = make(map[string]mcpconfig.ServerConfig)
			}
			state.mcp.Servers[name] = value
			return nil
		case "mcp-role":
			value, err := transferValue[[]string](data)
			if err != nil {
				return err
			}
			if state.mcp.Roles == nil {
				state.mcp.Roles = make(map[string][]string)
			}
			state.mcp.Roles[name] = value
			return nil
		case "lsp-server":
			value, err := transferValue[lsp.ServerConfig](data)
			if err != nil {
				return err
			}
			if state.main.LSP.Servers == nil {
				state.main.LSP.Servers = make(map[string]lsp.ServerConfig)
			}
			state.main.LSP.Servers[name] = value
			return nil
		case "lsp-language":
			value, err := transferValue[string](data)
			if err != nil {
				return err
			}
			if state.main.LSP.Languages == nil {
				state.main.LSP.Languages = make(map[string]string)
			}
			state.main.LSP.Languages[name] = value
			return nil
		}
	}
	return errors.New("unsupported setting ID or section")
}

func transferTouchesMain(bundle settingsBundle) bool {
	if len(bundle.Sections["models"])+len(bundle.Sections["runtime"]) > 0 {
		return true
	}
	for id := range bundle.Sections["subagents"] {
		if !strings.HasPrefix(id, "profile/") {
			return true
		}
	}
	for id := range bundle.Sections["integrations"] {
		if strings.HasPrefix(id, "lsp-") {
			return true
		}
	}
	return false
}

// Validate the complete merged state before any file is changed, so dependencies
// can be imported together even when they span multiple tabs.
func (state *transferState) validate(ctx context.Context, bundle settingsBundle, directory string) error {
	if transferTouchesMain(bundle) || len(bundle.Sections["subagents"]) > 0 {
		normalized, err := state.main.LSP.Normalized()
		if err != nil {
			return err
		}
		state.main.LSP = normalized
		if err := state.main.Validate(); err != nil {
			return err
		}
	}
	if err := state.gateway.Validate(); err != nil {
		return err
	}
	if err := state.systemOne.Validate(); err != nil {
		return err
	}
	if err := state.library.Validate(); err != nil {
		return err
	}
	if err := state.memory.Validate(); err != nil {
		return err
	}
	if err := state.usage.Validate(); err != nil {
		return err
	}
	if err := state.mcp.Validate(); err != nil {
		return err
	}
	if len(bundle.Sections["providers"]) > 0 {
		if err := validateProviderIdentities(state.providers); err != nil {
			return err
		}
		validation, err := gateway.NewContext(ctx, providerhost.LocalConfig(state.providers, directory))
		if err != nil {
			return err
		}
		if err := validation.Close(); err != nil {
			return err
		}
	}
	if len(bundle.Sections["subagents"])+len(bundle.Sections["models"]) > 0 {
		definitions := subagent.PublicAgentDefinitions()
		for _, entry := range state.profiles {
			profile := entry.Profile
			if profile.EffectiveKind() == subagent.AgentKindExternal {
				connection, found := state.main.Agents.Connections[profile.Agent]
				_, importingProfile := bundle.Sections["subagents"]["profile/"+profile.Name]
				if !found || connection.Disabled && importingProfile {
					return fmt.Errorf("profile %q requires enabled connection %q", profile.Name, profile.Agent)
				}
			} else if !state.main.HasNativeRole(profile.Role) {
				return fmt.Errorf("profile %q requires model role %q", profile.Name, profile.Role)
			}
			definition, err := subagent.DefinitionForProfile(entry)
			if err != nil {
				return err
			}
			definitions = append(definitions, definition)
		}
		if _, err := subagent.NewRegistry(definitions); err != nil {
			return err
		}
	}
	return nil
}
