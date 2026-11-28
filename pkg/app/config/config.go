// Package config manages q v2's global data directory, strict config.json
// loading, secret redaction, validation, and fsnotify hot-reload.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/v2/internal/fsutil"
	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/llm/decisions"
	"github.com/snowmerak/q/v2/pkg/llm/gateway"
)

const (
	Version        = 2
	FileName       = "config.json"
	DataDirEnvName = "Q_DATA_DIR"
)

type Config struct {
	Version    int              `json:"version"`
	Server     ServerConfig     `json:"server"`
	Storage    StorageConfig    `json:"storage"`
	LLM        LLMConfig        `json:"llm"`
	Decisions  decisions.Config `json:"decisions"`
	Embeddings EmbeddingsConfig `json:"embeddings"`
	Tools      ToolsConfig      `json:"tools"`
}

type ServerConfig struct {
	Listen      string   `json:"listen"`
	OpenBrowser bool     `json:"open_browser"`
	CORSOrigins []string `json:"cors_origins,omitempty"`
}

type StorageConfig struct {
	RDB    RDBConfig    `json:"rdb"`
	Search SearchConfig `json:"search"`
	Blob   BlobConfig   `json:"blob"`
}

type RDBConfig struct {
	Driver     string `json:"driver"` // "sqlite" | "postgres" | "mysql"
	SQLitePath string `json:"sqlite_path,omitempty"`
	DSNEnv     string `json:"dsn_env,omitempty"`
}

type SearchConfig struct {
	Driver     string           `json:"driver"` // "bleve" | "opensearch"
	OpenSearch OpenSearchConfig `json:"opensearch,omitempty"`
}

type OpenSearchConfig struct {
	Endpoint       string `json:"endpoint,omitempty"`
	IndexPrefix    string `json:"index_prefix,omitempty"`
	Username       string `json:"username,omitempty"`
	PasswordEnv    string `json:"password_env,omitempty"`
	BearerTokenEnv string `json:"bearer_token_env,omitempty"`
	TimeoutMS      int    `json:"timeout_ms,omitempty"`
}

type BlobConfig struct {
	Driver               string       `json:"driver"` // "local" | "s3"
	MaxUploadBytes       int64        `json:"max_upload_bytes,omitempty"`
	LoomMaxArtifactBytes int64        `json:"loom_max_artifact_bytes,omitempty"`
	LoomMaxStoreBytes    int64        `json:"loom_max_store_bytes,omitempty"`
	LoomGC               LoomGCConfig `json:"loom_gc,omitempty"`
	S3                   S3Config     `json:"s3,omitempty"`
}

type LoomGCConfig struct {
	Disabled     bool    `json:"disabled,omitempty"`
	TriggerRatio float64 `json:"trigger_ratio,omitempty"`
	TargetRatio  float64 `json:"target_ratio,omitempty"`
	GraceHours   int     `json:"grace_hours,omitempty"`
}

type S3Config struct {
	Endpoint     string `json:"endpoint,omitempty"`
	Bucket       string `json:"bucket,omitempty"`
	Region       string `json:"region,omitempty"`
	AccessKeyEnv string `json:"access_key_env,omitempty"`
	SecretKeyEnv string `json:"secret_key_env,omitempty"`
}

type LLMConfig struct {
	ModelCacheRefreshInterval string                           `json:"model_cache_refresh_interval,omitempty"`
	ModelCacheRefreshTimeout  string                           `json:"model_cache_refresh_timeout,omitempty"`
	Providers                 []gateway.ProviderConfig         `json:"providers"`
	Roles                     LLMRolesConfig                   `json:"roles"`
	ModelBehaviors            map[string]gateway.ModelBehavior `json:"model_behaviors,omitempty"`
}

type LLMRolesConfig struct {
	ChatModel             string `json:"chat_model,omitempty"`
	ChatReasoningEffort   string `json:"chat_reasoning_effort,omitempty"`
	MemoryModel           string `json:"memory_model,omitempty"`
	MemoryReasoningEffort string `json:"memory_reasoning_effort,omitempty"`
	RecallPlannerModel    string `json:"recall_planner_model,omitempty"`
}

type EmbeddingsConfig struct {
	Enabled        bool   `json:"enabled"`
	ProviderID     string `json:"provider_id,omitempty"`
	BaseURL        string `json:"base_url,omitempty"`
	APIKeyEnv      string `json:"api_key_env,omitempty"`
	Model          string `json:"model,omitempty"`
	Revision       string `json:"revision,omitempty"`
	Dimensions     int    `json:"dimensions,omitempty"`
	QueryPrefix    string `json:"query_prefix,omitempty"`
	DocumentPrefix string `json:"document_prefix,omitempty"`
	TimeoutMS      int    `json:"timeout_ms,omitempty"`
}

type ToolsConfig struct {
	ApprovalMode     domain.ApprovalMode            `json:"approval_mode"`
	ToolOverrides    map[string]domain.ApprovalMode `json:"tool_overrides,omitempty"`
	AllowedRoots     []string                       `json:"allowed_roots,omitempty"`
	CommandTimeoutMS int                            `json:"command_timeout_ms,omitempty"`
	MaxToolRounds    int                            `json:"max_tool_rounds,omitempty"`
	BrowserControl   BrowserControlConfig           `json:"browser_control"`
	MCPServers       []MCPServerConfig              `json:"mcp_servers,omitempty"`
}

type BrowserControlConfig struct {
	Enabled   bool                           `json:"enabled"`
	Preferred string                         `json:"preferred"` // "cdp-q-driver" | "ego-browser"
	Drivers   map[string]BrowserDriverConfig `json:"drivers,omitempty"`
}

type BrowserDriverConfig struct {
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

type MCPServerConfig struct {
	ID        string            `json:"id"`
	Transport string            `json:"transport"` // "stdio" | "http"
	Enabled   bool              `json:"enabled"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
}

// Default returns a local-first single-instance configuration ready to run.
func Default() Config {
	return Config{
		Version: Version,
		Server: ServerConfig{
			Listen:      "127.0.0.1:17070",
			OpenBrowser: false,
		},
		Storage: StorageConfig{
			RDB: RDBConfig{
				Driver:     "sqlite",
				SQLitePath: "q.sqlite",
			},
			Search: SearchConfig{
				Driver: "bleve",
				OpenSearch: OpenSearchConfig{
					IndexPrefix: "q",
					TimeoutMS:   15000,
				},
			},
			Blob: BlobConfig{
				Driver:               "local",
				MaxUploadBytes:       20 << 20,
				LoomMaxArtifactBytes: 64 << 20,
				LoomMaxStoreBytes:    256 << 20,
				LoomGC: LoomGCConfig{
					Disabled:     false,
					TriggerRatio: 0.80,
					TargetRatio:  0.60,
					GraceHours:   1,
				},
			},
		},
		LLM: LLMConfig{
			ModelCacheRefreshInterval: "15m",
			ModelCacheRefreshTimeout:  "10s",
			Providers:                 []gateway.ProviderConfig{},
		},
		Decisions: decisions.Config{
			Providers: []decisions.ProviderConfig{},
			Models:    []decisions.ModelConfig{},
		},
		Embeddings: EmbeddingsConfig{
			Enabled:   false,
			TimeoutMS: 15000,
		},
		Tools: ToolsConfig{
			ApprovalMode: domain.ApprovalAutoDetection,
			ToolOverrides: map[string]domain.ApprovalMode{
				"remove_path": domain.ApprovalRequest,
				"run_command": domain.ApprovalAutoDetection,
				"read_file":   domain.ApprovalBypass,
			},
			CommandTimeoutMS: 120000,
			MaxToolRounds:    120,
			BrowserControl: BrowserControlConfig{
				Enabled:   true,
				Preferred: "cdp-q-driver",
				Drivers: map[string]BrowserDriverConfig{
					"cdp-q-driver": {Args: []string{"cdp"}},
					"ego-browser":  {Command: "ego-browser"},
				},
			},
			MCPServers: []MCPServerConfig{},
		},
	}
}

// ResolveDataDir selects the global data directory using precedence:
// explicit flag value > Q_DATA_DIR env > ~/.q.
func ResolveDataDir(flagValue string) (string, error) {
	raw := strings.TrimSpace(flagValue)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(DataDirEnvName))
	}
	if raw == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		raw = filepath.Join(home, ".q")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve data dir %q: %w", raw, err)
	}
	return abs, nil
}

// Load reads and strictly validates path. When path does not exist, Default()
// is returned without creating the file.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), nil
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	return Parse(data)
}

// Parse strictly decodes and validates JSON config bytes.
func Parse(data []byte) (Config, error) {
	data, err := stripRemovedLegacyFields(data)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if dec.More() {
		return Config{}, errors.New("decode config: trailing data")
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return Config{}, errors.New("decode config: trailing JSON value")
	}
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func stripRemovedLegacyFields(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if dec.More() {
		return nil, errors.New("decode config: trailing data")
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("decode config: trailing JSON value")
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return data, nil
	}
	changed := false
	if llmMap, ok := obj["llm"].(map[string]any); ok {
		if rolesMap, ok := llmMap["roles"].(map[string]any); ok {
			for _, key := range []string{"subagent_model", "subagent_reasoning_effort"} {
				if _, exists := rolesMap[key]; exists {
					delete(rolesMap, key)
					changed = true
				}
			}
		}
	}
	if toolsMap, ok := obj["tools"].(map[string]any); ok {
		if _, exists := toolsMap["max_delegation_depth"]; exists {
			delete(toolsMap, "max_delegation_depth")
			changed = true
		}
	}
	if !changed {
		return data, nil
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	return out, nil
}

// Save atomically writes cfg as formatted JSON after validation.
func Save(path string, cfg Config) error {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(path, data, 0o600)
}

func (c Config) withDefaults() Config {
	def := Default()
	if c.Version == 0 {
		c.Version = def.Version
	}
	if strings.TrimSpace(c.Server.Listen) == "" {
		c.Server.Listen = def.Server.Listen
	}
	c.Storage.RDB.Driver = strings.ToLower(strings.TrimSpace(c.Storage.RDB.Driver))
	if c.Storage.RDB.Driver == "" {
		c.Storage.RDB.Driver = def.Storage.RDB.Driver
	}
	if c.Storage.RDB.Driver == "sqlite" && strings.TrimSpace(c.Storage.RDB.SQLitePath) == "" {
		c.Storage.RDB.SQLitePath = def.Storage.RDB.SQLitePath
	}
	c.Storage.Search.Driver = strings.ToLower(strings.TrimSpace(c.Storage.Search.Driver))
	if c.Storage.Search.Driver == "" {
		c.Storage.Search.Driver = def.Storage.Search.Driver
	}
	if strings.TrimSpace(c.Storage.Search.OpenSearch.IndexPrefix) == "" {
		c.Storage.Search.OpenSearch.IndexPrefix = def.Storage.Search.OpenSearch.IndexPrefix
	}
	if c.Storage.Search.OpenSearch.TimeoutMS == 0 {
		c.Storage.Search.OpenSearch.TimeoutMS = def.Storage.Search.OpenSearch.TimeoutMS
	}
	c.Storage.Blob.Driver = strings.ToLower(strings.TrimSpace(c.Storage.Blob.Driver))
	if c.Storage.Blob.Driver == "" {
		c.Storage.Blob.Driver = def.Storage.Blob.Driver
	}
	if c.Storage.Blob.MaxUploadBytes == 0 {
		c.Storage.Blob.MaxUploadBytes = def.Storage.Blob.MaxUploadBytes
	}
	if c.Storage.Blob.LoomMaxArtifactBytes == 0 {
		c.Storage.Blob.LoomMaxArtifactBytes = def.Storage.Blob.LoomMaxArtifactBytes
	}
	if c.Storage.Blob.LoomMaxStoreBytes == 0 {
		c.Storage.Blob.LoomMaxStoreBytes = def.Storage.Blob.LoomMaxStoreBytes
	}
	if c.Storage.Blob.LoomGC.TriggerRatio == 0 {
		c.Storage.Blob.LoomGC.TriggerRatio = def.Storage.Blob.LoomGC.TriggerRatio
	}
	if c.Storage.Blob.LoomGC.TargetRatio == 0 {
		c.Storage.Blob.LoomGC.TargetRatio = def.Storage.Blob.LoomGC.TargetRatio
	}
	if c.Storage.Blob.LoomGC.GraceHours == 0 {
		c.Storage.Blob.LoomGC.GraceHours = def.Storage.Blob.LoomGC.GraceHours
	}
	if strings.TrimSpace(c.LLM.ModelCacheRefreshInterval) == "" {
		c.LLM.ModelCacheRefreshInterval = def.LLM.ModelCacheRefreshInterval
	}
	if strings.TrimSpace(c.LLM.ModelCacheRefreshTimeout) == "" {
		c.LLM.ModelCacheRefreshTimeout = def.LLM.ModelCacheRefreshTimeout
	}
	if c.Embeddings.TimeoutMS == 0 {
		c.Embeddings.TimeoutMS = def.Embeddings.TimeoutMS
	}
	if c.Tools.ApprovalMode == "" {
		c.Tools.ApprovalMode = def.Tools.ApprovalMode
	}
	if c.Tools.CommandTimeoutMS == 0 {
		c.Tools.CommandTimeoutMS = def.Tools.CommandTimeoutMS
	}
	if c.Tools.MaxToolRounds == 0 {
		c.Tools.MaxToolRounds = def.Tools.MaxToolRounds
	}
	if strings.TrimSpace(c.Tools.BrowserControl.Preferred) == "" {
		c.Tools.BrowserControl.Preferred = def.Tools.BrowserControl.Preferred
	}
	return c
}

// Validate checks schema invariants without probing external hosts.
func (c Config) Validate() error {
	if c.Version != Version {
		return fmt.Errorf("config: unsupported version %d (want %d)", c.Version, Version)
	}
	switch c.Storage.RDB.Driver {
	case "sqlite":
		if strings.TrimSpace(c.Storage.RDB.SQLitePath) == "" {
			return errors.New("config: storage.rdb.sqlite_path is required for sqlite")
		}
	case "postgres", "mysql":
		if strings.TrimSpace(c.Storage.RDB.DSNEnv) == "" {
			return fmt.Errorf("config: storage.rdb.dsn_env is required for %s", c.Storage.RDB.Driver)
		}
	default:
		return fmt.Errorf("config: unsupported storage.rdb.driver %q", c.Storage.RDB.Driver)
	}

	switch c.Storage.Search.Driver {
	case "bleve":
	case "opensearch":
		if strings.TrimSpace(c.Storage.Search.OpenSearch.Endpoint) == "" {
			return errors.New("config: storage.search.opensearch.endpoint is required")
		}
	default:
		return fmt.Errorf("config: unsupported storage.search.driver %q", c.Storage.Search.Driver)
	}

	switch c.Storage.Blob.Driver {
	case "local":
	case "s3":
		if strings.TrimSpace(c.Storage.Blob.S3.Bucket) == "" {
			return errors.New("config: storage.blob.s3.bucket is required")
		}
	default:
		return fmt.Errorf("config: unsupported storage.blob.driver %q", c.Storage.Blob.Driver)
	}
	if c.Storage.Blob.LoomMaxArtifactBytes < 1 || c.Storage.Blob.LoomMaxArtifactBytes > 1024<<20 {
		return errors.New("config: storage.blob.loom_max_artifact_bytes must be between 1 and 1073741824")
	}
	if c.Storage.Blob.LoomMaxStoreBytes < 1 || c.Storage.Blob.LoomMaxStoreBytes > 10240<<20 {
		return errors.New("config: storage.blob.loom_max_store_bytes must be between 1 and 10737418240")
	}
	if c.Storage.Blob.LoomMaxArtifactBytes > c.Storage.Blob.LoomMaxStoreBytes {
		return errors.New("config: storage.blob.loom_max_artifact_bytes cannot exceed loom_max_store_bytes")
	}
	loomGC := c.Storage.Blob.LoomGC
	if !(loomGC.TargetRatio > 0 && loomGC.TriggerRatio > loomGC.TargetRatio && loomGC.TriggerRatio < 1) {
		return errors.New("config: storage.blob.loom_gc ratios must satisfy 0 < target_ratio < trigger_ratio < 1")
	}
	if loomGC.GraceHours < 1 || loomGC.GraceHours > 24*365 {
		return errors.New("config: storage.blob.loom_gc.grace_hours must be between 1 and 8760")
	}

	if err := c.Decisions.Validate(); err != nil {
		return fmt.Errorf("config: decisions: %w", err)
	}
	if c.Embeddings.Enabled {
		if strings.TrimSpace(c.Embeddings.Model) == "" || c.Embeddings.Dimensions < 1 {
			return errors.New("config: enabled embeddings require model and positive dimensions")
		}
		if pid := strings.TrimSpace(c.Embeddings.ProviderID); pid != "" {
			var found *gateway.ProviderConfig
			for i := range c.LLM.Providers {
				if strings.TrimSpace(c.LLM.Providers[i].ID) == pid {
					found = &c.LLM.Providers[i]
					break
				}
			}
			if found == nil {
				return fmt.Errorf("config: embeddings provider_id %q not found in llm.providers", pid)
			}
			switch found.Type {
			case "openai-compatible", "openrouter", "grok", "xai":
			default:
				return fmt.Errorf("config: embeddings provider_id %q type %q does not support embeddings", pid, found.Type)
			}
		} else if strings.TrimSpace(c.Embeddings.BaseURL) == "" {
			return errors.New("config: enabled embeddings require provider_id or base_url")
		}
	}
	if !c.Tools.ApprovalMode.Valid() {
		return fmt.Errorf("config: invalid tools.approval_mode %q", c.Tools.ApprovalMode)
	}
	for name, mode := range c.Tools.ToolOverrides {
		if strings.TrimSpace(name) == "" || !mode.Valid() {
			return fmt.Errorf("config: invalid tool_overrides entry %q=%q", name, mode)
		}
	}
	if c.Tools.CommandTimeoutMS < 100 {
		return errors.New("config: invalid tools command timeout")
	}
	if c.Tools.MaxToolRounds < 1 || c.Tools.MaxToolRounds > 1000 {
		return errors.New("config: tools.max_tool_rounds must be between 1 and 1000")
	}
	switch c.Tools.BrowserControl.Preferred {
	case "cdp-q-driver", "ego-browser":
	default:
		return fmt.Errorf("config: invalid tools.browser_control.preferred %q", c.Tools.BrowserControl.Preferred)
	}
	seenMCP := make(map[string]struct{}, len(c.Tools.MCPServers))
	for _, srv := range c.Tools.MCPServers {
		id := strings.TrimSpace(srv.ID)
		if id == "" {
			return errors.New("config: mcp_servers entry requires id")
		}
		if _, dup := seenMCP[id]; dup {
			return fmt.Errorf("config: duplicate mcp_servers id %q", id)
		}
		seenMCP[id] = struct{}{}
		switch srv.Transport {
		case "stdio":
			if srv.Enabled && strings.TrimSpace(srv.Command) == "" {
				return fmt.Errorf("config: mcp_servers %q stdio transport requires command", id)
			}
		case "http":
			if srv.Enabled && strings.TrimSpace(srv.URL) == "" {
				return fmt.Errorf("config: mcp_servers %q http transport requires url", id)
			}
		default:
			return fmt.Errorf("config: mcp_servers %q unsupported transport %q", id, srv.Transport)
		}
	}
	for role, effort := range map[string]string{
		"chat_reasoning_effort":   c.LLM.Roles.ChatReasoningEffort,
		"memory_reasoning_effort": c.LLM.Roles.MemoryReasoningEffort,
	} {
		if effort != strings.TrimSpace(effort) {
			return fmt.Errorf("config: llm.roles.%s must not have surrounding whitespace", role)
		}
	}
	if err := gateway.ValidateModelBehaviors(c.LLM.ModelBehaviors); err != nil {
		return fmt.Errorf("config: llm: %w", err)
	}
	return nil
}

// GatewayConfig converts LLMConfig into pkg/llm/gateway.Config.
func (c Config) GatewayConfig() gateway.Config {
	var behaviors map[string]gateway.ModelBehavior
	if len(c.LLM.ModelBehaviors) > 0 {
		behaviors = make(map[string]gateway.ModelBehavior, len(c.LLM.ModelBehaviors))
		for k, v := range c.LLM.ModelBehaviors {
			if v.Sampling != nil {
				s := *v.Sampling
				v.Sampling = &s
			}
			behaviors[k] = v
		}
	}
	return gateway.Config{
		ModelCacheRefreshInterval: c.LLM.ModelCacheRefreshInterval,
		ModelCacheRefreshTimeout:  c.LLM.ModelCacheRefreshTimeout,
		Providers:                 append([]gateway.ProviderConfig(nil), c.LLM.Providers...),
		ModelBehaviors:            behaviors,
	}
}

// DiffRestartRequired reports top-level sections that require process restart.
func DiffRestartRequired(prev, next Config) []string {
	var fields []string
	if prev.Server.Listen != next.Server.Listen {
		fields = append(fields, "server.listen")
	}
	if prev.Storage.RDB != next.Storage.RDB {
		fields = append(fields, "storage.rdb")
	}
	if prev.Storage.Blob.Driver != next.Storage.Blob.Driver || prev.Storage.Blob.S3 != next.Storage.Blob.S3 {
		fields = append(fields, "storage.blob")
	}
	return fields
}

// Redact returns a copy of c stripped of any inline secret values.
func (c Config) Redact() Config {
	out := c
	if len(c.LLM.Providers) > 0 {
		out.LLM.Providers = make([]gateway.ProviderConfig, len(c.LLM.Providers))
		for i, p := range c.LLM.Providers {
			p.APIKey = ""
			p.Codex.ConversationCache.Redis.Password = ""
			out.LLM.Providers[i] = p
		}
	}
	return out
}
