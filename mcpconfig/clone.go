package mcpconfig

import "maps"

// Clone copies mutable server settings and role assignments, including resolved
// credentials that are intentionally omitted from JSON serialization.
func (c Config) Clone() Config {
	result := Config{Version: c.Version, Servers: make(map[string]ServerConfig, len(c.Servers)), Roles: make(map[string][]string, len(c.Roles))}
	for id, server := range c.Servers {
		server.Args = append([]string(nil), server.Args...)
		server.Env = maps.Clone(server.Env)
		server.Headers = maps.Clone(server.Headers)
		server.ResolvedEnv = maps.Clone(server.ResolvedEnv)
		server.ResolvedHeaders = maps.Clone(server.ResolvedHeaders)
		result.Servers[id] = server
	}
	for role, servers := range c.Roles {
		result.Roles[role] = append([]string(nil), servers...)
	}
	return result
}
