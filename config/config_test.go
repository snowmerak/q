package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), ".q")}
	want := Default()
	want.Provider.Model = "test-model"
	want.Provider.APIKey = "secret"
	want.Agents.Roles = map[string]AgentConfig{
		AgentRolePlanner: {Model: "planning-model", ReasoningEffort: "high"},
		AgentRoleCoder:   {Model: "coding-model", ReasoningEffort: "medium"},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded config = %#v; want %#v", got, want)
	}
	want.Provider.Model = "second-model"
	if err := store.Save(want); err != nil {
		t.Fatalf("replace existing config: %v", err)
	}
	got, err = store.Load()
	if err != nil || got.Provider.Model != "second-model" {
		t.Fatalf("reloaded config = %#v, err = %v", got, err)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions are too broad: %o", info.Mode().Perm())
	}
}

func TestStoreNotFound(t *testing.T) {
	_, err := (Store{Dir: t.TempDir()}).Load()
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	body := "version: 1\nprovider:\n  type: openai-compatible\n  base_url: https://example.com/v1\n  model: test\n  surprise: true\n"
	if err := os.WriteFile(store.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load()
	if err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveAPIKeyPrecedence(t *testing.T) {
	t.Setenv("TEST_Q_API_KEY", "from-env")
	provider := ProviderConfig{APIKeyEnv: "TEST_Q_API_KEY"}
	if got := provider.ResolveAPIKey(); got != "from-env" {
		t.Fatalf("key = %q", got)
	}
	provider.APIKey = "inline"
	if got := provider.ResolveAPIKey(); got != "inline" {
		t.Fatalf("key = %q", got)
	}
}

func TestManagedGatewayConfigDoesNotPersistEndpointCredentials(t *testing.T) {
	value := Default()
	value.Provider.Model = "codex/gpt-test"
	value.Provider.APIKey = "secret"
	value.UseManagedGateway()
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	if !value.Provider.Managed || value.Provider.BaseURL != "" || value.Provider.APIKey != "" || value.Provider.APIKeyEnv != "" {
		t.Fatalf("managed provider = %#v", value.Provider)
	}
}

func TestEffectiveContextUsesDefaultsAndExplicitWindow(t *testing.T) {
	value := Config{Provider: ProviderConfig{ContextWindow: 123456}}
	effective := value.EffectiveContext()
	if effective.TriggerRatio != .85 || effective.TargetRatio != .22 || effective.RecentRatio != .07 {
		t.Fatalf("effective context = %#v", effective)
	}
	if got := value.EffectiveContextWindow(); got != 123456 {
		t.Fatalf("model context window = %d", got)
	}
	value.Context.Window = 654321
	if got := value.EffectiveContextWindow(); got != 654321 {
		t.Fatalf("explicit context window = %d", got)
	}
}

func TestEffectiveAgentUsesRoleOverrideAndActiveModelFallback(t *testing.T) {
	value := Default()
	value.Provider.Model = "active-model"
	value.Agents.Roles = map[string]AgentConfig{
		AgentRolePlanner: {Model: "planning-model", ReasoningEffort: "high"},
		AgentRoleCoder:   {ReasoningEffort: "medium"},
	}

	planner, err := value.EffectiveAgent(AgentRolePlanner)
	if err != nil || planner.Model != "planning-model" || planner.ReasoningEffort != "high" {
		t.Fatalf("planner = %#v, err = %v", planner, err)
	}
	coder, err := value.EffectiveAgent(AgentRoleCoder)
	if err != nil || coder.Model != "active-model" || coder.ReasoningEffort != "medium" {
		t.Fatalf("coder = %#v, err = %v", coder, err)
	}
	if _, err := value.EffectiveAgent("reviewer"); err == nil {
		t.Fatal("unknown role unexpectedly resolved")
	}
}

func TestValidateAgentConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		agents AgentsConfig
		want   string
	}{
		{name: "negative parallelism", agents: AgentsConfig{MaxParallel: -1}, want: "max_parallel"},
		{name: "unknown role", agents: AgentsConfig{Roles: map[string]AgentConfig{"reviewer": {}}}, want: "unsupported agent role"},
		{name: "model whitespace", agents: AgentsConfig{Roles: map[string]AgentConfig{AgentRoleScout: {Model: " model"}}}, want: "model must not"},
		{name: "effort whitespace", agents: AgentsConfig{Roles: map[string]AgentConfig{AgentRoleScout: {ReasoningEffort: " high "}}}, want: "reasoning_effort"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := Default()
			value.Provider.Model = "active-model"
			value.Agents = test.agents
			if err := value.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v; want containing %q", err, test.want)
			}
		})
	}
}

func TestWhitespaceOnlyReasoningEffortIsCanonicalizedToEmpty(t *testing.T) {
	value := Default()
	value.Provider.Model = "active-model"
	value.Agents.Roles = map[string]AgentConfig{
		AgentRoleScout: {ReasoningEffort: "   "},
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	agent, err := value.EffectiveAgent(AgentRoleScout)
	if err != nil {
		t.Fatal(err)
	}
	if agent.ReasoningEffort != "" {
		t.Fatalf("reasoning effort = %q", agent.ReasoningEffort)
	}
}
