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
	"github.com/snowmerak/q/internal/fsreplace"
)

const (
	FileName     = "systemone.json"
	DefaultURI   = "https://api.typesafe.ai/v1/systemone"
	DefaultModel = "jev-latest"
	Version      = 1
)

type ServerConfig struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	APIKey string `json:"api_key,omitempty"`
}

type ProviderConfig struct {
	ID        string `json:"id"`
	URI       string `json:"uri"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	Model     string `json:"model"`
}

type Config struct {
	Version   int              `json:"version"`
	Server    ServerConfig     `json:"server"`
	Selected  string           `json:"selected"`
	Providers []ProviderConfig `json:"providers"`
}

func Default() Config {
	return Config{
		Version:  Version,
		Server:   ServerConfig{Host: "127.0.0.1"},
		Selected: "typesafe",
		Providers: []ProviderConfig{{
			ID: "typesafe", URI: DefaultURI, APIKeyEnv: "TYPESAFE_API_KEY", Model: DefaultModel,
		}},
	}
}

func (c Config) Validate() error {
	if c.Version != Version {
		return fmt.Errorf("systemone: unsupported config version %d", c.Version)
	}
	address := net.ParseIP(c.Server.Host)
	if address == nil {
		return errors.New("systemone: server host must be an IP address")
	}
	if c.Server.Port < 0 || c.Server.Port > 65535 {
		return errors.New("systemone: server port must be between 0 and 65535")
	}
	if strings.ContainsAny(c.Server.APIKey, "\r\n") {
		return errors.New("systemone: server API key must be a single line")
	}
	if !address.IsLoopback() && c.Server.APIKey == "" {
		return errors.New("systemone: a server API key is required outside loopback")
	}
	if len(c.Providers) == 0 {
		return errors.New("systemone: at least one provider is required")
	}
	seen := make(map[string]bool, len(c.Providers))
	selected := false
	for _, provider := range c.Providers {
		if err := provider.Validate(); err != nil {
			return err
		}
		if seen[provider.ID] {
			return fmt.Errorf("systemone: duplicate provider ID %q", provider.ID)
		}
		seen[provider.ID] = true
		selected = selected || provider.ID == c.Selected
	}
	if !selected {
		return fmt.Errorf("systemone: selected provider %q does not exist", c.Selected)
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
	if p.Model == "" || p.Model != strings.TrimSpace(p.Model) || strings.ContainsAny(p.Model, "\r\n") {
		return fmt.Errorf("systemone: provider %q model is required without surrounding whitespace", p.ID)
	}
	if strings.ContainsAny(p.APIKey, "\r\n") || strings.ContainsAny(p.APIKeyEnv, "\r\n") {
		return fmt.Errorf("systemone: provider %q API key settings must be single lines", p.ID)
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
	if p.APIKey != "" {
		return p.APIKey
	}
	return os.Getenv(p.APIKeyEnv)
}

func (p ProviderConfig) NewClient() (*systemone.Client, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	key := p.ResolveAPIKey()
	if key == "" {
		return nil, fmt.Errorf("systemone: provider %q requires an API key or a populated API key environment variable", p.ID)
	}
	return systemone.New(systemone.Config{BaseURL: p.BaseURL(), APIKey: key})
}

func (c Config) SelectedProvider() (ProviderConfig, error) {
	for _, provider := range c.Providers {
		if provider.ID == c.Selected {
			return provider, nil
		}
	}
	return ProviderConfig{}, fmt.Errorf("systemone: selected provider %q does not exist", c.Selected)
}

func (c Config) NewClient() (*systemone.Client, error) {
	provider, err := c.SelectedProvider()
	if err != nil {
		return nil, err
	}
	return provider.NewClient()
}

type Store struct{ Dir string }

func (s Store) Path() string { return filepath.Join(s.Dir, FileName) }

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
			URI    string `json:"uri"`
			APIKey string `json:"api_key"`
			Model  string `json:"model"`
		}
		if err := json.Unmarshal(data, &old); err != nil {
			return Config{}, fmt.Errorf("systemone: decode legacy settings: %w", err)
		}
		value := Default()
		value.Providers[0].URI = old.URI
		value.Providers[0].APIKey = old.APIKey
		value.Providers[0].Model = old.Model
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
	value, err := s.LoadOrDefault()
	if err != nil {
		return nil, "", err
	}
	provider, err := value.SelectedProvider()
	if err != nil {
		return nil, "", err
	}
	client, err := provider.NewClient()
	return client, provider.Model, err
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
