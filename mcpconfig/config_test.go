package mcpconfig

import (
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	want := Config{
		Servers: map[string]ServerConfig{
			"local":  {Transport: TransportStdio, Command: "server", Args: []string{"serve"}, Env: map[string]string{"TOKEN": "SOURCE_TOKEN"}},
			"remote": {Transport: TransportStreamableHTTP, URL: "https://example.test/mcp", Headers: map[string]string{"Authorization": "MCP_AUTH"}},
		},
		Roles: map[string][]string{RoleDefault: {"remote", "local"}},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want.Version = CurrentVersion
	want.Roles[RoleDefault] = []string{"local", "remote"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded = %#v; want %#v", got, want)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}
}

func TestValidateRejectsInvalidReferencesAndSecrets(t *testing.T) {
	tests := []struct {
		name  string
		value Config
		want  string
	}{
		{"unknown server", Config{Version: CurrentVersion, Roles: map[string][]string{RoleDefault: {"missing"}}}, "unknown server"},
		{"unknown role", Config{Version: CurrentVersion, Roles: map[string][]string{"made-up": nil}}, "unsupported role"},
		{"inline env value", Config{Version: CurrentVersion, Servers: map[string]ServerConfig{"local": {Transport: TransportStdio, Command: "server", Env: map[string]string{"TOKEN": "literal secret"}}}}, "environment variable names"},
		{"bad URL", Config{Version: CurrentVersion, Servers: map[string]ServerConfig{"remote": {Transport: TransportStreamableHTTP, URL: "relative"}}}, "absolute HTTP"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.value.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v; want %q", err, test.want)
			}
		})
	}
}

func TestServersForRoleReturnsSortedServers(t *testing.T) {
	value := Config{Roles: map[string][]string{RoleDefault: {"z", "x"}}}
	if got := value.ServersForRole(RoleDefault); !reflect.DeepEqual(got, []string{"x", "z"}) {
		t.Fatalf("ServersForRole() = %v", got)
	}
}

func TestRoleIDsContainOnlyToolUsingRoles(t *testing.T) {
	want := []string{"default", "griller", "scout", "planner", "coder"}
	if got := RoleIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("RoleIDs() = %v; want %v", got, want)
	}
	if IsRole("thinker") {
		t.Fatal("role without a tool runtime was accepted")
	}
}
