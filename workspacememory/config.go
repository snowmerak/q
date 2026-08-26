// Package workspacememory provides q's user-level workspace archive service.
package workspacememory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/snowmerak/q/worklock"
)

const (
	ConfigVersion          = 1
	ConfigFileName         = "workspace-memory.json"
	CredentialFileName     = "workspace-memory.token"
	CredentialLockFileName = "workspace-memory-token.lock"
	ServiceLockFileName    = "workspace-memory.lock"
	WorkspaceLockFileName  = ".q/workspace-memory.lock"
	DefaultHost            = "127.0.0.1"
	DefaultPort            = 17892
)

var errInvalidCredential = errors.New("workspacememory: credential is invalid")

// Config identifies the local endpoint used by the user-level service.
// Workspace Memory deliberately accepts loopback addresses only.
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
		return fmt.Errorf("workspacememory: unsupported config version %d", c.Version)
	}
	host := net.ParseIP(c.Host)
	if host == nil || !host.IsLoopback() {
		return fmt.Errorf("workspacememory: host %q must be a loopback IP address", c.Host)
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("workspacememory: port must be between 1 and 65535")
	}
	if c.ProbeHost != "" {
		probe := net.ParseIP(c.ProbeHost)
		if probe == nil || !probe.IsLoopback() {
			return fmt.Errorf("workspacememory: probe_host %q must be a loopback IP address", c.ProbeHost)
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

// ConfigStore persists endpoint configuration below q's user configuration
// directory. The bearer credential is intentionally stored separately.
type ConfigStore struct{ Dir string }

func (s ConfigStore) Path() string { return filepath.Join(s.Dir, ConfigFileName) }

func (s ConfigStore) CredentialPath() string { return filepath.Join(s.Dir, CredentialFileName) }

func (s ConfigStore) LoadOrDefault() (Config, error) {
	file, err := os.Open(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("workspacememory: open config: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var value Config
	if err := decoder.Decode(&value); err != nil {
		return Config{}, fmt.Errorf("workspacememory: decode config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("workspacememory: config contains multiple JSON values")
		}
		return Config{}, fmt.Errorf("workspacememory: decode config: %w", err)
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
		return fmt.Errorf("workspacememory: encode config: %w", err)
	}
	body = append(body, '\n')
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("workspacememory: create config directory: %w", err)
	}
	file, err := os.CreateTemp(s.Dir, ".workspace-memory-*.json")
	if err != nil {
		return fmt.Errorf("workspacememory: create temporary config: %w", err)
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
		return fmt.Errorf("workspacememory: secure temporary config: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		return fmt.Errorf("workspacememory: write temporary config: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("workspacememory: sync temporary config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("workspacememory: close temporary config: %w", err)
	}
	if err := replaceFile(temporary, s.Path()); err != nil {
		return fmt.Errorf("workspacememory: replace config: %w", err)
	}
	keep = true
	return nil
}

// EnsureCredential loads or creates the service's private bearer token. It is
// always enabled, unlike configurable public Gateway authentication.
func (s ConfigStore) EnsureCredential() (string, error) {
	if credential, err := s.LoadCredential(); err == nil {
		return credential, nil
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errInvalidCredential) {
		return "", err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", fmt.Errorf("workspacememory: create config directory: %w", err)
	}
	_ = os.Chmod(s.Dir, 0o700)
	credentialLock, err := acquireCredentialLock(s.Dir)
	if err != nil {
		return "", err
	}
	defer credentialLock.Close()
	if credential, loadErr := s.LoadCredential(); loadErr == nil {
		return credential, nil
	} else if !errors.Is(loadErr, os.ErrNotExist) && !errors.Is(loadErr, errInvalidCredential) {
		return "", loadErr
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("workspacememory: generate credential: %w", err)
	}
	credential := hex.EncodeToString(random)
	file, err := os.CreateTemp(s.Dir, ".workspace-memory-token-*")
	if err != nil {
		return "", fmt.Errorf("workspacememory: create temporary credential: %w", err)
	}
	temporary := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(temporary)
		}
	}()
	_ = file.Chmod(0o600)
	if _, err := file.WriteString(credential + "\n"); err != nil {
		return "", fmt.Errorf("workspacememory: write temporary credential: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("workspacememory: sync temporary credential: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("workspacememory: close temporary credential: %w", err)
	}
	if err := replaceFile(temporary, s.CredentialPath()); err != nil {
		return "", fmt.Errorf("workspacememory: replace credential: %w", err)
	}
	keep = true
	return credential, nil
}

func acquireCredentialLock(dir string) (*worklock.Lock, error) {
	deadline := time.Now().Add(3 * time.Second)
	for {
		lock, err := worklock.AcquireFile(dir, CredentialLockFileName, "q workspace memory credential")
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, worklock.ErrLocked) || time.Now().After(deadline) {
			return nil, fmt.Errorf("workspacememory: acquire credential lock: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s ConfigStore) LoadCredential() (string, error) {
	body, err := os.ReadFile(s.CredentialPath())
	if err != nil {
		return "", fmt.Errorf("workspacememory: read credential: %w", err)
	}
	credential := strings.TrimSpace(string(body))
	decoded, decodeErr := hex.DecodeString(credential)
	if decodeErr != nil || len(decoded) != 32 {
		return "", errInvalidCredential
	}
	return credential, nil
}
