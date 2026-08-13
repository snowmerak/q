// Package library provides q's user-level Agent Skill and proposition service.
package library

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/snowmerak/q/gatewayconfig"
)

const (
	ConfigVersion       = 1
	ConfigFileName      = "library.json"
	MasterKeyFileName   = "library.key"
	LockFileName        = "library.lock"
	DefaultHost         = "127.0.0.1"
	DefaultPort         = 17891
	DefaultAPIKeyEnv    = "Q_LIBRARY_API_KEY"
	defaultProbeTimeout = 300
)

type Config struct {
	Version   int                    `json:"version"`
	Host      string                 `json:"host"`
	Port      int                    `json:"port"`
	ProbeHost string                 `json:"probe_host,omitempty"`
	APIKeyEnv string                 `json:"api_key_env,omitempty"`
	APIKeys   []gatewayconfig.APIKey `json:"api_keys,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		Version: ConfigVersion, Host: DefaultHost, Port: DefaultPort,
		APIKeyEnv: DefaultAPIKeyEnv,
	}
}

func (c Config) Effective() Config {
	c.APIKeys = append([]gatewayconfig.APIKey(nil), c.APIKeys...)
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
	if strings.TrimSpace(c.APIKeyEnv) == "" {
		c.APIKeyEnv = defaults.APIKeyEnv
	}
	return c
}

func (c Config) Validate() error {
	c = c.Effective()
	if c.Version != ConfigVersion {
		return fmt.Errorf("library: unsupported config version %d", c.Version)
	}
	if net.ParseIP(c.Host) == nil {
		return fmt.Errorf("library: host %q is not an IP address", c.Host)
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("library: port must be between 1 and 65535")
	}
	if c.ProbeHost != "" && net.ParseIP(c.ProbeHost) == nil {
		return fmt.Errorf("library: probe_host %q is not an IP address", c.ProbeHost)
	}
	if strings.Contains(c.APIKeyEnv, "=") {
		return errors.New("library: api_key_env must be an environment variable name")
	}
	if err := authenticationConfig(c).Validate(); err != nil {
		return fmt.Errorf("library: invalid API-key configuration: %w", err)
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
		if host == "0.0.0.0" {
			host = "127.0.0.1"
		} else if host == "::" {
			host = "::1"
		}
	}
	return "http://" + net.JoinHostPort(host, fmt.Sprintf("%d", c.Port)) + "/v1"
}

func (c Config) ResolveAPIKey() string {
	if c.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(c.APIKeyEnv)
}

type ConfigStore struct{ Dir string }

func (s ConfigStore) Path() string { return filepath.Join(s.Dir, ConfigFileName) }

func (s ConfigStore) MasterKeyPath() string { return filepath.Join(s.Dir, MasterKeyFileName) }

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

func (s ConfigStore) LoadMasterKey() ([32]byte, error) {
	var result [32]byte
	body, err := os.ReadFile(s.MasterKeyPath())
	if err != nil {
		return result, fmt.Errorf("library: read API-key master: %w", err)
	}
	if len(body) != len(result) {
		return result, fmt.Errorf("library: API-key master must be %d bytes", len(result))
	}
	copy(result[:], body)
	return result, nil
}

func (s ConfigStore) EnsureMasterKey(randomMaster [32]byte) ([32]byte, error) {
	if existing, err := s.LoadMasterKey(); err == nil {
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return [32]byte{}, err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return [32]byte{}, fmt.Errorf("library: create config directory: %w", err)
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return [32]byte{}, fmt.Errorf("library: secure config directory: %w", err)
	}
	file, err := os.OpenFile(s.MasterKeyPath(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return s.LoadMasterKey()
	}
	if err != nil {
		return [32]byte{}, fmt.Errorf("library: create API-key master: %w", err)
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(s.MasterKeyPath())
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return [32]byte{}, fmt.Errorf("library: secure API-key master: %w", err)
	}
	if _, err := file.Write(randomMaster[:]); err != nil {
		return [32]byte{}, fmt.Errorf("library: write API-key master: %w", err)
	}
	if err := file.Sync(); err != nil {
		return [32]byte{}, fmt.Errorf("library: sync API-key master: %w", err)
	}
	if err := file.Close(); err != nil {
		return [32]byte{}, fmt.Errorf("library: close API-key master: %w", err)
	}
	keep = true
	return randomMaster, nil
}

func (s ConfigStore) CreateAPIKey(value Config, alias string, now time.Time) (Config, gatewayconfig.GeneratedAPIKey, error) {
	var randomMaster [32]byte
	if _, err := rand.Read(randomMaster[:]); err != nil {
		return Config{}, gatewayconfig.GeneratedAPIKey{}, fmt.Errorf("library: generate API-key master: %w", err)
	}
	master, err := s.EnsureMasterKey(randomMaster)
	if err != nil {
		return Config{}, gatewayconfig.GeneratedAPIKey{}, err
	}
	generated, err := gatewayconfig.GenerateAPIKey(master, alias, now)
	if err != nil {
		return Config{}, gatewayconfig.GeneratedAPIKey{}, err
	}
	authentication, err := gatewayconfig.AddAPIKey(authenticationConfig(value), generated)
	if err != nil {
		return Config{}, gatewayconfig.GeneratedAPIKey{}, err
	}
	updated := value.Effective()
	updated.APIKeys = authentication.APIKeys
	if err := s.Save(updated); err != nil {
		return Config{}, gatewayconfig.GeneratedAPIKey{}, err
	}
	return updated, generated, nil
}

func (s ConfigStore) RevokeAPIKey(value Config, id string, now time.Time) (Config, error) {
	authentication, err := gatewayconfig.RevokeAPIKey(authenticationConfig(value), id, now)
	if err != nil {
		return Config{}, err
	}
	updated := value.Effective()
	updated.APIKeys = authentication.APIKeys
	if err := s.Save(updated); err != nil {
		return Config{}, err
	}
	return updated, nil
}

func authenticationConfig(value Config) gatewayconfig.Config {
	return gatewayconfig.Config{
		Version: gatewayconfig.CurrentVersion,
		Server:  gatewayconfig.Default().Server,
		APIKeys: append([]gatewayconfig.APIKey(nil), value.APIKeys...),
	}
}
