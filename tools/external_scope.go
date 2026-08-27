package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/mcpconfig"
)

// ExternalScope adds an isolated set of external MCP connections to a Runtime.
// Builtin tools, workspace-configured external tools, and Loom capture remain
// owned by the base Runtime. Closing a scope closes only the connections added
// by that scope.
type ExternalScope struct {
	base      *Runtime
	mu        sync.RWMutex
	external  map[string]*externalServer
	routes    map[string]externalToolRoute
	config    mcpconfig.Config
	closeOnce sync.Once
	closeErr  error
}

// NewExternalScope connects the external servers in value without replacing
// the base Runtime's workspace-wide MCP configuration.
func (r *Runtime) NewExternalScope(ctx context.Context, root string, value mcpconfig.Config) (*ExternalScope, []ExternalStatus) {
	scope := &ExternalScope{base: r, config: cloneMCPConfig(value)}
	if r == nil {
		return scope, []ExternalStatus{{Error: "tools: runtime is unavailable"}}
	}
	if err := value.Validate(); err != nil {
		return scope, []ExternalStatus{{Error: err.Error()}}
	}
	statuses := make([]ExternalStatus, 0, len(value.Servers))
	servers := make(map[string]*externalServer)
	routes := make(map[string]externalToolRoute)
	baseNames := make(map[string]struct{})
	for _, tool := range r.Tools() {
		baseNames[tool.Function.Name] = struct{}{}
	}
	for _, role := range mcpconfig.RoleIDs() {
		for _, tool := range r.ToolsForRole(role) {
			baseNames[tool.Function.Name] = struct{}{}
		}
	}
	for _, id := range assignedServerIDs(value.Roles) {
		server, serverRoutes, err := connectExternalServer(ctx, root, id, value.Servers[id])
		status := ExternalStatus{ID: id}
		if err != nil {
			status.Error = err.Error()
			statuses = append(statuses, status)
			continue
		}
		collision := ""
		for name := range serverRoutes {
			if _, exists := routes[name]; exists {
				collision = name
				break
			}
			if _, exists := baseNames[name]; exists {
				collision = name
				break
			}
		}
		if collision != "" {
			_ = server.session.Close()
			status.Error = "external tool name collision " + collision
			statuses = append(statuses, status)
			continue
		}
		servers[id] = server
		for name, route := range serverRoutes {
			routes[name] = route
		}
		status.Tools = len(server.tools)
		statuses = append(statuses, status)
	}
	scope.external = servers
	scope.routes = routes
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	return scope, statuses
}

func (s *ExternalScope) Tools() []client.Tool {
	if s == nil || s.base == nil {
		return nil
	}
	return s.base.Tools()
}

// ToolsForRole returns the base role catalog plus tools connected only for
// this scope.
func (s *ExternalScope) ToolsForRole(role string) []client.Tool {
	if s == nil || s.base == nil {
		return nil
	}
	result := s.base.ToolsForRole(role)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, serverID := range s.config.ServersForRole(role) {
		if server := s.external[serverID]; server != nil {
			result = append(result, server.tools...)
		}
	}
	return result
}

func (s *ExternalScope) Environment() HostEnvironment {
	if s == nil || s.base == nil {
		return HostEnvironment{}
	}
	return s.base.Environment()
}

func (s *ExternalScope) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	if s == nil || s.base == nil {
		return client.ToolResult{}, errors.New("tools: external scope is unavailable")
	}
	s.mu.RLock()
	if route, external := s.routes[call.Function.Name]; external {
		defer s.mu.RUnlock()
		callContext, cancel := context.WithTimeout(ctx, externalCallTimeout)
		defer cancel()
		arguments := make(map[string]any)
		if strings.TrimSpace(call.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
				return client.ToolResult{Content: "invalid tool arguments: " + err.Error(), IsError: true}, nil
			}
		}
		result, err := route.callTool(callContext, arguments)
		if err != nil {
			return client.ToolResult{}, err
		}
		return CaptureMCPToolResult(ctx, s.base.loom.Store, route.server, call, result)
	}
	s.mu.RUnlock()
	return s.base.Call(ctx, call)
}

// CaptureResult preserves the base Runtime's common Loom boundary for
// non-MCP agent invocation results.
func (s *ExternalScope) CaptureResult(
	ctx context.Context,
	source CaptureSource,
	call client.ToolCall,
	result client.ToolResult,
) (client.ToolResult, error) {
	if s == nil || s.base == nil {
		return client.ToolResult{}, errors.New("tools: external scope is unavailable")
	}
	return s.base.CaptureResult(ctx, source, call, result)
}

// Close waits for active scoped MCP calls and closes only scoped servers.
func (s *ExternalScope) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, server := range s.external {
			s.closeErr = errors.Join(s.closeErr, server.session.Close())
		}
		s.external = nil
		s.routes = nil
		s.config = mcpconfig.Config{}
	})
	return s.closeErr
}
