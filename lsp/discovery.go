package lsp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var errNoProjectRoot = errors.New("lsp: no configured project root")

type ManagerOption func(*Manager)

// WithRootDiscovery enables one lazy discovery pass when routing finds no
// project or server. Results stay in memory; existing settings take precedence.
func WithRootDiscovery(discover func(context.Context) ([]RootConfig, error)) ManagerOption {
	return func(m *Manager) { m.discoverRoots = discover }
}

type discoveryAttempt struct {
	ready chan struct{}
	err   error
}

func (m *Manager) autoDiscover(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	if m.discoverRoots == nil {
		m.mu.Unlock()
		return nil
	}
	if attempt := m.discovery; attempt != nil {
		m.mu.Unlock()
		select {
		case <-attempt.ready:
			return attempt.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	attempt := &discoveryAttempt{ready: make(chan struct{})}
	m.discovery = attempt
	discover := m.discoverRoots
	configured := append([]RootConfig(nil), m.workspace.Roots...)
	m.mu.Unlock()

	roots, err := discover(ctx)
	var servers []DiscoveredServer
	if err == nil && ctx.Err() == nil {
		var languages []string
		for _, root := range append(configured, roots...) {
			if !root.Disabled {
				languages = append(languages, root.Language)
			}
		}
		if len(languages) > 0 {
			servers = DiscoverServers(languages)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil {
		err = ctx.Err()
		// A cancelled scan must not consume the next caller's discovery attempt.
		m.discovery = nil
	} else if m.closed {
		err = ErrClosed
	} else if err == nil {
		var global GlobalConfig
		var workspace WorkspaceConfig
		global, workspace, err = mergeDiscoveredConfiguration(m.global, m.workspace, roots, servers)
		if err == nil {
			m.global, m.workspace = global, workspace
		}
	}
	if err != nil {
		attempt.err = fmt.Errorf("lsp: automatic discovery: %w", err)
	}
	close(attempt.ready)
	return attempt.err
}

func mergeDiscoveredConfiguration(global GlobalConfig, workspace WorkspaceConfig, roots []RootConfig, servers []DiscoveredServer) (GlobalConfig, WorkspaceConfig, error) {
	global, err := global.Normalized()
	if err != nil {
		return GlobalConfig{}, WorkspaceConfig{}, err
	}
	workspace.Roots = append([]RootConfig(nil), workspace.Roots...)
	seen := make(map[string]bool, len(workspace.Roots))
	for _, root := range workspace.Roots {
		seen[sessionKey(root)] = true
	}
	for _, root := range roots {
		root, err = NormalizeRoot(root)
		if err != nil {
			return GlobalConfig{}, WorkspaceConfig{}, err
		}
		if seen[sessionKey(root)] || beneathDisabledRoot(workspace.Roots, root) {
			continue
		}
		root.Source = RootSourceDiscovered
		workspace.Roots = append(workspace.Roots, root)
		seen[sessionKey(root)] = true
	}
	for _, found := range servers {
		if _, exists := global.Servers[found.ID]; exists {
			continue
		}
		var languages []string
		for _, language := range found.Config.Languages {
			// Do not replace a user's profile or bypass a disabled alternative,
			// even when that profile has no language-default mapping.
			configured := global.Languages[language] != ""
			for _, server := range global.Servers {
				configured = configured || containsLanguage(server.Languages, language)
			}
			if !configured {
				languages = append(languages, language)
			}
		}
		if len(languages) == 0 {
			continue
		}
		global.Servers[found.ID] = found.Config
		for _, language := range languages {
			global.Languages[language] = found.ID
		}
	}
	global, err = global.Normalized()
	if err != nil {
		return GlobalConfig{}, WorkspaceConfig{}, err
	}
	if err := global.Validate(); err != nil {
		return GlobalConfig{}, WorkspaceConfig{}, err
	}
	if err := workspace.Validate(global); err != nil {
		return GlobalConfig{}, WorkspaceConfig{}, err
	}
	return global, workspace, nil
}

func beneathDisabledRoot(roots []RootConfig, candidate RootConfig) bool {
	for _, root := range roots {
		if root.Disabled && root.Language == candidate.Language && pathInside(filepath.FromSlash(root.Path), filepath.FromSlash(candidate.Path)) {
			return true
		}
	}
	return false
}

func enabledRoot(root RootConfig) (RootConfig, error) {
	if root.Disabled {
		return RootConfig{}, fmt.Errorf("lsp: project root %s (%s) is disabled", root.Path, root.Language)
	}
	return root, nil
}

func (m *Manager) hasServer(root RootConfig) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := ResolveServer(m.global, root)
	return ok
}

func (m *Manager) enabledRoots(language string) []RootConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	var roots []RootConfig
	for _, root := range m.workspace.Roots {
		if root.Disabled || (language != "" && !strings.EqualFold(root.Language, language)) {
			continue
		}
		if _, ok := ResolveServer(m.global, root); ok {
			roots = append(roots, root)
		}
	}
	return roots
}
