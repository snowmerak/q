// Package config stores q's personal application configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/lsp"
	"gopkg.in/yaml.v3"
)

const (
	CurrentVersion          = 1
	DirectoryName           = ".q"
	FileName                = "config.yaml"
	DefaultAgentMaxParallel = 3
	DefaultLoomArtifactMiB  = 64
	DefaultLoomStoreMiB     = 256
	DefaultLoomGCGraceHours = 1

	AgentRoleGriller   = "griller"
	AgentRoleScout     = "scout"
	AgentRoleResearch  = "research"
	AgentRoleSearch    = "search"
	AgentRolePlanner   = "planner"
	AgentRoleCoder     = "coder"
	AgentRoleCommit    = "commit"
	AgentRoleAdvisor   = "advisor"
	AgentRoleThinker   = "thinker"
	AgentRoleLibrarian = "librarian"
)

var ErrNotFound = errors.New("q config not found")

type Config struct {
	Version     int                         `yaml:"version"`
	Provider    ProviderConfig              `yaml:"provider"`
	Embedding   EmbeddingConfig             `yaml:"embedding,omitempty"`
	Context     ContextConfig               `yaml:"context,omitempty"`
	Plan        PlanConfig                  `yaml:"plan,omitempty"`
	ModelGroups map[string]ModelGroupConfig `yaml:"model_groups,omitempty"`
	Agents      AgentsConfig                `yaml:"agents,omitempty"`
	Loom        LoomConfig                  `yaml:"loom,omitempty"`
	LSP         lsp.GlobalConfig            `yaml:"lsp,omitempty"`
}

type ProviderConfig struct {
	Managed         bool   `yaml:"managed,omitempty"`
	Type            string `yaml:"type"`
	BaseURL         string `yaml:"base_url"`
	Model           string `yaml:"model"`
	ReasoningEffort string `yaml:"reasoning_effort,omitempty"`
	APIKeyEnv       string `yaml:"api_key_env,omitempty"`
	APIKey          string `yaml:"api_key,omitempty"`
	SystemPrompt    string `yaml:"system_prompt,omitempty"`
	ContextWindow   int64  `yaml:"context_window,omitempty"`
}

type ContextConfig struct {
	Window       int64   `yaml:"window,omitempty"`
	TriggerRatio float64 `yaml:"trigger_ratio,omitempty"`
	TargetRatio  float64 `yaml:"target_ratio,omitempty"`
	RecentRatio  float64 `yaml:"recent_ratio,omitempty"`
}

// PlanConfig controls persistent automation for the approval-gated /plan
// workflow. Command-line overrides may replace these values for one process.
type PlanConfig struct {
	AutoApprove bool `yaml:"auto_approve,omitempty"`
	AutoResolve bool `yaml:"auto_resolve,omitempty"`
}

type LoomConfig struct {
	MaximumArtifactMiB int          `yaml:"maximum_artifact_mib,omitempty"`
	MaximumStoreMiB    int          `yaml:"maximum_store_mib,omitempty"`
	GC                 LoomGCConfig `yaml:"gc,omitempty"`
}

type LoomGCConfig struct {
	Disabled     bool    `yaml:"disabled,omitempty"`
	TriggerRatio float64 `yaml:"trigger_ratio,omitempty"`
	TargetRatio  float64 `yaml:"target_ratio,omitempty"`
	GraceHours   int     `yaml:"grace_hours,omitempty"`
}

// EmbeddingConfig selects the model and fixed vector size used by q's
// rebuildable semantic indexes, including the global Library. A zero value
// disables embedding generation and vector indexing.
type EmbeddingConfig struct {
	Model      string `yaml:"model,omitempty"`
	Dimensions int    `yaml:"dimensions,omitempty"`
}

type AgentsConfig struct {
	MaxParallel int                              `yaml:"max_parallel,omitempty"`
	Connections map[string]AgentConnectionConfig `yaml:"connections,omitempty"`
	Roles       map[string]AgentConfig           `yaml:"roles,omitempty"`
}

// AgentConnectionConfig describes one ACP agent process. Preset supplies the
// built-in Codex or Grok command; Command and Args support another stdio ACP
// implementation instead.
type AgentConnectionConfig struct {
	Preset     string            `yaml:"preset,omitempty"`
	Command    string            `yaml:"command,omitempty"`
	Args       []string          `yaml:"args,omitempty"`
	Env        map[string]string `yaml:"env,omitempty"`
	AuthMethod string            `yaml:"auth_method,omitempty"`
	Disabled   bool              `yaml:"disabled,omitempty"`
}

type ModelGroupConfig struct {
	Candidates []ModelCandidateConfig `yaml:"candidates"`
}

type ModelCandidateConfig struct {
	Model           string        `yaml:"model"`
	ReasoningEffort string        `yaml:"reasoning_effort,omitempty"`
	Timeout         time.Duration `yaml:"timeout,omitempty"`
}

// AgentConfig selects either model controls for q-native roles or an ACP
// connection for an external role such as Search. Model and Group are mutually
// exclusive. With neither set, native roles inherit the active chat model.
type AgentConfig struct {
	Model           string `yaml:"model,omitempty"`
	Group           string `yaml:"group,omitempty"`
	ReasoningEffort string `yaml:"reasoning_effort,omitempty"`
	Agent           string `yaml:"agent,omitempty"`
}

var agentRoles = []string{
	AgentRoleGriller,
	AgentRoleScout,
	AgentRoleResearch,
	AgentRolePlanner,
	AgentRoleCoder,
	AgentRoleCommit,
	AgentRoleAdvisor,
	AgentRoleThinker,
	AgentRoleLibrarian,
}

func Default() Config {
	return Config{
		Version: CurrentVersion,
		Provider: ProviderConfig{
			Type:         "openai-compatible",
			BaseURL:      "https://api.openai.com/v1",
			APIKeyEnv:    "OPENAI_API_KEY",
			SystemPrompt: "You are a helpful assistant.",
		},
		Context: ContextConfig{
			TriggerRatio: 0.85,
			TargetRatio:  0.22,
			RecentRatio:  0.07,
		},
		Agents: AgentsConfig{MaxParallel: DefaultAgentMaxParallel},
		Loom: LoomConfig{
			MaximumArtifactMiB: DefaultLoomArtifactMiB,
			MaximumStoreMiB:    DefaultLoomStoreMiB,
			GC:                 LoomGCConfig{TriggerRatio: .80, TargetRatio: .60, GraceHours: DefaultLoomGCGraceHours},
		},
	}
}

func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("config: unsupported version %d", c.Version)
	}
	if !c.Provider.Managed {
		if c.Provider.Type != "openai-compatible" {
			return fmt.Errorf("config: unsupported provider type %q", c.Provider.Type)
		}
		parsed, err := url.Parse(c.Provider.BaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("config: provider base_url must be an absolute HTTP(S) URL")
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("config: provider base_url must not contain a query or fragment")
		}
	}
	if strings.TrimSpace(c.Provider.Model) == "" {
		return fmt.Errorf("config: provider model is required")
	}
	if effort := c.Provider.EffectiveReasoningEffort(); effort != "" && c.Provider.ReasoningEffort != effort {
		return fmt.Errorf("config: provider reasoning_effort must not have surrounding whitespace")
	}
	if strings.Contains(c.Provider.APIKeyEnv, "=") {
		return fmt.Errorf("config: api_key_env must be an environment variable name")
	}
	if c.Embedding.Model != strings.TrimSpace(c.Embedding.Model) {
		return fmt.Errorf("config: embedding model must not have surrounding whitespace")
	}
	if c.Embedding.Model == "" && c.Embedding.Dimensions != 0 {
		return fmt.Errorf("config: embedding model is required when dimensions are configured")
	}
	if c.Embedding.Model != "" && (c.Embedding.Dimensions < 1 || c.Embedding.Dimensions > 4096) {
		return fmt.Errorf("config: embedding dimensions must be between 1 and 4096")
	}
	contextConfig := c.EffectiveContext()
	if contextConfig.Window < 0 || c.Provider.ContextWindow < 0 {
		return fmt.Errorf("config: context window must not be negative")
	}
	if contextConfig.TriggerRatio <= 0 || contextConfig.TriggerRatio >= 1 {
		return fmt.Errorf("config: context trigger_ratio must be between 0 and 1")
	}
	if contextConfig.TargetRatio <= 0 || contextConfig.TargetRatio >= contextConfig.TriggerRatio {
		return fmt.Errorf("config: context target_ratio must be positive and below trigger_ratio")
	}
	if contextConfig.RecentRatio <= 0 || contextConfig.RecentRatio >= contextConfig.TargetRatio {
		return fmt.Errorf("config: context recent_ratio must be positive and below target_ratio")
	}
	if c.Agents.MaxParallel < 0 {
		return fmt.Errorf("config: agents max_parallel must not be negative")
	}
	for name, group := range c.ModelGroups {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return fmt.Errorf("config: model group name %q must be non-empty without surrounding whitespace", name)
		}
		if len(group.Candidates) == 0 {
			return fmt.Errorf("config: model group %q must contain at least one candidate", name)
		}
		seen := make(map[string]struct{}, len(group.Candidates))
		for index, candidate := range group.Candidates {
			if candidate.Model == "" || candidate.Model != strings.TrimSpace(candidate.Model) {
				return fmt.Errorf("config: model group %q candidate %d model must be non-empty without surrounding whitespace", name, index)
			}
			if strings.HasPrefix(candidate.Model, "group/") {
				return fmt.Errorf("config: model group %q candidate %d cannot reference another model group", name, index)
			}
			if _, duplicate := seen[candidate.Model]; duplicate {
				return fmt.Errorf("config: model group %q contains duplicate model %q", name, candidate.Model)
			}
			seen[candidate.Model] = struct{}{}
			if candidate.ReasoningEffort != strings.TrimSpace(candidate.ReasoningEffort) {
				return fmt.Errorf("config: model group %q candidate %d reasoning_effort must not have surrounding whitespace", name, index)
			}
			if candidate.Timeout < 0 {
				return fmt.Errorf("config: model group %q candidate %d timeout must not be negative", name, index)
			}
		}
	}
	if name, grouped := strings.CutPrefix(c.Provider.Model, "group/"); grouped {
		if _, found := c.ModelGroups[name]; !found {
			return fmt.Errorf("config: provider model references unknown model group %q", name)
		}
	}
	for id, connection := range c.Agents.Connections {
		if !validAgentConnectionID(id) {
			return fmt.Errorf("config: agent connection ID %q must use only letters, digits, '.', '_', or '-'", id)
		}
		if connection.Preset != strings.TrimSpace(connection.Preset) || connection.Command != strings.TrimSpace(connection.Command) {
			return fmt.Errorf("config: agent connection %q preset and command must not have surrounding whitespace", id)
		}
		if connection.Preset != "" && connection.Command != "" {
			return fmt.Errorf("config: agent connection %q preset and command are mutually exclusive", id)
		}
		if connection.Preset == "" && connection.Command == "" {
			return fmt.Errorf("config: agent connection %q requires preset or command", id)
		}
		if connection.Preset != "" && connection.Preset != "codex" && connection.Preset != "grok" {
			return fmt.Errorf("config: agent connection %q uses unsupported preset %q", id, connection.Preset)
		}
		for index, argument := range connection.Args {
			if strings.ContainsRune(argument, 0) {
				return fmt.Errorf("config: agent connection %q argument %d contains NUL", id, index)
			}
		}
		for name := range connection.Env {
			if name == "" || strings.Contains(name, "=") {
				return fmt.Errorf("config: agent connection %q has invalid environment variable name %q", id, name)
			}
		}
		if connection.AuthMethod != strings.TrimSpace(connection.AuthMethod) {
			return fmt.Errorf("config: agent connection %q auth_method must not have surrounding whitespace", id)
		}
	}
	for role, agent := range c.Agents.Roles {
		if role == AgentRoleSearch {
			if agent.Model != "" || agent.Group != "" || strings.TrimSpace(agent.ReasoningEffort) != "" {
				return fmt.Errorf("config: search role accepts agent only, not model controls")
			}
			if agent.Agent != strings.TrimSpace(agent.Agent) {
				return fmt.Errorf("config: search role agent must not have surrounding whitespace")
			}
			if agent.Agent != "" {
				if _, found := c.Agents.Connections[agent.Agent]; !found {
					return fmt.Errorf("config: search role references unknown agent connection %q", agent.Agent)
				}
			}
			continue
		}
		if !IsAgentRole(role) && !ValidCustomRoleName(role) {
			return fmt.Errorf("config: unsupported agent role %q", role)
		}
		if agent.Agent != "" {
			return fmt.Errorf("config: native agent role %q does not accept an ACP agent connection", role)
		}
		if agent.Model != strings.TrimSpace(agent.Model) {
			return fmt.Errorf("config: agent %q model must not have surrounding whitespace", role)
		}
		trimmedEffort := strings.TrimSpace(agent.ReasoningEffort)
		if trimmedEffort != "" && agent.ReasoningEffort != trimmedEffort {
			return fmt.Errorf("config: agent %q reasoning_effort must not have surrounding whitespace", role)
		}
		if agent.Group != strings.TrimSpace(agent.Group) {
			return fmt.Errorf("config: agent %q group must not have surrounding whitespace", role)
		}
		if agent.Model != "" && agent.Group != "" {
			return fmt.Errorf("config: agent %q model and group are mutually exclusive", role)
		}
		if agent.Group != "" {
			if _, found := c.ModelGroups[agent.Group]; !found {
				return fmt.Errorf("config: agent %q references unknown model group %q", role, agent.Group)
			}
		}
	}
	loomConfig := c.EffectiveLoom()
	if loomConfig.MaximumArtifactMiB < 1 || loomConfig.MaximumArtifactMiB > 1024 {
		return errors.New("config: Loom maximum_artifact_mib must be between 1 and 1024")
	}
	if loomConfig.MaximumStoreMiB < 1 || loomConfig.MaximumStoreMiB > 10240 {
		return errors.New("config: Loom maximum_store_mib must be between 1 and 10240")
	}
	if loomConfig.MaximumArtifactMiB > loomConfig.MaximumStoreMiB {
		return errors.New("config: Loom maximum_artifact_mib cannot exceed maximum_store_mib")
	}
	if loomConfig.GC.TargetRatio <= 0 || loomConfig.GC.TriggerRatio <= loomConfig.GC.TargetRatio || loomConfig.GC.TriggerRatio >= 1 {
		return errors.New("config: Loom GC ratios must satisfy 0 < target_ratio < trigger_ratio < 1")
	}
	if loomConfig.GC.GraceHours < 1 || loomConfig.GC.GraceHours > 24*365 {
		return errors.New("config: Loom GC grace_hours must be between 1 and 8760")
	}
	if err := c.LSP.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

func validAgentConnectionID(id string) bool {
	if id == "" || id != strings.TrimSpace(id) {
		return false
	}
	for _, value := range id {
		if value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
			value >= '0' && value <= '9' || value == '.' || value == '_' || value == '-' {
			continue
		}
		return false
	}
	return true
}

// UseManagedGateway removes legacy endpoint credentials after they have been
// migrated into providers.json. Model and chat policy fields remain intact.
func (c *Config) UseManagedGateway() {
	c.Provider.Managed = true
	c.Provider.Type = ""
	c.Provider.BaseURL = ""
	c.Provider.APIKeyEnv = ""
	c.Provider.APIKey = ""
}

// EffectiveContext fills policy values omitted by configurations written by
// older q versions.
func (c Config) EffectiveContext() ContextConfig {
	result := c.Context
	defaults := Default().Context
	if result.TriggerRatio == 0 {
		result.TriggerRatio = defaults.TriggerRatio
	}
	if result.TargetRatio == 0 {
		result.TargetRatio = defaults.TargetRatio
	}
	if result.RecentRatio == 0 {
		result.RecentRatio = defaults.RecentRatio
	}
	return result
}

// EffectiveContextWindow prefers the current Gateway model metadata and uses
// context.window only when the Gateway does not advertise a context length.
func (c Config) EffectiveContextWindow() int64 {
	if c.Provider.ContextWindow > 0 {
		return c.Provider.ContextWindow
	}
	return c.Context.Window
}

func (c Config) EffectiveLoom() LoomConfig {
	result := c.Loom
	defaults := Default().Loom
	if result.MaximumArtifactMiB == 0 {
		result.MaximumArtifactMiB = defaults.MaximumArtifactMiB
	}
	if result.MaximumStoreMiB == 0 {
		result.MaximumStoreMiB = defaults.MaximumStoreMiB
	}
	if result.GC.TriggerRatio == 0 {
		result.GC.TriggerRatio = defaults.GC.TriggerRatio
	}
	if result.GC.TargetRatio == 0 {
		result.GC.TargetRatio = defaults.GC.TargetRatio
	}
	if result.GC.GraceHours == 0 {
		result.GC.GraceHours = defaults.GC.GraceHours
	}
	return result
}

func (c Config) LoomStoreOptions(roots loom.RootProvider) loom.StoreOptions {
	effective := c.EffectiveLoom()
	return loom.StoreOptions{
		MaximumArtifactBytes: int64(effective.MaximumArtifactMiB) << 20,
		MaximumStoreBytes:    int64(effective.MaximumStoreMiB) << 20,
		AutoGC:               !effective.GC.Disabled,
		GCTriggerRatio:       effective.GC.TriggerRatio,
		GCTargetRatio:        effective.GC.TargetRatio,
		GCGracePeriod:        time.Duration(effective.GC.GraceHours) * time.Hour,
		Roots:                roots,
	}
}

// AgentRoles returns the built-in role names accepted in Agents.Roles.
func AgentRoles() []string {
	return append([]string(nil), agentRoles...)
}

func IsAgentRole(role string) bool {
	for _, candidate := range agentRoles {
		if role == candidate {
			return true
		}
	}
	return false
}

// EffectiveAgents fills defaults omitted by older or hand-written configs.
func (c Config) EffectiveAgents() AgentsConfig {
	result := c.Agents
	if result.MaxParallel == 0 {
		result.MaxParallel = DefaultAgentMaxParallel
	}
	if c.Agents.Roles != nil {
		result.Roles = make(map[string]AgentConfig, len(c.Agents.Roles))
		for role, agent := range c.Agents.Roles {
			if strings.TrimSpace(agent.ReasoningEffort) == "" {
				agent.ReasoningEffort = ""
			}
			result.Roles[role] = agent
		}
	}
	if c.Agents.Connections != nil {
		result.Connections = make(map[string]AgentConnectionConfig, len(c.Agents.Connections))
		for id, connection := range c.Agents.Connections {
			connection.Args = append([]string(nil), connection.Args...)
			if connection.Env != nil {
				env := make(map[string]string, len(connection.Env))
				for name, value := range connection.Env {
					env[name] = value
				}
				connection.Env = env
			}
			result.Connections[id] = connection
		}
	}
	return result
}

// EffectiveAgent returns a role's configured model controls with an empty
// model inherited from the active chat model.
func (c Config) EffectiveAgent(role string) (AgentConfig, error) {
	if !c.HasNativeRole(role) {
		return AgentConfig{}, fmt.Errorf("config: unsupported agent role %q", role)
	}
	result := c.EffectiveAgents().Roles[role]
	if result.Model == "" && result.Group == "" {
		result.Model = c.Provider.Model
	}
	return result, nil
}

// ValidCustomName validates user-defined role and profile identifiers.
func ValidCustomName(name string) bool {
	if len(name) == 0 || len(name) > 64 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// ValidCustomRoleName excludes identifiers reserved by the model settings UI
// in addition to applying the shared custom identifier syntax.
func ValidCustomRoleName(name string) bool {
	return ValidCustomName(name) && name != "default" && name != "embedding" &&
		name != AgentRoleSearch && !IsAgentRole(name)
}

func (c Config) HasNativeRole(role string) bool {
	if IsAgentRole(role) {
		return true
	}
	a, ok := c.Agents.Roles[role]
	return ok && ValidCustomRoleName(role) && a.Agent == ""
}

func (c Config) NativeRoles() []string {
	result := AgentRoles()
	var custom []string
	for role := range c.Agents.Roles {
		if !IsAgentRole(role) && c.HasNativeRole(role) {
			custom = append(custom, role)
		}
	}
	sort.Strings(custom)
	return append(result, custom...)
}

// EffectiveModelCandidates resolves a role to an ordered model list. A
// role-level reasoning effort supplies the default for group candidates.
func (c Config) EffectiveModelCandidates(role string) ([]ModelCandidateConfig, error) {
	agent, err := c.EffectiveAgent(role)
	if err != nil {
		return nil, err
	}
	if agent.Group == "" {
		return []ModelCandidateConfig{{Model: agent.Model, ReasoningEffort: agent.ReasoningEffort}}, nil
	}
	group, found := c.ModelGroups[agent.Group]
	if !found {
		return nil, fmt.Errorf("config: agent %q references unknown model group %q", role, agent.Group)
	}
	result := make([]ModelCandidateConfig, len(group.Candidates))
	copy(result, group.Candidates)
	for index := range result {
		if result[index].ReasoningEffort == "" {
			result[index].ReasoningEffort = agent.ReasoningEffort
		}
	}
	return result, nil
}

// EffectiveReasoningEffort treats an empty or whitespace-only effort as the
// provider default. Native roles keep their own independent effort settings.
func (p ProviderConfig) EffectiveReasoningEffort() string {
	return strings.TrimSpace(p.ReasoningEffort)
}

// ResolveAPIKey returns the inline key first, then the configured environment
// variable. The key itself is never included in errors or rendered output.
func (p ProviderConfig) ResolveAPIKey() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	if p.APIKeyEnv != "" {
		return os.Getenv(p.APIKeyEnv)
	}
	return ""
}

type Store struct {
	Dir string
}

func DefaultStore() (Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Store{}, fmt.Errorf("config: find home directory: %w", err)
	}
	return Store{Dir: filepath.Join(home, DirectoryName)}, nil
}

func (s Store) Path() string {
	return filepath.Join(s.Dir, FileName)
}

func (s Store) Load() (Config, error) {
	file, err := os.Open(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: open %s: %w", s.Path(), err)
	}
	defer file.Close()

	var loaded Config
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&loaded); err != nil {
		return Config{}, fmt.Errorf("config: decode %s: %w", s.Path(), err)
	}
	normalizedLSP, err := loaded.LSP.Normalized()
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	loaded.LSP = normalizedLSP
	if err := loaded.Validate(); err != nil {
		return Config{}, err
	}
	return loaded, nil
}

func (s Store) Save(value Config) error {
	value.Version = CurrentVersion
	if value.Provider.EffectiveReasoningEffort() == "" {
		value.Provider.ReasoningEffort = ""
	}
	value.Context = value.EffectiveContext()
	value.Agents = value.EffectiveAgents()
	value.Loom = value.EffectiveLoom()
	normalizedLSP, err := value.LSP.Normalized()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	value.LSP = normalizedLSP
	if err := value.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("config: create %s: %w", s.Dir, err)
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return fmt.Errorf("config: secure %s: %w", s.Dir, err)
	}
	body, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}

	temporary, err := os.CreateTemp(s.Dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("config: create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("config: secure temporary file: %w", err)
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("config: write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("config: sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("config: close temporary file: %w", err)
	}
	if err := replaceFile(temporaryPath, s.Path()); err != nil {
		return fmt.Errorf("config: replace %s: %w", s.Path(), err)
	}
	keep = true
	return nil
}
