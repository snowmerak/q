package mcpconfig

import "testing"

func TestCloneDoesNotShareMutableSettings(t *testing.T) {
	value := Config{Servers: map[string]ServerConfig{"server": {
		Args: []string{"original"}, Env: map[string]string{"key": "original"},
		Headers: map[string]string{"key": "original"}, ResolvedEnv: map[string]string{"key": "secret"},
		ResolvedHeaders: map[string]string{"key": "secret"},
	}}, Roles: map[string][]string{"default": {"server"}}}
	cloned := value.Clone()
	server := cloned.Servers["server"]
	server.Args[0] = "changed"
	server.Env["key"] = "changed"
	server.Headers["key"] = "changed"
	server.ResolvedEnv["key"] = "changed"
	server.ResolvedHeaders["key"] = "changed"
	cloned.Roles["default"][0] = "changed"
	original := value.Servers["server"]
	if original.Args[0] != "original" || original.Env["key"] != "original" || original.Headers["key"] != "original" ||
		original.ResolvedEnv["key"] != "secret" || original.ResolvedHeaders["key"] != "secret" || value.Roles["default"][0] != "server" {
		t.Fatal("clone mutated its source")
	}
}
