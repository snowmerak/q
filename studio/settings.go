package studio

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gatewayconfig"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/systemoneconfig"
)

type settingsService struct {
	mu        sync.Mutex
	main      config.Store
	gateway   gatewayconfig.Store
	systemOne systemoneconfig.Store
	mcp       mcpconfig.Store
	providers providerhost.Store
	library   qlibrary.ConfigStore
	runtime   settingsRuntime
}

type settingsRuntime interface {
	ApplyGateway(context.Context, gateway.Config) error
}

type embeddingRuntime interface {
	SyncEmbeddings(context.Context, string, config.Config) error
}

func newSettingsService(store config.Store, runtimes ...settingsRuntime) *settingsService {
	service := &settingsService{
		main:      store,
		gateway:   gatewayconfig.Store{Dir: store.Dir},
		systemOne: systemoneconfig.Store{Dir: store.Dir},
		mcp:       mcpconfig.Store{Dir: store.Dir},
		providers: providerhost.Store{Dir: store.Dir},
		library:   qlibrary.ConfigStore{Dir: store.Dir},
	}
	if len(runtimes) > 0 {
		service.runtime = runtimes[0]
	}
	return service
}

func (service *settingsService) serveSnapshot(writer http.ResponseWriter, _ *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	snapshot, err := service.snapshot()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshot)
}

func (service *settingsService) writeSnapshot(writer http.ResponseWriter) {
	snapshot, err := service.snapshot()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshot)
}

func (service *settingsService) snapshot() (settingsSnapshot, error) {
	main, err := service.main.Load()
	configured := true
	if errors.Is(err, config.ErrNotFound) {
		main, configured, err = config.Default(), false, nil
	}
	if err != nil {
		return settingsSnapshot{}, err
	}
	gateway, err := service.gateway.LoadOrDefault()
	if err != nil {
		return settingsSnapshot{}, err
	}
	systemOne, err := service.systemOne.LoadOrDefault()
	if err != nil {
		return settingsSnapshot{}, err
	}
	library, err := service.library.LoadOrDefault()
	if err != nil {
		return settingsSnapshot{}, err
	}
	mcp, err := service.mcp.LoadOrDefault()
	if err != nil {
		return settingsSnapshot{}, err
	}
	providerConfig, err := service.loadProviderConfig()
	if err != nil {
		return settingsSnapshot{}, err
	}

	contextValue := main.EffectiveContext()
	loomValue := main.EffectiveLoom()
	agentsValue := main.EffectiveAgents()
	roleAssignments := make([]roleModelAssignment, 0, len(main.NativeRoles()))
	for _, role := range main.NativeRoles() {
		raw, configured := agentsValue.Roles[role]
		configuredModel := raw.Model
		if raw.Group != "" {
			configuredModel = "group/" + raw.Group
		}
		effective, effectiveErr := main.EffectiveAgent(role)
		if effectiveErr != nil {
			return settingsSnapshot{}, effectiveErr
		}
		effectiveModel := effective.Model
		if effective.Group != "" {
			effectiveModel = "group/" + effective.Group
		}
		roleAssignments = append(roleAssignments, roleModelAssignment{
			Role: role, ConfiguredModel: configuredModel, EffectiveModel: effectiveModel,
			ReasoningEffort: raw.ReasoningEffort,
			Inherited:       !configured || raw.Model == "" && raw.Group == "",
			Custom:          config.ValidCustomRoleName(role),
		})
	}
	groups := make([]modelGroupSettings, 0, len(main.ModelGroups))
	for name, group := range main.ModelGroups {
		item := modelGroupSettings{Name: name, Candidates: make([]modelCandidateSettings, 0, len(group.Candidates))}
		for _, candidate := range group.Candidates {
			timeout := ""
			if candidate.Timeout > 0 {
				timeout = candidate.Timeout.String()
			}
			item.Candidates = append(item.Candidates, modelCandidateSettings{
				Model: candidate.Model, ReasoningEffort: candidate.ReasoningEffort, Timeout: timeout,
			})
		}
		groups = append(groups, item)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	providerItems := make([]gatewayProviderSettings, 0, len(providerConfig.Providers))
	for _, provider := range providerConfig.Providers {
		modelCount := len(provider.Models)
		if len(provider.ModelMetadata) > modelCount {
			modelCount = len(provider.ModelMetadata)
		}
		providerItems = append(providerItems, gatewayProviderSettings{
			ID: provider.ID, Type: provider.Type, Kind: provider.Kind, Prefix: provider.Prefix,
			Enabled: provider.Enabled, BaseURL: provider.BaseURL, APIKeyEnv: provider.APIKeyEnv,
			HasInlineAPIKey: provider.APIKey != "", ModelCount: modelCount,
		})
	}
	gatewayKeys := make([]serviceAPIKeySettings, 0, len(gateway.APIKeys))
	for _, key := range gateway.APIKeys {
		createdAt := key.CreatedAt
		gatewayKeys = append(gatewayKeys, serviceAPIKeySettings{
			ID: key.ID, Alias: key.Alias, CreatedAt: &createdAt, RevokedAt: key.RevokedAt,
		})
	}
	systemOneProviders := make([]systemOneProviderSettings, 0, len(systemOne.Providers))
	for _, provider := range systemOne.Providers {
		systemOneProviders = append(systemOneProviders, systemOneProviderSettings{
			ID: provider.ID, URI: provider.URI, APIKeyEnv: provider.APIKeyEnv,
		})
	}
	systemOneKeys := make([]serviceAPIKeySettings, 0, len(systemOne.APIKeys)+1)
	if systemOne.Server.APIKey != "" {
		systemOneKeys = append(systemOneKeys, serviceAPIKeySettings{ID: "legacy", Alias: "Legacy key", Legacy: true})
	}
	for _, key := range systemOne.APIKeys {
		createdAt := key.CreatedAt
		systemOneKeys = append(systemOneKeys, serviceAPIKeySettings{
			ID: key.ID, Alias: key.Alias, CreatedAt: &createdAt, RevokedAt: key.RevokedAt,
		})
	}
	return settingsSnapshot{
		Version: 1,
		Scope:   "global",
		Runtime: runtimeSettings{
			Configured: configured, ConfigPath: service.main.Path(), MaxParallel: agentsValue.MaxParallel,
			Context: contextSettings{
				Window: contextValue.Window, TriggerRatio: contextValue.TriggerRatio,
				TargetRatio: contextValue.TargetRatio, RecentRatio: contextValue.RecentRatio,
			},
			Loom: loomSettings{
				MaximumArtifactMiB: loomValue.MaximumArtifactMiB,
				MaximumStoreMiB:    loomValue.MaximumStoreMiB,
				GCDisabled:         loomValue.GC.Disabled,
				GCTriggerRatio:     loomValue.GC.TriggerRatio,
				GCTargetRatio:      loomValue.GC.TargetRatio,
				GCGraceHours:       loomValue.GC.GraceHours,
			},
		},
		Models: modelSettings{
			ConfigPath: service.main.Path(), DefaultModel: main.Provider.Model,
			DefaultReasoning: main.Provider.EffectiveReasoningEffort(),
			EmbeddingModel:   main.Embedding.Model, EmbeddingDimensions: main.Embedding.Dimensions,
			GroupCount: len(main.ModelGroups), Groups: groups, Roles: roleAssignments,
		},
		Providers: providerSettings{ConfigPath: service.providers.Path(), Items: providerItems, APIKeys: gatewayKeys},
		SystemOne: systemOneAPISettings{
			ConfigPath: service.systemOne.Path(), DefaultModel: systemOne.DefaultModel,
			AgentSkillModel: systemOne.RoleModels[systemoneconfig.RoleAgentSkillDecision],
			ArchiveModel:    systemOne.RoleModels[systemoneconfig.RoleArchiveDecision],
			Providers:       systemOneProviders, APIKeys: systemOneKeys, ActiveAPIKeys: systemOne.ActiveKeyCount(),
		},
		Services: serviceSettings{
			Gateway: listenerSettings{
				ConfigPath: service.gateway.Path(), Host: gateway.Server.Host,
				Port: gateway.Server.Port, ActiveAPIKeys: gateway.ActiveKeyCount(),
			},
			SystemOne: systemOneSettings{
				listenerSettings: listenerSettings{
					ConfigPath: service.systemOne.Path(), Host: systemOne.Server.Host,
					Port: systemOne.Server.Port, ActiveAPIKeys: systemOne.ActiveKeyCount(),
				},
				ProviderCount: len(systemOne.Providers), DefaultModel: systemOne.DefaultModel,
				RoleModelCount: len(systemOne.RoleModels),
			},
			Library: listenerSettings{ConfigPath: service.library.Path(), Host: library.Host, Port: library.Port},
		},
		Integrations: integrationSettings{
			MCP: integrationSummary{ConfigPath: service.mcp.Path(), Items: len(mcp.Servers), Bindings: len(mcp.Roles)},
			LSP: integrationSummary{ConfigPath: service.main.Path(), Items: len(main.LSP.Servers), Bindings: len(main.LSP.Languages)},
		},
	}, nil
}
