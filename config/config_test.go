package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/snowmerak/q/lsp"
)

func TestStoreRoundTrip(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), ".q")}
	want := Default()
	want.Provider.Model = "test-model"
	want.Provider.APIKey = "secret"
	want.Embedding = EmbeddingConfig{Model: "embed-model", Dimensions: 1536}
	want.Agents.Roles = map[string]AgentConfig{
		AgentRolePlanner: {Model: "planning-model", ReasoningEffort: "high"},
		AgentRoleCoder:   {Model: "coding-model", ReasoningEffort: "medium"},
		AgentRoleCommit:  {Model: "commit-model", ReasoningEffort: "low"},
		AgentRoleThinker: {Model: "thinking-model", ReasoningEffort: "high"},
	}
	want.LSP = lsp.GlobalConfig{
		Servers:   map[string]lsp.ServerConfig{"gopls": {Languages: []string{"go"}, Command: "gopls"}},
		Languages: map[string]string{"go": "gopls"},
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

func TestEffectiveContextUsesDefaultsAndGatewayWindowFirst(t *testing.T) {
	value := Config{Context: ContextConfig{Window: 654321}}
	effective := value.EffectiveContext()
	if effective.TriggerRatio != .85 || effective.TargetRatio != .22 || effective.RecentRatio != .07 {
		t.Fatalf("effective context = %#v", effective)
	}
	if got := value.EffectiveContextWindow(); got != 654321 {
		t.Fatalf("fallback context window = %d", got)
	}
	value.Provider.ContextWindow = 123456
	if got := value.EffectiveContextWindow(); got != 123456 {
		t.Fatalf("Gateway model context window = %d", got)
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

func TestThinkerUsesRoleOverrideAndActiveModelFallback(t *testing.T) {
	value := Default()
	value.Provider.Model = "active-model"

	inherited, err := value.EffectiveAgent(AgentRoleThinker)
	if err != nil || inherited.Model != "active-model" || inherited.ReasoningEffort != "" {
		t.Fatalf("inherited thinker = %#v, err = %v", inherited, err)
	}

	value.Agents.Roles = map[string]AgentConfig{
		AgentRoleThinker: {Model: "thinking-model", ReasoningEffort: "high"},
	}
	overridden, err := value.EffectiveAgent(AgentRoleThinker)
	if err != nil || overridden.Model != "thinking-model" || overridden.ReasoningEffort != "high" {
		t.Fatalf("overridden thinker = %#v, err = %v", overridden, err)
	}
}

func TestAgentRolesIncludesThinker(t *testing.T) {
	roles := AgentRoles()
	if !IsAgentRole(AgentRoleThinker) {
		t.Fatal("thinker is not recognized as an agent role")
	}
	found := false
	for _, role := range roles {
		if role == AgentRoleThinker {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("agent roles = %#v", roles)
	}
}

func TestEffectiveLoomUsesDefaultsAndValidatesPolicy(t *testing.T) {
	value := Default()
	value.Provider.Model = "active-model"
	value.Loom = LoomConfig{}
	effective := value.EffectiveLoom()
	if effective.MaximumArtifactMiB != 64 || effective.MaximumStoreMiB != 256 ||
		effective.GC.TriggerRatio != .80 || effective.GC.TargetRatio != .60 || effective.GC.GraceHours != 1 {
		t.Fatalf("effective Loom config = %#v", effective)
	}
	value.Loom = LoomConfig{
		MaximumArtifactMiB: 128, MaximumStoreMiB: 64,
		GC: LoomGCConfig{TriggerRatio: .8, TargetRatio: .6, GraceHours: 24},
	}
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "cannot exceed") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestCommitAgentUsesRoleOverrideAndActiveModelFallback(t *testing.T) {
	value := Default()
	value.Provider.Model = "active-model"

	inherited, err := value.EffectiveAgent(AgentRoleCommit)
	if err != nil || inherited.Model != "active-model" || inherited.ReasoningEffort != "" {
		t.Fatalf("inherited commit agent = %#v, err = %v", inherited, err)
	}

	value.Agents.Roles = map[string]AgentConfig{
		AgentRoleCommit: {Model: "commit-model", ReasoningEffort: "low"},
	}
	overridden, err := value.EffectiveAgent(AgentRoleCommit)
	if err != nil || overridden.Model != "commit-model" || overridden.ReasoningEffort != "low" {
		t.Fatalf("overridden commit agent = %#v, err = %v", overridden, err)
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

func TestValidateEmbeddingConfiguration(t *testing.T) {
	tests := []struct {
		name      string
		embedding EmbeddingConfig
		want      string
	}{
		{name: "dimensions without model", embedding: EmbeddingConfig{Dimensions: 1536}, want: "model is required"},
		{name: "model without dimensions", embedding: EmbeddingConfig{Model: "embed-model"}, want: "dimensions"},
		{name: "model whitespace", embedding: EmbeddingConfig{Model: " embed-model", Dimensions: 1536}, want: "surrounding whitespace"},
		{name: "dimensions too large", embedding: EmbeddingConfig{Model: "embed-model", Dimensions: 4097}, want: "dimensions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := Default()
			value.Provider.Model = "active-model"
			value.Embedding = test.embedding
			if err := value.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v; want containing %q", err, test.want)
			}
		})
	}

	value := Default()
	value.Provider.Model = "active-model"
	value.Embedding = EmbeddingConfig{Model: "embed-model", Dimensions: 3072}
	if err := value.Validate(); err != nil {
		t.Fatalf("valid embedding configuration: %v", err)
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
