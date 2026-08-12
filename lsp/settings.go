package lsp

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const (
	WorkspaceConfigVersion = 1
	RootSourceManual       = "manual"
	RootSourceDiscovered   = "discovered"
)

// GlobalConfig describes trusted language-server executables. Workspace
// configuration may select these profiles but cannot introduce commands.
type GlobalConfig struct {
	Servers   map[string]ServerConfig `yaml:"servers,omitempty" json:"servers,omitempty"`
	Languages map[string]string       `yaml:"languages,omitempty" json:"languages,omitempty"`
}

type ServerConfig struct {
	Languages []string `yaml:"languages" json:"languages"`
	Command   string   `yaml:"command" json:"command"`
	Args      []string `yaml:"args,omitempty" json:"args,omitempty"`
	Disabled  bool     `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}

type WorkspaceConfig struct {
	Version int          `json:"version"`
	Roots   []RootConfig `json:"roots,omitempty"`
}

type RootConfig struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Server   string `json:"server,omitempty"`
	Source   string `json:"source,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

func (c GlobalConfig) Validate() error {
	for id, server := range c.Servers {
		if err := validateIdentifier("server ID", id); err != nil {
			return err
		}
		if strings.TrimSpace(server.Command) == "" || server.Command != strings.TrimSpace(server.Command) {
			return fmt.Errorf("LSP server %q command is required without surrounding whitespace", id)
		}
		languages, err := NormalizeLanguages(server.Languages)
		if err != nil {
			return fmt.Errorf("LSP server %q: %w", id, err)
		}
		if len(languages) == 0 {
			return fmt.Errorf("LSP server %q requires at least one language", id)
		}
		for _, argument := range server.Args {
			if strings.ContainsRune(argument, '\x00') {
				return fmt.Errorf("LSP server %q argument contains NUL", id)
			}
		}
	}
	for language, serverID := range c.Languages {
		if err := validateIdentifier("language", language); err != nil {
			return err
		}
		server, exists := c.Servers[serverID]
		if !exists {
			return fmt.Errorf("LSP language %q selects unknown server %q", language, serverID)
		}
		if !containsLanguage(server.Languages, language) {
			return fmt.Errorf("LSP server %q does not support mapped language %q", serverID, language)
		}
	}
	return nil
}

func (c WorkspaceConfig) Validate(global GlobalConfig) error {
	if c.Version != WorkspaceConfigVersion {
		return fmt.Errorf("LSP workspace config version must be %d", WorkspaceConfigVersion)
	}
	seen := make(map[string]struct{}, len(c.Roots))
	for index, root := range c.Roots {
		normalized, err := NormalizeRoot(root)
		if err != nil {
			return fmt.Errorf("LSP root %d: %w", index+1, err)
		}
		key := strings.ToLower(filepath.ToSlash(normalized.Path)) + "\x00" + normalized.Language
		if _, exists := seen[key]; exists {
			return fmt.Errorf("LSP root %q has duplicate language %q", normalized.Path, normalized.Language)
		}
		seen[key] = struct{}{}
		if normalized.Server != "" {
			server, exists := global.Servers[normalized.Server]
			if !exists {
				return fmt.Errorf("LSP root %q selects unknown server %q", normalized.Path, normalized.Server)
			}
			if !containsLanguage(server.Languages, normalized.Language) {
				return fmt.Errorf("LSP server %q does not support language %q", normalized.Server, normalized.Language)
			}
		}
	}
	return nil
}

func NormalizeLanguages(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if err := validateIdentifier("language", value); err != nil {
			return nil, err
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func NormalizeRoot(root RootConfig) (RootConfig, error) {
	rawPath := strings.TrimSpace(root.Path)
	if rawPath == "" {
		return RootConfig{}, errors.New("path is required")
	}
	root.Path = filepath.ToSlash(filepath.Clean(rawPath))
	if root.Path == "" || root.Path == "/" || root.Path == ".." || strings.HasPrefix(root.Path, "../") || filepath.IsAbs(root.Path) || filepath.VolumeName(root.Path) != "" {
		return RootConfig{}, errors.New("path must stay workspace-relative")
	}
	if root.Path == "" {
		root.Path = "."
	}
	root.Language = strings.ToLower(strings.TrimSpace(root.Language))
	if err := validateIdentifier("language", root.Language); err != nil {
		return RootConfig{}, err
	}
	root.Server = strings.TrimSpace(root.Server)
	if root.Server != "" {
		if err := validateIdentifier("server ID", root.Server); err != nil {
			return RootConfig{}, err
		}
	}
	root.Source = strings.TrimSpace(root.Source)
	if root.Source == "" {
		root.Source = RootSourceManual
	}
	if root.Source != RootSourceManual && root.Source != RootSourceDiscovered {
		return RootConfig{}, errors.New("source must be manual or discovered")
	}
	return root, nil
}

func ResolveServer(global GlobalConfig, root RootConfig) (string, bool) {
	if root.Server != "" {
		server, ok := global.Servers[root.Server]
		return root.Server, ok && !server.Disabled && containsLanguage(server.Languages, root.Language)
	}
	id := global.Languages[root.Language]
	server, exists := global.Servers[id]
	if exists && !server.Disabled && containsLanguage(server.Languages, root.Language) {
		return id, true
	}
	return "", false
}

func validateIdentifier(label, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s is required without surrounding whitespace", label)
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return fmt.Errorf("%s %q contains unsupported characters", label, value)
	}
	return nil
}

func containsLanguage(values []string, language string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), language) {
			return true
		}
	}
	return false
}
