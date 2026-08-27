package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/lsp"
)

func TestStoreRoundTrip(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), ".q")}
	want := Default()
	want.Provider.Model = "test-model"
	want.Provider.ReasoningEffort = "high"
	want.Provider.APIKey = "secret"
	want.Embedding = EmbeddingConfig{Model: "embed-model", Dimensions: 1536}
	want.ModelGroups = map[string]ModelGroupConfig{
		"heavy": {Candidates: []ModelCandidateConfig{
			{Model: "planning-model", ReasoningEffort: "high", Timeout: 45 * time.Second},
			{Model: "backup-model", ReasoningEffort: "medium"},
		}},
	}
	want.Agents.Roles = map[string]AgentConfig{
		AgentRolePlanner:   {Group: "heavy"},
		AgentRoleCoder:     {Model: "coding-model", ReasoningEffort: "medium"},
		AgentRoleCommit:    {Model: "commit-model", ReasoningEffort: "low"},
		AgentRoleThinker:   {Model: "thinking-model", ReasoningEffort: "high"},
		AgentRoleLibrarian: {Model: "librarian-model", ReasoningEffort: "medium"},
		AgentRoleSearch:    {Agent: "primary"},
	}
	want.Agents.Connections = map[string]AgentConnectionConfig{
		"primary":  {Preset: "codex"},
		"fallback": {Preset: "grok", AuthMethod: "local-subscription"},
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
	value.Provider.ReasoningEffort = "low"
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

func TestEffectiveModelCandidatesResolvesOrderedGroup(t *testing.T) {
	value := Default()
	value.Provider.Model = "active-model"
	value.ModelGroups = map[string]ModelGroupConfig{
		"heavy": {Candidates: []ModelCandidateConfig{
			{Model: "primary", Timeout: 10 * time.Second},
			{Model: "secondary", ReasoningEffort: "medium"},
		}},
	}
	value.Agents.Roles = map[string]AgentConfig{
		AgentRolePlanner: {Group: "heavy", ReasoningEffort: "high"},
	}

	candidates, err := value.EffectiveModelCandidates(AgentRolePlanner)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].Model != "primary" ||
		candidates[0].ReasoningEffort != "high" || candidates[0].Timeout != 10*time.Second ||
		candidates[1].Model != "secondary" || candidates[1].ReasoningEffort != "medium" {
		t.Fatalf("candidates = %#v", candidates)
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

func TestLibrarianUsesRoleOverrideAndActiveModelFallback(t *testing.T) {
	value := Default()
	value.Provider.Model = "active-model"

	inherited, err := value.EffectiveAgent(AgentRoleLibrarian)
	if err != nil || inherited.Model != "active-model" || inherited.ReasoningEffort != "" {
		t.Fatalf("inherited librarian = %#v, err = %v", inherited, err)
	}

	value.Agents.Roles = map[string]AgentConfig{
		AgentRoleLibrarian: {Model: "librarian-model", ReasoningEffort: "medium"},
	}
	overridden, err := value.EffectiveAgent(AgentRoleLibrarian)
	if err != nil || overridden.Model != "librarian-model" || overridden.ReasoningEffort != "medium" {
		t.Fatalf("overridden librarian = %#v, err = %v", overridden, err)
	}
}

func TestAgentRolesIncludesLearningRoles(t *testing.T) {
	roles := AgentRoles()
	for _, expected := range []string{AgentRoleThinker, AgentRoleLibrarian} {
		if !IsAgentRole(expected) {
			t.Fatalf("%s is not recognized as an agent role", expected)
		}
		found := false
		for _, role := range roles {
			if role == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("agent roles = %#v; missing %q", roles, expected)
		}
	}
}

func TestValidateAgentConnectionsAndSearchRole(t *testing.T) {
	tests := []struct {
		name        string
		connections map[string]AgentConnectionConfig
		search      AgentConfig
		want        string
	}{
		{name: "invalid id", connections: map[string]AgentConnectionConfig{"bad id": {Preset: "codex"}}, want: "connection ID"},
		{name: "unknown preset", connections: map[string]AgentConnectionConfig{"other": {Preset: "claude"}}, want: "unsupported preset"},
		{name: "missing command", connections: map[string]AgentConnectionConfig{"other": {}}, want: "requires preset or command"},
		{name: "mixed command", connections: map[string]AgentConnectionConfig{"other": {Preset: "codex", Command: "agent"}}, want: "mutually exclusive"},
		{name: "auth whitespace", connections: map[string]AgentConnectionConfig{"codex": {Preset: "codex", AuthMethod: " login "}}, want: "auth_method"},
		{name: "unknown search assignment", search: AgentConfig{Agent: "missing"}, want: "unknown agent connection"},
		{name: "search model", search: AgentConfig{Model: "model"}, want: "accepts agent only"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := Default()
			value.Provider.Model = "active-model"
			value.Agents.Connections = test.connections
			if test.search != (AgentConfig{}) {
				value.Agents.Roles = map[string]AgentConfig{AgentRoleSearch: test.search}
			}
			if err := value.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v; want containing %q", err, test.want)
			}
		})
	}
	value := Default()
	value.Provider.Model = "active-model"
	value.Agents.Connections = map[string]AgentConnectionConfig{
		"codex-main":  {Preset: "codex"},
		"grok.backup": {Preset: "grok"},
	}
	value.Agents.Roles = map[string]AgentConfig{AgentRoleSearch: {Agent: "codex-main"}}
	if err := value.Validate(); err != nil {
		t.Fatalf("valid ACP connections: %v", err)
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

func TestValidateModelGroups(t *testing.T) {
	tests := []struct {
		name   string
		groups map[string]ModelGroupConfig
		agent  AgentConfig
		want   string
	}{
		{name: "empty group", groups: map[string]ModelGroupConfig{"heavy": {}}, want: "at least one"},
		{name: "empty model", groups: map[string]ModelGroupConfig{"heavy": {Candidates: []ModelCandidateConfig{{}}}}, want: "model must be"},
		{name: "duplicate model", groups: map[string]ModelGroupConfig{"heavy": {Candidates: []ModelCandidateConfig{{Model: "one"}, {Model: "one"}}}}, want: "duplicate"},
		{name: "nested group", groups: map[string]ModelGroupConfig{"heavy": {Candidates: []ModelCandidateConfig{{Model: "group/other"}}}}, want: "cannot reference"},
		{name: "negative timeout", groups: map[string]ModelGroupConfig{"heavy": {Candidates: []ModelCandidateConfig{{Model: "one", Timeout: -time.Second}}}}, want: "timeout"},
		{name: "unknown group", agent: AgentConfig{Group: "missing"}, want: "unknown model group"},
		{name: "model and group", groups: map[string]ModelGroupConfig{"heavy": {Candidates: []ModelCandidateConfig{{Model: "one"}}}}, agent: AgentConfig{Model: "one", Group: "heavy"}, want: "mutually exclusive"},
		{name: "unknown provider group", agent: AgentConfig{}, want: "provider model references unknown model group"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := Default()
			value.Provider.Model = "active-model"
			if test.name == "unknown provider group" {
				value.Provider.Model = "group/missing"
			}
			value.ModelGroups = test.groups
			if test.agent != (AgentConfig{}) {
				value.Agents.Roles = map[string]AgentConfig{AgentRolePlanner: test.agent}
			}
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

func TestProviderReasoningEffortDefaultsAndPersistence(t *testing.T) {
	for _, effort := range []string{"", " \t\n", "low", "high", "provider-specific"} {
		t.Run(effort, func(t *testing.T) {
			value := Default()
			value.Provider.Model = "test-model"
			value.Provider.ReasoningEffort = effort
			store := Store{Dir: t.TempDir()}
			if err := value.Validate(); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(value); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			want := strings.TrimSpace(effort)
			if loaded.Provider.ReasoningEffort != want || value.Provider.EffectiveReasoningEffort() != want {
				t.Fatalf("saved effort = %q, effective effort = %q; want %q", loaded.Provider.ReasoningEffort, value.Provider.EffectiveReasoningEffort(), want)
			}
			body, err := os.ReadFile(store.Path())
			if err != nil {
				t.Fatal(err)
			}
			if want == "" && strings.Contains(string(body), "reasoning_effort") {
				t.Fatal("provider-default effort should be omitted from YAML")
			}
			for _, role := range AgentRoles() {
				agent, err := loaded.EffectiveAgent(role)
				if err != nil || agent.ReasoningEffort != "" {
					t.Fatalf("role %s inherited default chat effort: %#v, err = %v", role, agent, err)
				}
			}
		})
	}
}

func TestProviderReasoningEffortRejectsSurroundingWhitespace(t *testing.T) {
	value := Default()
	value.Provider.Model = "test-model"
	value.Provider.ReasoningEffort = " high "
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "provider reasoning_effort") {
		t.Fatalf("validation error = %v", err)
	}
	if err := (Store{Dir: t.TempDir()}).Save(value); err == nil {
		t.Fatal("saving a padded reasoning effort should fail")
	}
}
