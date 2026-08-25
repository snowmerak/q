// Package mcpconfig stores external MCP server profiles and per-role tool assignments.
package mcpconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/snowmerak/q/config"
)

const (
	CurrentVersion = 1
	FileName       = "mcp.json"
	maximumSize    = 4 << 20

	TransportStdio          = "stdio"
	TransportStreamableHTTP = "streamable-http"
	RoleDefault             = "default"
)

var (
	ErrNotFound   = errors.New("q MCP configuration not found")
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	envPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerPattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

type Config struct {
	Version int                     `json:"version"`
	Servers map[string]ServerConfig `json:"servers,omitempty"`
	Roles   map[string][]string     `json:"roles,omitempty"`
}

// ServerConfig persists no credentials. Env maps child variable names to
// parent environment variable names, and Headers maps HTTP header names to
// environment variable names. Resolved values are memory-only session inputs.
type ServerConfig struct {
	Transport       string            `json:"transport"`
	Command         string            `json:"command,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	URL             string            `json:"url,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	ResolvedEnv     map[string]string `json:"-"`
	ResolvedHeaders map[string]string `json:"-"`
}

func Default() Config {
	return Config{CurrentVersion, make(map[string]ServerConfig), make(map[string][]string)}
}

func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("mcpconfig: unsupported version %d", c.Version)
	}
	for id, server := range c.Servers {
		if !idPattern.MatchString(id) {
			return fmt.Errorf("mcpconfig: server ID %q must match %s", id, idPattern.String())
		}
		if err := validateServer(id, server); err != nil {
			return err
		}
	}
	for role, servers := range c.Roles {
		if !IsRole(role) {
			return fmt.Errorf("mcpconfig: unsupported role %q", role)
		}
		seen := make(map[string]struct{}, len(servers))
		for _, id := range servers {
			if _, found := c.Servers[id]; !found {
				return fmt.Errorf("mcpconfig: role %q references unknown server %q", role, id)
			}
			if _, duplicate := seen[id]; duplicate {
				return fmt.Errorf("mcpconfig: role %q references server %q more than once", role, id)
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}

func validateServer(id string, server ServerConfig) error {
	switch server.Transport {
	case TransportStdio:
		if strings.TrimSpace(server.Command) == "" || server.Command != strings.TrimSpace(server.Command) {
			return fmt.Errorf("mcpconfig: stdio server %q requires a command without surrounding whitespace", id)
		}
		if server.URL != "" || len(server.Headers) > 0 || len(server.ResolvedHeaders) > 0 {
			return fmt.Errorf("mcpconfig: stdio server %q cannot configure url or headers", id)
		}
	case TransportStreamableHTTP:
		parsed, err := url.Parse(server.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("mcpconfig: HTTP server %q requires an absolute HTTP(S) URL", id)
		}
		if server.Command != "" || len(server.Args) > 0 || len(server.Env) > 0 || len(server.ResolvedEnv) > 0 {
			return fmt.Errorf("mcpconfig: HTTP server %q cannot configure command, args, or child env", id)
		}
	default:
		return fmt.Errorf("mcpconfig: server %q has unsupported transport %q", id, server.Transport)
	}
	for target, source := range server.Env {
		if !envPattern.MatchString(target) || !envPattern.MatchString(source) {
			return fmt.Errorf("mcpconfig: server %q env mapping %q -> %q must use environment variable names", id, target, source)
		}
	}
	for target := range server.ResolvedEnv {
		if !envPattern.MatchString(target) {
			return fmt.Errorf("mcpconfig: server %q resolved env name %q is invalid", id, target)
		}
	}
	for header, source := range server.Headers {
		if !headerPattern.MatchString(header) || http.CanonicalHeaderKey(header) == "" {
			return fmt.Errorf("mcpconfig: server %q has invalid HTTP header %q", id, header)
		}
		if !envPattern.MatchString(source) {
			return fmt.Errorf("mcpconfig: server %q header %q must reference an environment variable", id, header)
		}
	}
	for header := range server.ResolvedHeaders {
		if !headerPattern.MatchString(header) || http.CanonicalHeaderKey(header) == "" {
			return fmt.Errorf("mcpconfig: server %q has invalid resolved HTTP header %q", id, header)
		}
	}
	return nil
}

func (c Config) ServerIDs() []string {
	ids := make([]string, 0, len(c.Servers))
	for id := range c.Servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func RoleIDs() []string {
	return []string{
		RoleDefault,
		config.AgentRoleGriller,
		config.AgentRoleScout,
		config.AgentRolePlanner,
		config.AgentRoleCoder,
	}
}

func IsRole(role string) bool {
	for _, candidate := range RoleIDs() {
		if role == candidate {
			return true
		}
	}
	return false
}

func (c Config) ServersForRole(role string) []string {
	set := make(map[string]struct{})
	for _, id := range c.Roles[role] {
		set[id] = struct{}{}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

type Store struct{ Dir string }

func (s Store) Path() string { return filepath.Join(s.Dir, FileName) }

func (s Store) Load() (Config, error) {
	file, err := os.Open(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, fmt.Errorf("mcpconfig: open %s: %w", s.Path(), err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Config{}, fmt.Errorf("mcpconfig: inspect %s: %w", s.Path(), err)
	}
	if info.Size() > maximumSize {
		return Config{}, fmt.Errorf("mcpconfig: settings exceed %d bytes", maximumSize)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maximumSize+1))
	decoder.DisallowUnknownFields()
	var value Config
	if err := decoder.Decode(&value); err != nil {
		return Config{}, fmt.Errorf("mcpconfig: decode %s: %w", s.Path(), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, fmt.Errorf("mcpconfig: decode %s: multiple JSON values", s.Path())
		}
		return Config{}, fmt.Errorf("mcpconfig: decode %s: %w", s.Path(), err)
	}
	if value.Servers == nil {
		value.Servers = make(map[string]ServerConfig)
	}
	if value.Roles == nil {
		value.Roles = make(map[string][]string)
	}
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
	value.Version = CurrentVersion
	if value.Servers == nil {
		value.Servers = make(map[string]ServerConfig)
	}
	if value.Roles == nil {
		value.Roles = make(map[string][]string)
	}
	for role, ids := range value.Roles {
		ids = append([]string(nil), ids...)
		sort.Strings(ids)
		if len(ids) == 0 {
			delete(value.Roles, role)
		} else {
			value.Roles[role] = ids
		}
	}
	if err := value.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("mcpconfig: create %s: %w", s.Dir, err)
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return fmt.Errorf("mcpconfig: secure %s: %w", s.Dir, err)
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("mcpconfig: encode: %w", err)
	}
	body = append(body, '\n')
	temporary, err := os.CreateTemp(s.Dir, ".mcp-*.json")
	if err != nil {
		return fmt.Errorf("mcpconfig: create temporary file: %w", err)
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
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, s.Path()); err != nil {
		return fmt.Errorf("mcpconfig: replace %s: %w", s.Path(), err)
	}
	keep = true
	return nil
}
