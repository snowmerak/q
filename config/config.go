// Package config stores q's personal application configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	CurrentVersion = 1
	DirectoryName  = ".q"
	FileName       = "config.yaml"
)

var ErrNotFound = errors.New("q config not found")

type Config struct {
	Version  int            `yaml:"version"`
	Provider ProviderConfig `yaml:"provider"`
}

type ProviderConfig struct {
	Type         string `yaml:"type"`
	BaseURL      string `yaml:"base_url"`
	Model        string `yaml:"model"`
	APIKeyEnv    string `yaml:"api_key_env,omitempty"`
	APIKey       string `yaml:"api_key,omitempty"`
	SystemPrompt string `yaml:"system_prompt,omitempty"`
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
	}
}

func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("config: unsupported version %d", c.Version)
	}
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
	if strings.TrimSpace(c.Provider.Model) == "" {
		return fmt.Errorf("config: provider model is required")
	}
	if strings.Contains(c.Provider.APIKeyEnv, "=") {
		return fmt.Errorf("config: api_key_env must be an environment variable name")
	}
	return nil
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
	if err := loaded.Validate(); err != nil {
		return Config{}, err
	}
	return loaded, nil
}

func (s Store) Save(value Config) error {
	value.Version = CurrentVersion
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
