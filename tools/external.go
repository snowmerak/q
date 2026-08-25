package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/mcpconfig"
)

const externalConnectTimeout = 10 * time.Second
const externalCallTimeout = 60 * time.Second

type ExternalStatus struct {
	ID    string
	Tools int
	Error string
}

type externalServer struct {
	session *mcp.ClientSession
	tools   []client.Tool
}

type externalToolRoute struct {
	server  string
	tool    string
	session *mcp.ClientSession
}

// ConfigureExternal atomically replaces the external MCP sessions. Individual
// server failures are reported as statuses and do not disable builtin tools or
// successfully connected servers.
func (r *Runtime) ConfigureExternal(ctx context.Context, root string, value mcpconfig.Config) []ExternalStatus {
	if r == nil {
		return []ExternalStatus{{Error: "tools: runtime is unavailable"}}
	}
	if err := value.Validate(); err != nil {
		return []ExternalStatus{{Error: err.Error()}}
	}
	statuses := make([]ExternalStatus, 0, len(value.Servers))
	servers := make(map[string]*externalServer)
	routes := make(map[string]externalToolRoute)
	required := assignedServerIDs(value.Roles)
	for _, id := range required {
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
		}
		if collision != "" {
			_ = server.session.Close()
			status.Error = fmt.Sprintf("external tool name collision %q", collision)
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

	r.externalMu.Lock()
	previous := r.external
	r.external = servers
	r.externalRoutes = routes
	r.externalConfig = cloneMCPConfig(value)
	r.externalMu.Unlock()
	for _, server := range previous {
		_ = server.session.Close()
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	return statuses
}

func connectExternalServer(ctx context.Context, root, id string, value mcpconfig.ServerConfig) (*externalServer, map[string]externalToolRoute, error) {
	transport, err := externalTransport(root, id, value)
	if err != nil {
		return nil, nil, err
	}
	connectContext, cancel := context.WithTimeout(ctx, externalConnectTimeout)
	defer cancel()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "q-" + id, Version: ServerVersion}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{},
	}).Connect(connectContext, transport, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("connect %s: %w", id, err)
	}
	server := &externalServer{session: session}
	routes := make(map[string]externalToolRoute)
	for tool, listErr := range session.Tools(connectContext, nil) {
		if listErr != nil {
			_ = session.Close()
			return nil, nil, fmt.Errorf("list tools from %s: %w", id, listErr)
		}
		name := externalToolName(id, tool.Name)
		if _, exists := routes[name]; exists {
			_ = session.Close()
			return nil, nil, fmt.Errorf("server %s exposes colliding tool names after normalization: %q", id, name)
		}
		parameters, schemaErr := schemaObject(tool.InputSchema)
		if schemaErr != nil {
			_ = session.Close()
			return nil, nil, fmt.Errorf("encode schema for %s/%s: %w", id, tool.Name, schemaErr)
		}
		description := strings.TrimSpace(tool.Description)
		if description == "" {
			description = "External MCP tool from " + id + "."
		} else {
			description = "[MCP " + id + "] " + description
		}
		server.tools = append(server.tools, client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
			Name: name, Description: description, Parameters: parameters,
		}})
		routes[name] = externalToolRoute{server: id, tool: tool.Name, session: session}
	}
	sort.Slice(server.tools, func(i, j int) bool { return server.tools[i].Function.Name < server.tools[j].Function.Name })
	return server, routes, nil
}

func externalTransport(root, id string, value mcpconfig.ServerConfig) (mcp.Transport, error) {
	switch value.Transport {
	case mcpconfig.TransportStdio:
		command := exec.Command(value.Command, value.Args...)
		command.Dir = root
		command.Stderr = io.Discard
		command.Env = command.Environ()
		for target, source := range value.Env {
			secret, found := os.LookupEnv(source)
			if !found {
				return nil, fmt.Errorf("server %s requires environment variable %s", id, source)
			}
			command.Env = replaceEnvironment(command.Env, target, secret)
		}
		for target, secret := range value.ResolvedEnv {
			command.Env = replaceEnvironment(command.Env, target, secret)
		}
		return &mcp.CommandTransport{Command: command}, nil
	case mcpconfig.TransportStreamableHTTP:
		headers := make(http.Header, len(value.Headers))
		for header, source := range value.Headers {
			secret, found := os.LookupEnv(source)
			if !found {
				return nil, fmt.Errorf("server %s requires environment variable %s", id, source)
			}
			headers.Set(header, secret)
		}
		for header, secret := range value.ResolvedHeaders {
			headers.Set(header, secret)
		}
		return &mcp.StreamableClientTransport{Endpoint: value.URL, HTTPClient: &http.Client{
			Transport: headerTransport{base: http.DefaultTransport, headers: headers},
		}}, nil
	default:
		return nil, fmt.Errorf("server %s has unsupported transport %q", id, value.Transport)
	}
}

func replaceEnvironment(environment []string, name, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		key, _, found := strings.Cut(item, "=")
		matches := key == name
		if runtime.GOOS == "windows" {
			matches = strings.EqualFold(key, name)
		}
		if !found || !matches {
			result = append(result, item)
		}
	}
	return append(result, name+"="+value)
}

type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	for name, values := range t.headers {
		for _, value := range values {
			clone.Header.Add(name, value)
		}
	}
	return t.base.RoundTrip(clone)
}

func externalToolName(server, tool string) string {
	var normalized strings.Builder
	for _, r := range tool {
		if r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') {
			normalized.WriteRune(r)
		} else {
			normalized.WriteByte('_')
		}
	}
	name := strings.Trim(normalized.String(), "_-")
	if name == "" {
		name = "tool"
	}
	full := "mcp_" + server + "__" + name
	if len(full) <= 64 {
		return full
	}
	digest := sha256.Sum256([]byte(full))
	return full[:55] + "_" + hex.EncodeToString(digest[:4])
}

func assignedServerIDs(values map[string][]string) []string {
	set := make(map[string]struct{})
	for _, ids := range values {
		for _, id := range ids {
			set[id] = struct{}{}
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func cloneMCPConfig(value mcpconfig.Config) mcpconfig.Config {
	result := mcpconfig.Config{Version: value.Version, Servers: make(map[string]mcpconfig.ServerConfig, len(value.Servers)), Roles: make(map[string][]string, len(value.Roles))}
	for id, server := range value.Servers {
		server.Args = append([]string(nil), server.Args...)
		server.Env = cloneStrings(server.Env)
		server.Headers = cloneStrings(server.Headers)
		server.ResolvedEnv = cloneStrings(server.ResolvedEnv)
		server.ResolvedHeaders = cloneStrings(server.ResolvedHeaders)
		result.Servers[id] = server
	}
	for role, ids := range value.Roles {
		result.Roles[role] = append([]string(nil), ids...)
	}
	return result
}

func cloneStrings(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
