// Package library provides q's user-level Agent Skill and proposition service.
package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/gatewayconfig"
)

const (
	ConfigVersion       = 1
	ConfigFileName      = "library.json"
	LockFileName        = "library.lock"
	DefaultHost         = "127.0.0.1"
	DefaultPort         = 17891
	defaultProbeTimeout = 300
)

type Config struct {
	Version   int    `json:"version"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	ProbeHost string `json:"probe_host,omitempty"`
	// Deprecated: retained only so pre-local-only config files still decode.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// Deprecated: retained only so pre-local-only config files still decode.
	APIKeys []gatewayconfig.APIKey `json:"api_keys,omitempty"`
}

func DefaultConfig() Config {
	return Config{Version: ConfigVersion, Host: DefaultHost, Port: DefaultPort}
}

func (c Config) Effective() Config {
	// Authentication fields from older config files are intentionally dropped.
	c.APIKeyEnv = ""
	c.APIKeys = nil
	defaults := DefaultConfig()
	if c.Version == 0 {
		c.Version = defaults.Version
	}
	if strings.TrimSpace(c.Host) == "" {
		c.Host = defaults.Host
	}
	if c.Port == 0 {
		c.Port = defaults.Port
	}
	return c
}

func (c Config) Validate() error {
	c = c.Effective()
	if c.Version != ConfigVersion {
		return fmt.Errorf("library: unsupported config version %d", c.Version)
	}
	host := net.ParseIP(c.Host)
	if host == nil || !host.IsLoopback() {
		return fmt.Errorf("library: host %q must be a loopback IP address", c.Host)
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("library: port must be between 1 and 65535")
	}
	if c.ProbeHost != "" {
		probe := net.ParseIP(c.ProbeHost)
		if probe == nil || !probe.IsLoopback() {
			return fmt.Errorf("library: probe_host %q must be a loopback IP address", c.ProbeHost)
		}
	}
	return nil
}

func (c Config) ListenAddress() string {
	c = c.Effective()
	return net.JoinHostPort(c.Host, fmt.Sprintf("%d", c.Port))
}

func (c Config) Endpoint() string {
	c = c.Effective()
	host := c.ProbeHost
	if host == "" {
		host = c.Host
	}
	return "http://" + net.JoinHostPort(host, fmt.Sprintf("%d", c.Port)) + "/v1"
}

type ConfigStore struct{ Dir string }

func (s ConfigStore) Path() string { return filepath.Join(s.Dir, ConfigFileName) }

func (s ConfigStore) LoadOrDefault() (Config, error) {
	file, err := os.Open(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("library: open config: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var value Config
	if err := decoder.Decode(&value); err != nil {
		return Config{}, fmt.Errorf("library: decode config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("library: config contains multiple JSON values")
		}
		return Config{}, fmt.Errorf("library: decode config: %w", err)
	}
	value = value.Effective()
	// Older releases allowed wildcard and non-loopback listeners. Localize
	// those valid legacy IPs during load so upgrading cannot reopen the service
	// externally or leave the Library unavailable solely because of that old
	// setting. Newly saved configurations are still validated strictly.
	if host := net.ParseIP(value.Host); host != nil && !host.IsLoopback() {
		value.Host = DefaultHost
	}
	if probe := net.ParseIP(value.ProbeHost); probe != nil && !probe.IsLoopback() {
		value.ProbeHost = ""
	}
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func (s ConfigStore) Save(value Config) error {
	value = value.Effective()
	if err := value.Validate(); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("library: encode config: %w", err)
	}
	body = append(body, '\n')
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("library: create config directory: %w", err)
	}
	file, err := os.CreateTemp(s.Dir, ".library-*.json")
	if err != nil {
		return fmt.Errorf("library: create temporary config: %w", err)
	}
	temporary := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("library: secure temporary config: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		return fmt.Errorf("library: write temporary config: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("library: sync temporary config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("library: close temporary config: %w", err)
	}
	if err := replaceFile(temporary, s.Path()); err != nil {
		return fmt.Errorf("library: replace config: %w", err)
	}
	keep = true
	return nil
}
