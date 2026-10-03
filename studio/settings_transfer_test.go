package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/systemoneconfig"
)

func transferTestStore(t *testing.T) config.Store {
	t.Helper()
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "local/model"
	value.Provider.APIKey = "main-secret"
	value.Agents.Connections = map[string]config.AgentConnectionConfig{
		"codex": {Preset: "codex", Env: map[string]string{"TOKEN": "acp-secret"}},
	}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	return store
}

func transferRequest(t *testing.T, handler http.Handler, path string, bundle settingsBundle) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data)))
	return response
}

func transferTestBundle(t *testing.T, sections map[string]map[string]any) settingsBundle {
	t.Helper()
	bundle := settingsBundle{Format: "q-settings", Version: 1, Scope: "global", Sections: make(map[string]map[string]json.RawMessage)}
	for section, items := range sections {
		bundle.Sections[section] = make(map[string]json.RawMessage)
		for id, value := range items {
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			bundle.Sections[section][id] = data
		}
	}
	return bundle
}

func TestSettingsTransferExportsPortableValues(t *testing.T) {
	store := transferTestStore(t)
	providers := providerhost.Store{Dir: store.Dir}
	value := gateway.Config{Providers: []gateway.ProviderConfig{{ID: "local", Type: "openai-compatible", Prefix: "local", Enabled: true,
		BaseURL: "http://127.0.0.1:1", APIKey: "provider-secret", Headers: map[string]string{"Authorization": "header-secret"}, Body: map[string]any{"token": "body-secret"},
		APIKeyEnv: "PROVIDER_TOKEN", Codex: gateway.CodexConfig{Environment: map[string]string{"TOKEN": "codex-secret"}, ThreadStart: map[string]any{"token": "thread-secret"},
			ConversationCache: gateway.CodexConversationCacheConfig{Redis: gateway.CodexRedisConfig{Password: "redis-secret"}}},
	}}}
	if err := providers.Save(value); err != nil {
		t.Fatal(err)
	}
	one := systemoneconfig.Default()
	one.Server.APIKey = "system-one-secret"
	if err := (systemoneconfig.Store{Dir: store.Dir}).Save(one); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/transfer", nil))
	if response.Code != 200 {
		t.Fatalf("export = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), store.Dir) {
		t.Fatalf("export leaked local values: %s", response.Body.String())
	}
	var bundle settingsBundle
	if err := json.Unmarshal(response.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Format != "q-settings" || bundle.Version != 1 || bundle.Scope != "global" || len(bundle.Sections) != 6 {
		t.Fatalf("bundle = %#v", bundle)
	}
	provider, err := transferValue[gateway.ProviderConfig](bundle.Sections["providers"]["provider/local"])
	if err != nil || provider.APIKeyEnv != "PROVIDER_TOKEN" {
		t.Fatalf("portable provider = %#v %v", provider, err)
	}
	contextJSON := string(bundle.Sections["runtime"]["context"])
	if !strings.Contains(contextJSON, "trigger_ratio") {
		t.Fatalf("non-portable context fields: %s", contextJSON)
	}
}

func TestSettingsTransferMergesSelectedItemsAndPreservesSecrets(t *testing.T) {
	store := transferTestStore(t)
	value, _ := store.Load()
	value.ModelGroups = map[string]config.ModelGroupConfig{"keep": {Candidates: []config.ModelCandidateConfig{{Model: "local/keep"}}}}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	bundle := transferTestBundle(t, map[string]map[string]any{
		"models":       {"group/new": []modelCandidateSettings{{Model: "local/new", Timeout: "30s"}}, "role/coder": transferRole{Group: "new", ReasoningEffort: "high"}},
		"runtime":      {"parallel-agents": 7},
		"subagents":    {"connection/codex": config.AgentConnectionConfig{Preset: "codex", Args: []string{"--stdio"}}, "binding/search": "codex"},
		"integrations": {"mcp-server/docs": mcpconfig.ServerConfig{Transport: "stdio", Command: "docs-server", Env: map[string]string{"TOKEN": "DOCS_TOKEN"}}, "mcp-role/coder": []string{"docs"}},
	})
	before, _ := os.ReadFile(store.Path())
	preview := transferRequest(t, handler, "/api/v1/settings/transfer/preview", bundle)
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), `"action":"add"`) || !strings.Contains(preview.Body.String(), `"action":"replace"`) {
		t.Fatalf("preview = %d %s", preview.Code, preview.Body.String())
	}
	afterPreview, _ := os.ReadFile(store.Path())
	if !bytes.Equal(before, afterPreview) {
		t.Fatal("preview modified settings")
	}
	response := transferRequest(t, handler, "/api/v1/settings/transfer/import", bundle)
	if response.Code != 200 {
		t.Fatalf("import = %d %s", response.Code, response.Body.String())
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Provider.Model != "local/model" || loaded.Provider.APIKey != "main-secret" || loaded.Agents.Connections["codex"].Env["TOKEN"] != "acp-secret" {
		t.Fatalf("unselected or secret values changed: %#v", loaded)
	}
	if len(loaded.ModelGroups) != 2 || loaded.ModelGroups["new"].Candidates[0].Timeout != 30*time.Second || loaded.Agents.Roles["coder"].Group != "new" || loaded.Agents.MaxParallel != 7 {
		t.Fatalf("selected values = %#v", loaded)
	}
	mcp, err := (mcpconfig.Store{Dir: store.Dir}).Load()
	if err != nil || len(mcp.Roles["coder"]) != 1 {
		t.Fatalf("MCP = %#v %v", mcp, err)
	}
}

func TestSettingsTransferRejectsInvalidSelectionsBeforeWriting(t *testing.T) {
	store := transferTestStore(t)
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(store.Path())
	for name, sections := range map[string]map[string]map[string]any{
		"unknown section":       {"other": {"thing": 1}},
		"unknown empty section": {"other": {}},
		"unknown field":         {"runtime": {"context": map[string]any{"trigger_ratio": .8, "typo": 1}}},
		"missing group":         {"runtime": {"parallel-agents": 9}, "models": {"role/coder": transferRole{Group: "missing"}}},
		"missing connection":    {"subagents": {"binding/search": "missing"}},
		"missing MCP server":    {"integrations": {"mcp-role/coder": []string{"missing"}}},
		"invalid context":       {"runtime": {"context": contextSettings{TriggerRatio: .1, TargetRatio: .2, RecentRatio: .05}}},
		"ACP secrets":           {"subagents": {"connection/new": config.AgentConnectionConfig{Preset: "codex", Env: map[string]string{"TOKEN": "secret"}}}},
		"provider secrets":      {"providers": {"provider/new": gateway.ProviderConfig{ID: "new", APIKey: "secret"}}},
		"null":                  {"runtime": {"context": nil}},
		"empty":                 {},
	} {
		t.Run(name, func(t *testing.T) {
			response := transferRequest(t, handler, "/api/v1/settings/transfer/import", transferTestBundle(t, sections))
			if response.Code < 400 {
				t.Fatalf("accepted invalid selection: %s", response.Body.String())
			}
			after, _ := os.ReadFile(store.Path())
			if !bytes.Equal(before, after) {
				t.Fatal("invalid import changed settings")
			}
		})
	}
	for _, body := range []string{`{"format":"q-settings","version":2,"scope":"global","sections":{}}`, `{"format":"q-settings","version":1,"scope":"workspace","sections":{}}`, `{} {}`, strings.Repeat(" ", maximumTransferSize+1)} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/settings/transfer/import", strings.NewReader(body)))
		if response.Code != 400 {
			t.Fatalf("bad document = %d %s", response.Code, response.Body.String())
		}
	}
}

func TestSettingsTransferProfilesImportTogetherAndRoundTrip(t *testing.T) {
	store := transferTestStore(t)
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	first := subagent.Profile{Version: 1, Name: "first", Kind: "inner", Role: "coder", SystemPrompt: "First", Tools: []string{}, Delegates: []string{"global/second"}}
	second := subagent.Profile{Version: 1, Name: "second", Kind: "inner", Role: "reviewer", SystemPrompt: "Second", Tools: []string{}, Delegates: []string{}}
	bundle := transferTestBundle(t, map[string]map[string]any{"subagents": {"profile/first": first, "profile/second": second}})
	response := transferRequest(t, handler, "/api/v1/settings/transfer/import", bundle)
	if response.Code != 200 {
		t.Fatalf("profiles import = %d %s", response.Code, response.Body.String())
	}
	first.SystemPrompt = "Updated"
	bundle = transferTestBundle(t, map[string]map[string]any{"subagents": {"profile/first": first}})
	response = transferRequest(t, handler, "/api/v1/settings/transfer/import", bundle)
	if response.Code != 200 {
		t.Fatalf("profile replacement = %d %s", response.Code, response.Body.String())
	}
	state, err := newSettingsService(store).loadTransferState()
	if err != nil {
		t.Fatal(err)
	}
	exported, err := state.bundle()
	if err != nil {
		t.Fatal(err)
	}
	response = transferRequest(t, handler, "/api/v1/settings/transfer/import", exported)
	if response.Code != 200 {
		t.Fatalf("round trip = %d %s", response.Code, response.Body.String())
	}
	entry, err := profileStore(store, "").Get("first")
	if err != nil || entry.Profile.SystemPrompt != "Updated" {
		t.Fatalf("profile = %#v %v", entry, err)
	}
}

type failingTransferRuntime struct {
	store providerhost.Store
	calls int
}

func (runtime *failingTransferRuntime) ApplyGateway(_ context.Context, value gateway.Config) error {
	runtime.calls++
	if err := runtime.store.Save(value); err != nil {
		return err
	}
	if runtime.calls == 1 {
		return errors.New("runtime failed after writing")
	}
	return nil
}

func TestSettingsTransferRollsBackFilesAndRuntimeOnFailure(t *testing.T) {
	store := transferTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"data":[]}`) }))
	defer upstream.Close()
	providers := providerhost.Store{Dir: store.Dir}
	provider := gateway.ProviderConfig{ID: "local", Type: "openai-compatible", Prefix: "local", Enabled: true, BaseURL: upstream.URL, APIKey: "keep-secret"}
	if err := providers.Save(gateway.Config{Providers: []gateway.ProviderConfig{provider}}); err != nil {
		t.Fatal(err)
	}
	runtime := &failingTransferRuntime{store: providers}
	service := newSettingsService(store, runtime)
	beforeMain, _ := os.ReadFile(store.Path())
	beforeProvider, _ := os.ReadFile(providers.Path())
	provider.Models = []string{"new"}
	bundle := transferTestBundle(t, map[string]map[string]any{"runtime": {"parallel-agents": 11}, "providers": {"provider/local": portableProvider(provider)}})
	state, err := service.loadTransferState()
	if err != nil {
		t.Fatal(err)
	}
	for section, items := range bundle.Sections {
		for id, data := range items {
			if err := state.merge(section, id, data); err != nil {
				t.Fatal(err)
			}
		}
	}
	err = service.saveTransfer(httptest.NewRequest(http.MethodPost, "/", nil), state, bundle)
	if err == nil || runtime.calls != 2 {
		t.Fatalf("rollback = %v, runtime calls %d", err, runtime.calls)
	}
	afterMain, _ := os.ReadFile(store.Path())
	afterProvider, _ := os.ReadFile(providers.Path())
	if !bytes.Equal(beforeMain, afterMain) || !bytes.Equal(beforeProvider, afterProvider) {
		t.Fatal("failed import left settings changed")
	}
}

func TestSettingsTransferInitializesOnlyWhenDefaultModelIsSelected(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	services := transferTestBundle(t, map[string]map[string]any{"services": {"gateway": serviceUpdate{Host: "127.0.0.1", Port: 12345}}})
	response := transferRequest(t, handler, "/api/v1/settings/transfer/import", services)
	if response.Code != 200 {
		t.Fatalf("service-only import = %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(store.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("service-only import initialized main config")
	}
	bundle := transferTestBundle(t, map[string]map[string]any{"models": {"default": transferDefaultModel{Model: "local/first", ContextWindow: 64000}}, "runtime": {"parallel-agents": 4}})
	response = transferRequest(t, handler, "/api/v1/settings/transfer/import", bundle)
	if response.Code != 200 {
		t.Fatalf("initial import = %d %s", response.Code, response.Body.String())
	}
	loaded, err := store.Load()
	if err != nil || !loaded.Provider.Managed || loaded.Provider.Model != "local/first" || loaded.Provider.ContextWindow != 64000 {
		t.Fatalf("initialized = %#v %v", loaded, err)
	}
	keys := gatewayconfig.Store{Dir: store.Dir}
	if _, err := os.Stat(keys.MasterKeyPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("transfer created an authentication master key")
	}
	if _, err := os.Stat(filepath.Join(store.Dir, "subagents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("transfer created unrelated profile directory")
	}
}

func TestSettingsTransferPreservesProviderCredentialsOnSuccessfulImport(t *testing.T) {
	store := transferTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"data":[]}`) }))
	defer upstream.Close()
	providers := providerhost.Store{Dir: store.Dir}
	provider := gateway.ProviderConfig{ID: "local", Type: "openai-compatible", Prefix: "local", Enabled: true, BaseURL: upstream.URL,
		APIKey: "keep-key", Headers: map[string]string{"X-Token": "keep-header"}, Body: map[string]any{"token": "keep-body"},
		Codex: gateway.CodexConfig{Environment: map[string]string{"TOKEN": "keep-env"}, ThreadStart: map[string]any{"token": "keep-thread"},
			ConversationCache: gateway.CodexConversationCacheConfig{Redis: gateway.CodexRedisConfig{Password: "keep-password"}}},
	}
	if err := providers.Save(gateway.Config{Providers: []gateway.ProviderConfig{provider}}); err != nil {
		t.Fatal(err)
	}
	runtime := &settingsRuntimeRunner{providers: providers}
	handler, err := newHandlerWithRunner(store, runtime)
	if err != nil {
		t.Fatal(err)
	}
	portable := portableProvider(provider)
	portable.Models = []string{"imported-model"}
	bundle := transferTestBundle(t, map[string]map[string]any{"providers": {"provider/local": portable}})
	response := transferRequest(t, handler, "/api/v1/settings/transfer/import", bundle)
	if response.Code != 200 {
		t.Fatalf("provider import = %d %s", response.Code, response.Body.String())
	}
	loaded, err := providers.Load()
	if err != nil {
		t.Fatal(err)
	}
	actual := loaded.Providers[0]
	if len(actual.Models) != 1 || actual.Models[0] != "imported-model" || actual.APIKey != "keep-key" || actual.Headers["X-Token"] != "keep-header" || actual.Body["token"] != "keep-body" || actual.Codex.Environment["TOKEN"] != "keep-env" || actual.Codex.ThreadStart["token"] != "keep-thread" || actual.Codex.ConversationCache.Redis.Password != "keep-password" {
		t.Fatalf("provider credentials were overwritten: %#v", actual)
	}
	if len(runtime.applied.Providers) != 1 {
		t.Fatal("import did not apply Gateway runtime")
	}
}

type transferEmbeddingFailure struct{ settingsRuntimeRunner }

func (runtime *transferEmbeddingFailure) SyncEmbeddings(_ context.Context, _ string, value config.Config) error {
	runtime.syncValue = value
	return errors.New("index unavailable")
}

func TestSettingsTransferReportsDurableImportWhenReindexingFails(t *testing.T) {
	store := transferTestStore(t)
	runtime := &transferEmbeddingFailure{}
	handler, err := newHandlerWithRunner(store, runtime)
	if err != nil {
		t.Fatal(err)
	}
	bundle := transferTestBundle(t, map[string]map[string]any{"models": {"embedding": transferEmbedding{Model: "local/embed", Dimensions: 128}}})
	response := transferRequest(t, handler, "/api/v1/settings/transfer/import", bundle)
	if response.Code != 200 {
		t.Fatalf("durable import = %d %s", response.Code, response.Body.String())
	}
	var result transferImportResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Warning, "index unavailable") || result.Settings.Models.EmbeddingModel != "local/embed" || runtime.syncValue.Embedding.Dimensions != 128 {
		t.Fatalf("reindex warning = %#v", result)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Embedding.Model != "local/embed" {
		t.Fatalf("durable embedding = %#v %v", loaded.Embedding, err)
	}
}
