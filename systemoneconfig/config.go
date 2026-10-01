// Package systemoneconfig stores the independent System One providers and server settings.
package systemoneconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/internal/authkey"
	"github.com/snowmerak/q/internal/fsreplace"
)

const (
	FileName               = "systemone.json"
	MasterKeyName          = "systemone.key"
	DefaultURI             = "https://api.typesafe.ai/v1/systemone"
	DefaultModel           = "jev-latest"
	Version                = 2
	RoleAgentSkillDecision = "agent_skill_decision"
	RoleArchiveDecision    = "archive_decision"
)

type ServerConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// APIKey is retained for existing settings. New keys are stored in Config.APIKeys.
	APIKey string `json:"api_key,omitempty"`
}

type APIKey = authkey.Record

type ProviderConfig struct {
	ID        string `json:"id"`
	URI       string `json:"uri"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

type Config struct {
	Version      int               `json:"version"`
	Server       ServerConfig      `json:"server"`
	APIKeys      []APIKey          `json:"api_keys,omitempty"`
	Providers    []ProviderConfig  `json:"providers"`
	DefaultModel string            `json:"default_model"`
	RoleModels   map[string]string `json:"role_models,omitempty"`
}

func Default() Config {
	return Config{
		Version:      Version,
		Server:       ServerConfig{Host: "127.0.0.1"},
		DefaultModel: "typesafe/" + DefaultModel,
		Providers: []ProviderConfig{{
			ID: "typesafe", URI: DefaultURI, APIKeyEnv: "TYPESAFE_API_KEY",
		}},
	}
}

func (c Config) Validate() error {
	if c.Version != Version {
		return fmt.Errorf("systemone: unsupported config version %d", c.Version)
	}
	if net.ParseIP(c.Server.Host) == nil {
		return errors.New("systemone: server host must be an IP address")
	}
	if c.Server.Port < 0 || c.Server.Port > 65535 {
		return errors.New("systemone: server port must be between 0 and 65535")
	}
	if strings.ContainsAny(c.Server.APIKey, "\r\n") {
		return errors.New("systemone: server API key must be a single line")
	}
	if err := authkey.ValidateRecords(c.APIKeys, "systemone"); err != nil {
		return err
	}
	if len(c.Providers) == 0 {
		return errors.New("systemone: at least one provider is required")
	}
	seen := make(map[string]bool, len(c.Providers))
	for _, provider := range c.Providers {
		if err := provider.Validate(); err != nil {
			return err
		}
		if seen[provider.ID] {
			return fmt.Errorf("systemone: duplicate provider ID %q", provider.ID)
		}
		seen[provider.ID] = true
	}
	if err := validateModelReference(c.DefaultModel, seen); err != nil {
		return fmt.Errorf("systemone: default model: %w", err)
	}
	for role, model := range c.RoleModels {
		if role == "" || strings.ContainsAny(role, " \t\r\n") {
			return fmt.Errorf("systemone: invalid role %q", role)
		}
		if err := validateModelReference(model, seen); err != nil {
			return fmt.Errorf("systemone: role %q model: %w", role, err)
		}
	}
	return nil
}

func validateModelReference(model string, providers map[string]bool) error {
	id, name, qualified := strings.Cut(model, "/")
	if !qualified || !providers[id] || name == "" || strings.TrimSpace(model) != model || strings.ContainsAny(model, "\r\n") {
		return fmt.Errorf("%q must name a configured provider and model as provider-id/model-name", model)
	}
	return nil
}

func (p ProviderConfig) Validate() error {
	if p.ID == "" || strings.ContainsAny(p.ID, "/ \t\r\n") {
		return errors.New("systemone: provider ID must be nonempty and contain no slash or whitespace")
	}
	parsed, err := url.Parse(p.URI)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Opaque != "" || !strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/systemone") {
		return fmt.Errorf("systemone: provider %q URI must be an HTTP(S) /systemone endpoint without credentials, query, or fragment", p.ID)
	}
	if strings.ContainsAny(p.APIKeyEnv, "\r\n") {
		return fmt.Errorf("systemone: provider %q API key environment variable must be a single line", p.ID)
	}
	return nil
}

func (p ProviderConfig) BaseURL() string {
	parsed, err := url.Parse(p.URI)
	if err != nil || parsed == nil {
		return ""
	}
	parsed.Path = strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), "/systemone")
	parsed.RawPath = ""
	return parsed.String()
}

func (p ProviderConfig) ResolveAPIKey() string {
	return os.Getenv(p.APIKeyEnv)
}

func (p ProviderConfig) NewClient() (*systemone.Client, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return systemone.New(systemone.Config{BaseURL: p.BaseURL(), APIKey: p.ResolveAPIKey()})
}

// ModelForRole returns the role override, or the representative model.
func (c Config) ModelForRole(role string) string {
	if model := c.RoleModels[role]; model != "" {
		return model
	}
	return c.DefaultModel
}

// ResolveModel locates the provider and native model for an assignment.
func (c Config) ResolveModel(role string) (ProviderConfig, string, error) {
	id, model, qualified := strings.Cut(c.ModelForRole(role), "/")
	if !qualified || model == "" {
		return ProviderConfig{}, "", errors.New("systemone: model assignment must use provider-id/model-name")
	}
	for _, provider := range c.Providers {
		if provider.ID == id {
			return provider, model, nil
		}
	}
	return ProviderConfig{}, "", fmt.Errorf("systemone: provider %q does not exist", id)
}

func (c Config) NewClient() (*systemone.Client, error) {
	client, _, err := c.NewClientForRole("")
	return client, err
}

// NewClientForRole returns a native client and model for the role assignment.
func (c Config) NewClientForRole(role string) (*systemone.Client, string, error) {
	provider, model, err := c.ResolveModel(role)
	if err != nil {
		return nil, "", err
	}
	client, err := provider.NewClient()
	return client, model, err
}

type Store struct{ Dir string }

func (s Store) Path() string { return filepath.Join(s.Dir, FileName) }

func (s Store) MasterKeyPath() string { return filepath.Join(s.Dir, MasterKeyName) }

func (s Store) LoadOrDefault() (Config, error) {
	data, err := os.ReadFile(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("systemone: read settings: %w", err)
	}
	var shape struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(data, &shape); err != nil {
		return Config{}, fmt.Errorf("systemone: decode settings: %w", err)
	}
	if shape.URI != "" {
		// Files written by the original single-provider screen remain readable.
		var old struct {
			URI   string `json:"uri"`
			Model string `json:"model"`
		}
		if err := json.Unmarshal(data, &old); err != nil {
			return Config{}, fmt.Errorf("systemone: decode legacy settings: %w", err)
		}
		value := Default()
		value.Providers[0].URI = old.URI
		value.DefaultModel = "typesafe/" + old.Model
		if err := value.Validate(); err != nil {
			return Config{}, err
		}
		return value, nil
	}
	var version struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &version); err != nil {
		return Config{}, fmt.Errorf("systemone: decode settings: %w", err)
	}
	if version.Version == 1 {
		// Migrate the earlier multi-provider schema, which assigned one model per provider.
		var old struct {
			Server    ServerConfig `json:"server"`
			Selected  string       `json:"selected"`
			Providers []struct {
				ProviderConfig
				Model string `json:"model"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(data, &old); err != nil {
			return Config{}, fmt.Errorf("systemone: decode previous settings: %w", err)
		}
		value := Config{Version: Version, Server: old.Server}
		for _, provider := range old.Providers {
			value.Providers = append(value.Providers, provider.ProviderConfig)
			if provider.ID == old.Selected {
				value.DefaultModel = provider.ID + "/" + provider.Model
			}
		}
		if err := value.Validate(); err != nil {
			return Config{}, err
		}
		return value, nil
	}
	var value Config
	if err := json.Unmarshal(data, &value); err != nil {
		return Config{}, fmt.Errorf("systemone: decode settings: %w", err)
	}
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func (s Store) NewClient() (*systemone.Client, string, error) {
	return s.NewClientForRole("")
}

func (s Store) NewClientForRole(role string) (*systemone.Client, string, error) {
	value, err := s.LoadOrDefault()
	if err != nil {
		return nil, "", err
	}
	return value.NewClientForRole(role)
}

func (s Store) Save(value Config) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("systemone: create settings directory: %w", err)
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return fmt.Errorf("systemone: secure settings directory: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("systemone: encode settings: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(s.Dir, ".systemone-*.json")
	if err != nil {
		return fmt.Errorf("systemone: create temporary settings: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("systemone: secure temporary settings: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("systemone: write settings: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("systemone: sync settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("systemone: close settings: %w", err)
	}
	if err := fsreplace.Replace(temporaryPath, s.Path()); err != nil {
		return fmt.Errorf("systemone: replace settings: %w", err)
	}
	return nil
}
