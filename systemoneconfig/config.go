// Package systemoneconfig stores the independent System One endpoint settings.
package systemoneconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/internal/fsreplace"
)

const (
	FileName   = "systemone.json"
	DefaultURI = "https://api.typesafe.ai/v1/systemone"
)

type Config struct {
	URI    string `json:"uri"`
	APIKey string `json:"api_key,omitempty"`
	Model  string `json:"model"`
}

func Default() Config {
	return Config{URI: DefaultURI, Model: "jev-latest"}
}

func (c Config) Validate() error {
	parsed, err := url.Parse(c.URI)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Opaque != "" || !strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/systemone") {
		return errors.New("systemone: URI must be an HTTP(S) /systemone endpoint without credentials, query, or fragment")
	}
	if c.Model == "" || c.Model != strings.TrimSpace(c.Model) || strings.ContainsAny(c.Model, "\r\n") {
		return errors.New("systemone: model is required without surrounding whitespace")
	}
	if strings.ContainsAny(c.APIKey, "\r\n") {
		return errors.New("systemone: API key must be a single line")
	}
	return nil
}

// BaseURL converts the configured endpoint URI to the native client's root.
func (c Config) BaseURL() string {
	parsed, err := url.Parse(c.URI)
	if err != nil || parsed == nil {
		return ""
	}
	parsed.Path = strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), "/systemone")
	parsed.RawPath = ""
	return parsed.String()
}

// ResolveAPIKey prefers the saved key and otherwise reads TYPESAFE_API_KEY.
func (c Config) ResolveAPIKey() string {
	if c.APIKey != "" {
		return c.APIKey
	}
	return os.Getenv("TYPESAFE_API_KEY")
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
	value := Default()
	if err := json.Unmarshal(data, &value); err != nil {
		return Config{}, fmt.Errorf("systemone: decode settings: %w", err)
	}
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

// NewClient loads the saved endpoint and returns the selected model for
// requests. A blank saved key falls back to TYPESAFE_API_KEY.
func (s Store) NewClient() (*systemone.Client, string, error) {
	value, err := s.LoadOrDefault()
	if err != nil {
		return nil, "", err
	}
	client, err := systemone.New(systemone.Config{BaseURL: value.BaseURL(), APIKey: value.ResolveAPIKey()})
	if err != nil {
		return nil, "", err
	}
	return client, value.Model, nil
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
