package studio

import (
	"time"

	"github.com/snowmerak/q/loom"
)

type settingsSnapshot struct {
	Version      int                  `json:"version"`
	Scope        string               `json:"scope"`
	Runtime      runtimeSettings      `json:"runtime"`
	Models       modelSettings        `json:"models"`
	Providers    providerSettings     `json:"gateway_providers"`
	SystemOne    systemOneAPISettings `json:"system_one"`
	Services     serviceSettings      `json:"services"`
	Integrations integrationSettings  `json:"integrations"`
}

type runtimeSettings struct {
	Configured  bool            `json:"configured"`
	ConfigPath  string          `json:"config_path"`
	MaxParallel int             `json:"max_parallel"`
	Context     contextSettings `json:"context"`
	Loom        loomSettings    `json:"loom"`
}

type contextSettings struct {
	Window       int64   `json:"window"`
	TriggerRatio float64 `json:"trigger_ratio"`
	TargetRatio  float64 `json:"target_ratio"`
	RecentRatio  float64 `json:"recent_ratio"`
}

type loomSettings struct {
	MaximumArtifactMiB int     `json:"maximum_artifact_mib"`
	MaximumStoreMiB    int     `json:"maximum_store_mib"`
	GCDisabled         bool    `json:"gc_disabled"`
	GCTriggerRatio     float64 `json:"gc_trigger_ratio"`
	GCTargetRatio      float64 `json:"gc_target_ratio"`
	GCGraceHours       int     `json:"gc_grace_hours"`
}

type modelSettings struct {
	ConfigPath          string                `json:"config_path"`
	DefaultModel        string                `json:"default_model"`
	DefaultReasoning    string                `json:"default_reasoning_effort"`
	EmbeddingModel      string                `json:"embedding_model"`
	EmbeddingDimensions int                   `json:"embedding_dimensions"`
	GroupCount          int                   `json:"group_count"`
	Groups              []modelGroupSettings  `json:"groups"`
	Roles               []roleModelAssignment `json:"roles"`
}

type modelGroupSettings struct {
	Name       string                   `json:"name"`
	Candidates []modelCandidateSettings `json:"candidates"`
}

type modelCandidateSettings struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Timeout         string `json:"timeout,omitempty"`
}

type roleModelAssignment struct {
	Role            string `json:"role"`
	ConfiguredModel string `json:"configured_model"`
	EffectiveModel  string `json:"effective_model"`
	ReasoningEffort string `json:"reasoning_effort"`
	Inherited       bool   `json:"inherited"`
	Custom          bool   `json:"custom"`
}

type providerSettings struct {
	ConfigPath string                    `json:"config_path"`
	Items      []gatewayProviderSettings `json:"items"`
	APIKeys    []serviceAPIKeySettings   `json:"api_keys"`
}

type gatewayProviderSettings struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	Kind            string `json:"kind"`
	Prefix          string `json:"prefix"`
	Enabled         bool   `json:"enabled"`
	BaseURL         string `json:"base_url"`
	APIKeyEnv       string `json:"api_key_env"`
	HasInlineAPIKey bool   `json:"has_inline_api_key"`
	ModelCount      int    `json:"model_count"`
}

type systemOneAPISettings struct {
	ConfigPath      string                      `json:"config_path"`
	DefaultModel    string                      `json:"default_model"`
	AgentSkillModel string                      `json:"agent_skill_model"`
	Providers       []systemOneProviderSettings `json:"providers"`
	APIKeys         []serviceAPIKeySettings     `json:"api_keys"`
	ActiveAPIKeys   int                         `json:"active_api_keys"`
}

type systemOneProviderSettings struct {
	ID        string `json:"id"`
	URI       string `json:"uri"`
	APIKeyEnv string `json:"api_key_env"`
}

type serviceAPIKeySettings struct {
	ID        string     `json:"id"`
	Alias     string     `json:"alias"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	Legacy    bool       `json:"legacy,omitempty"`
}

type serviceSettings struct {
	Gateway   listenerSettings  `json:"gateway"`
	SystemOne systemOneSettings `json:"system_one"`
	Library   listenerSettings  `json:"library"`
}

type listenerSettings struct {
	ConfigPath    string `json:"config_path"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	ActiveAPIKeys int    `json:"active_api_keys"`
}

type systemOneSettings struct {
	listenerSettings
	ProviderCount  int    `json:"provider_count"`
	DefaultModel   string `json:"default_model"`
	RoleModelCount int    `json:"role_model_count"`
}

type integrationSettings struct {
	MCP integrationSummary `json:"mcp"`
	LSP integrationSummary `json:"lsp"`
}

type integrationSummary struct {
	ConfigPath string `json:"config_path"`
	Items      int    `json:"items"`
	Bindings   int    `json:"bindings"`
}

type runtimeUpdate struct {
	MaxParallel int             `json:"max_parallel"`
	Context     contextSettings `json:"context"`
	Loom        loomSettings    `json:"loom"`
}

type serviceUpdate struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type modelAssignmentUpdate struct {
	Model               string `json:"model"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	EmbeddingDimensions int    `json:"embedding_dimensions,omitempty"`
	WorkspaceRoot       string `json:"workspace_root,omitempty"`
}

type workspaceModelUpdate struct {
	WorkspaceRoot string `json:"workspace_root"`
	Model         string `json:"model,omitempty"`
}

type workspaceModelSettings struct {
	WorkspaceRoot string                         `json:"workspace_root"`
	ConfigPath    string                         `json:"config_path"`
	Assignments   []workspaceRoleModelAssignment `json:"assignments"`
}

type workspaceRoleModelAssignment struct {
	Role            string `json:"role"`
	ConfiguredModel string `json:"configured_model"`
	EffectiveModel  string `json:"effective_model"`
	Inherited       bool   `json:"inherited"`
}

type gatewayProviderUpdate struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Kind        string `json:"kind,omitempty"`
	Prefix      string `json:"prefix,omitempty"`
	Enabled     bool   `json:"enabled"`
	BaseURL     string `json:"base_url,omitempty"`
	APIKeyEnv   string `json:"api_key_env,omitempty"`
	APIKey      string `json:"api_key,omitempty"`
	ClearAPIKey bool   `json:"clear_api_key,omitempty"`
}

type systemOneProviderUpdate struct {
	ID        string `json:"id"`
	URI       string `json:"uri"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

type systemOneModelUpdate struct {
	Model string `json:"model"`
}

type apiKeyCreateRequest struct {
	Alias string `json:"alias"`
}

type apiKeyCreateResponse struct {
	Settings settingsSnapshot `json:"settings"`
	Secret   string           `json:"secret"`
}

type modelCatalog struct {
	Models []modelOption `json:"models"`
}

type systemOneModelCatalog struct {
	Models []systemOneModelOption `json:"models"`
}

type systemOneModelOption struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
}

type modelOption struct {
	ID               string   `json:"id"`
	ContextLength    int64    `json:"context_length,omitempty"`
	ReasoningControl string   `json:"reasoning_control,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
	DefaultEffort    string   `json:"default_effort,omitempty"`
	Group            bool     `json:"group,omitempty"`
	APIMode          string   `json:"api_mode,omitempty"`
	ContextOverride  int64    `json:"context_override,omitempty"`
}

type modelGroupUpdate struct {
	Name       string                   `json:"name"`
	Candidates []modelCandidateSettings `json:"candidates"`
}

type customRoleUpdate struct {
	Role string `json:"role"`
}

type modelAPIModeUpdate struct {
	Model string `json:"model"`
	Mode  string `json:"mode"`
}

type modelMetadataUpdate struct {
	Model         string `json:"model"`
	ContextWindow int64  `json:"context_window"`
}

type loomOperationRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
	DryRun        bool   `json:"dry_run"`
}

type loomStatusResponse struct {
	WorkspaceRoot string         `json:"workspace_root"`
	Stats         loom.Stats     `json:"stats"`
	Result        *loom.GCResult `json:"result,omitempty"`
}
