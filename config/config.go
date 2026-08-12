// Package config stores q's personal application configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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

	AgentRoleGriller  = "griller"
	AgentRoleScout    = "scout"
	AgentRoleResearch = "research"
	AgentRolePlanner  = "planner"
	AgentRoleCoder    = "coder"
	AgentRoleCommit   = "commit"
	AgentRoleAdvisor  = "advisor"
)

var ErrNotFound = errors.New("q config not found")

type Config struct {
	Version   int              `yaml:"version"`
	Provider  ProviderConfig   `yaml:"provider"`
	Embedding EmbeddingConfig  `yaml:"embedding,omitempty"`
	Context   ContextConfig    `yaml:"context,omitempty"`
	Agents    AgentsConfig     `yaml:"agents,omitempty"`
	Loom      LoomConfig       `yaml:"loom,omitempty"`
	LSP       lsp.GlobalConfig `yaml:"lsp,omitempty"`
}

type ProviderConfig struct {
	Managed       bool   `yaml:"managed,omitempty"`
	Type          string `yaml:"type"`
	BaseURL       string `yaml:"base_url"`
	Model         string `yaml:"model"`
	APIKeyEnv     string `yaml:"api_key_env,omitempty"`
	APIKey        string `yaml:"api_key,omitempty"`
	SystemPrompt  string `yaml:"system_prompt,omitempty"`
	ContextWindow int64  `yaml:"context_window,omitempty"`
}

type ContextConfig struct {
	Window       int64   `yaml:"window,omitempty"`
	TriggerRatio float64 `yaml:"trigger_ratio,omitempty"`
	TargetRatio  float64 `yaml:"target_ratio,omitempty"`
	RecentRatio  float64 `yaml:"recent_ratio,omitempty"`
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

// EmbeddingConfig selects the model and fixed vector size used by the
// workspace archive. A zero value disables embedding generation.
type EmbeddingConfig struct {
	Model      string `yaml:"model,omitempty"`
	Dimensions int    `yaml:"dimensions,omitempty"`
}

type AgentsConfig struct {
	MaxParallel int                    `yaml:"max_parallel,omitempty"`
	Roles       map[string]AgentConfig `yaml:"roles,omitempty"`
}

// AgentConfig selects the model controls for one subagent role. An empty
// Model inherits the active chat model. An empty ReasoningEffort leaves the
// model/provider default unchanged.
type AgentConfig struct {
	Model           string `yaml:"model,omitempty"`
	ReasoningEffort string `yaml:"reasoning_effort,omitempty"`
}

var agentRoles = []string{
	AgentRoleGriller,
	AgentRoleScout,
	AgentRoleResearch,
	AgentRolePlanner,
	AgentRoleCoder,
	AgentRoleCommit,
	AgentRoleAdvisor,
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
	for role, agent := range c.Agents.Roles {
		if !IsAgentRole(role) {
			return fmt.Errorf("config: unsupported agent role %q", role)
		}
		if agent.Model != strings.TrimSpace(agent.Model) {
			return fmt.Errorf("config: agent %q model must not have surrounding whitespace", role)
		}
		trimmedEffort := strings.TrimSpace(agent.ReasoningEffort)
		if trimmedEffort != "" && agent.ReasoningEffort != trimmedEffort {
			return fmt.Errorf("config: agent %q reasoning_effort must not have surrounding whitespace", role)
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
	return result
}

// EffectiveAgent returns a role's configured model controls with an empty
// model inherited from the active chat model.
func (c Config) EffectiveAgent(role string) (AgentConfig, error) {
	if !IsAgentRole(role) {
		return AgentConfig{}, fmt.Errorf("config: unsupported agent role %q", role)
	}
	result := c.EffectiveAgents().Roles[role]
	if result.Model == "" {
		result.Model = c.Provider.Model
	}
	return result, nil
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
