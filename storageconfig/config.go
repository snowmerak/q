// Package storageconfig stores user preferences for local vs remote storage
// backends (SQLite vs PostgreSQL, Bleve/HNSW vs OpenSearch) separately from
// service listener and model settings.
package storageconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/internal/strictjson"
)

const (
	Version    = 1
	FileName   = "storage.json"
	ModeLocal  = "local"
	ModeRemote = "remote"

	TargetSkills          = "skills"
	TargetKnowledge       = "knowledge"
	TargetWorkspaceMemory = "workspace_memory"
)

var ErrNotFound = errors.New("storageconfig: config file does not exist")

type Config struct {
	Version int          `json:"version"`
	Mode    string       `json:"mode"`
	Remote  RemoteConfig `json:"remote"`
}

type RemoteConfig struct {
	OpenSearch OpenSearchConfig `json:"opensearch"`
	Postgres   PostgresConfig   `json:"postgres"`
}

type OpenSearchConfig struct {
	Enabled        bool              `json:"enabled"`
	Endpoint       string            `json:"endpoint,omitempty"`
	IndexPrefix    string            `json:"index_prefix,omitempty"`
	Refresh        string            `json:"refresh,omitempty"`
	Username       string            `json:"username,omitempty"`
	PasswordEnv    string            `json:"password_env,omitempty"`
	Password       string            `json:"password,omitempty"`
	BearerTokenEnv string            `json:"bearer_token_env,omitempty"`
	BearerToken    string            `json:"bearer_token,omitempty"`
	Targets        OpenSearchTargets `json:"targets"`
}

type OpenSearchTargets struct {
	Skills          bool `json:"skills"`
	Knowledge       bool `json:"knowledge"`
	WorkspaceMemory bool `json:"workspace_memory"`
}

type PostgresConfig struct {
	Enabled bool   `json:"enabled"`
	DSNEnv  string `json:"dsn_env,omitempty"`
	DSN     string `json:"dsn,omitempty"`
}

// Default returns a local-first configuration with remote targets ready to toggle.
func Default() Config {
	return Config{
		Version: Version,
		Mode:    ModeLocal,
		Remote: RemoteConfig{
			OpenSearch: OpenSearchConfig{
				IndexPrefix: "q",
				Refresh:     "wait_for",
				Targets: OpenSearchTargets{
					Skills:          true,
					Knowledge:       true,
					WorkspaceMemory: true,
				},
			},
		},
	}
}

func (c Config) Effective() Config {
	defaults := Default()
	if c.Version == 0 {
		c.Version = defaults.Version
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode == "" {
		c.Mode = ModeLocal
	}
	c.Remote.OpenSearch.Endpoint = strings.TrimRight(strings.TrimSpace(c.Remote.OpenSearch.Endpoint), "/")
	c.Remote.OpenSearch.IndexPrefix = strings.TrimSpace(c.Remote.OpenSearch.IndexPrefix)
	if c.Remote.OpenSearch.IndexPrefix == "" {
		c.Remote.OpenSearch.IndexPrefix = defaults.Remote.OpenSearch.IndexPrefix
	}
	c.Remote.OpenSearch.Refresh = strings.TrimSpace(c.Remote.OpenSearch.Refresh)
	if c.Remote.OpenSearch.Refresh == "" {
		c.Remote.OpenSearch.Refresh = defaults.Remote.OpenSearch.Refresh
	}
	c.Remote.OpenSearch.Username = strings.TrimSpace(c.Remote.OpenSearch.Username)
	c.Remote.OpenSearch.PasswordEnv = strings.TrimSpace(c.Remote.OpenSearch.PasswordEnv)
	c.Remote.OpenSearch.BearerTokenEnv = strings.TrimSpace(c.Remote.OpenSearch.BearerTokenEnv)
	c.Remote.Postgres.DSNEnv = strings.TrimSpace(c.Remote.Postgres.DSNEnv)
	return c
}

func (c Config) Validate() error {
	c = c.Effective()
	if c.Version != Version {
		return fmt.Errorf("storageconfig: unsupported version %d", c.Version)
	}
	if c.Mode != ModeLocal && c.Mode != ModeRemote {
		return fmt.Errorf("storageconfig: unsupported mode %q", c.Mode)
	}
	if err := validateOpenSearch(c.Remote.OpenSearch); err != nil {
		return err
	}
	if err := validateEnvName("postgres.dsn_env", c.Remote.Postgres.DSNEnv); err != nil {
		return err
	}
	return nil
}

func validateOpenSearch(osCfg OpenSearchConfig) error {
	if osCfg.Endpoint != "" {
		parsed, err := url.Parse(osCfg.Endpoint)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("storageconfig: invalid opensearch HTTP(S) endpoint %q", osCfg.Endpoint)
		}
	}
	switch osCfg.Refresh {
	case "wait_for", "true", "false":
	default:
		return fmt.Errorf("storageconfig: unsupported opensearch refresh policy %q", osCfg.Refresh)
	}
	if err := validateEnvName("opensearch.password_env", osCfg.PasswordEnv); err != nil {
		return err
	}
	if err := validateEnvName("opensearch.bearer_token_env", osCfg.BearerTokenEnv); err != nil {
		return err
	}
	return nil
}

func validateEnvName(field, value string) error {
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, "=\r\n\x00 ") {
		return fmt.Errorf("storageconfig: invalid environment variable name for %s", field)
	}
	return nil
}

func (c Config) ResolveOpenSearchPassword() string {
	return resolveSecret(c.Remote.OpenSearch.PasswordEnv, c.Remote.OpenSearch.Password)
}

func (c Config) ResolveOpenSearchBearerToken() string {
	return resolveSecret(c.Remote.OpenSearch.BearerTokenEnv, c.Remote.OpenSearch.BearerToken)
}

func (c Config) ResolvePostgresDSN() string {
	return resolveSecret(c.Remote.Postgres.DSNEnv, c.Remote.Postgres.DSN)
}

func resolveSecret(envName, inline string) string {
	if name := strings.TrimSpace(envName); name != "" {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return strings.TrimSpace(inline)
}

func (c Config) UseRemotePostgres() bool {
	c = c.Effective()
	return c.Mode == ModeRemote && c.Remote.Postgres.Enabled
}

func (c Config) UseRemoteOpenSearch(target string) bool {
	c = c.Effective()
	if c.Mode != ModeRemote || !c.Remote.OpenSearch.Enabled {
		return false
	}
	switch target {
	case TargetSkills:
		return c.Remote.OpenSearch.Targets.Skills
	case TargetKnowledge:
		return c.Remote.OpenSearch.Targets.Knowledge
	case TargetWorkspaceMemory:
		return c.Remote.OpenSearch.Targets.WorkspaceMemory
	default:
		return false
	}
}

type Store struct{ Dir string }

func (s Store) Path() string { return filepath.Join(s.Dir, FileName) }

func (s Store) Load() (Config, error) {
	file, err := os.Open(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, fmt.Errorf("storageconfig: open config: %w", err)
	}
	defer func() { _ = file.Close() }()
	var value Config
	if err := strictjson.Decode(io.LimitReader(file, 1<<20), &value); err != nil {
		if errors.Is(err, strictjson.ErrMultipleValues) {
			return Config{}, errors.New("storageconfig: config contains multiple JSON values")
		}
		return Config{}, fmt.Errorf("storageconfig: decode config: %w", err)
	}
	value = value.Effective()
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func (s Store) LoadOrDefault() (Config, error) {
	value, err := s.Load()
	if errors.Is(err, ErrNotFound) {
		return Default(), nil
	}
	return value, err
}

func (s Store) Save(value Config) error {
	value = value.Effective()
	if err := value.Validate(); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("storageconfig: encode config: %w", err)
	}
	body = append(body, '\n')
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("storageconfig: create config directory: %w", err)
	}
	if err := fsreplace.WriteFile(s.Path(), body, 0o600); err != nil {
		return fmt.Errorf("storageconfig: save config: %w", err)
	}
	return nil
}
