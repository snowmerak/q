package usagelog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const (
	ConfigVersion       = 1
	ConfigFileName      = "usage.json"
	ServiceLockFileName = "usage-service.lock"
	DefaultHost         = "127.0.0.1"
	DefaultPort         = 17893
)

type Config struct {
	Version   int    `json:"version"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	ProbeHost string `json:"probe_host,omitempty"`
}

func DefaultConfig() Config {
	return Config{Version: ConfigVersion, Host: DefaultHost, Port: DefaultPort}
}

func (c Config) Effective() Config {
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
		return fmt.Errorf("usage: unsupported config version %d", c.Version)
	}
	host := net.ParseIP(c.Host)
	if host == nil || !host.IsLoopback() {
		return fmt.Errorf("usage: host %q must be a loopback IP address", c.Host)
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("usage: port must be between 1 and 65535")
	}
	if c.ProbeHost != "" {
		probe := net.ParseIP(c.ProbeHost)
		if probe == nil || !probe.IsLoopback() {
			return fmt.Errorf("usage: probe_host %q must be a loopback IP address", c.ProbeHost)
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
	return "http://" + net.JoinHostPort(host, fmt.Sprintf("%d", c.Port))
}

type ConfigStore struct{ Dir string }

func (s ConfigStore) Path() string { return filepath.Join(s.Dir, ConfigFileName) }

func (s ConfigStore) LoadOrDefault() (Config, error) {
	file, err := os.Open(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("usage: open config: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var value Config
	if err := decoder.Decode(&value); err != nil {
		return Config{}, fmt.Errorf("usage: decode config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("usage: config contains multiple JSON values")
		}
		return Config{}, fmt.Errorf("usage: decode config: %w", err)
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
		return fmt.Errorf("usage: encode config: %w", err)
	}
	body = append(body, '\n')
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("usage: create config directory: %w", err)
	}
	temporary, err := os.CreateTemp(s.Dir, ".usage-*.json")
	if err != nil {
		return err
	}
	path := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFile(path, s.Path()); err != nil {
		return fmt.Errorf("usage: replace config: %w", err)
	}
	keep = true
	return nil
}
