package providerhost

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
)

func TestProviderModelFilterUsesUpstreamID(t *testing.T) {
	routes := gateway.Config{Providers: []gateway.ProviderConfig{{ID: "router", Prefix: "custom"}, {ID: "other"}}}
	filter, err := NewProviderModelFilter(routes, map[string]string{"router": `(?i)^anthropic/claude-`, "other": `^gpt-`})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{
		"custom/anthropic/claude-sonnet": true, "custom/ANTHROPIC/CLAUDE-opus": true,
		"custom/openai/gpt-6": false, "other/gpt-6": true, "other/claude": false,
		"unfiltered/model": true, "group/main": true,
	} {
		if got := filter.Allows(id); got != want {
			t.Errorf("Allows(%q) = %v, want %v", id, got, want)
		}
	}
	if _, err := NewProviderModelFilter(routes, map[string]string{"router": "["}); err == nil {
		t.Fatal("invalid regex accepted")
	}
}

func TestProviderModelFilterCatalogReloadAndGroups(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.UseManagedGateway()
	value.ProviderModelFilters = map[string]string{"router": `^anthropic/`}
	value.ModelGroups = map[string]config.ModelGroupConfig{
		"claude": {Candidates: []config.ModelCandidateConfig{{Model: "custom/anthropic/claude"}}},
		"gpt":    {Candidates: []config.ModelCandidateConfig{{Model: "custom/openai/gpt"}}},
	}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	routes := gateway.Config{Providers: []gateway.ProviderConfig{{ID: "router", Prefix: "custom"}}}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Catalog", "preserved")
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"object":"list","extension":true,"data":[{"id":"custom/anthropic/claude","context_length":100000,"custom_metadata":{"value":42}},{"id":"custom/openai/gpt"},{"id":"other/model"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	filtered := ProviderModelFilterHandler(inner, routes, store)
	catalog := func(handler http.Handler) []string {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
		if w.Code != 200 || w.Header().Get("X-Catalog") != "preserved" {
			t.Fatalf("catalog = %d %s", w.Code, w.Body)
		}
		var envelope struct{ Data []struct{ ID string } }
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(envelope.Data))
		for _, model := range envelope.Data {
			ids = append(ids, model.ID)
		}
		return ids
	}
	if got := catalog(filtered); !reflect.DeepEqual(got, []string{"custom/anthropic/claude", "other/model"}) {
		t.Fatalf("filtered models = %v", got)
	}
	w := httptest.NewRecorder()
	filtered.ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	if !strings.Contains(w.Body.String(), `"extension":true`) || !strings.Contains(w.Body.String(), `"custom_metadata":{"value":42}`) {
		t.Fatalf("metadata lost: %s", w.Body)
	}
	handler := ModelGroupHandler(filtered, store)
	if got := catalog(handler); !reflect.DeepEqual(got, []string{"custom/anthropic/claude", "group/claude", "other/model"}) {
		t.Fatalf("group models = %v", got)
	}
	w = httptest.NewRecorder()
	filtered.ServeHTTP(w, httptest.NewRequest("GET", "/v1/models/custom/openai/gpt", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("hidden model detail = %d", w.Code)
	}
	// Filtering discovery must not break existing assignments or requests.
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/embeddings"} {
		w = httptest.NewRecorder()
		filtered.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"model":"custom/openai/gpt"}`)))
		if w.Code != 200 {
			t.Fatalf("request to %s blocked: %d", path, w.Code)
		}
	}
	value.ProviderModelFilters["router"] = `^openai/`
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	if got := catalog(handler); !reflect.DeepEqual(got, []string{"custom/openai/gpt", "group/gpt", "other/model"}) {
		t.Fatalf("reloaded models = %v", got)
	}
	delete(value.ProviderModelFilters, "router")
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	if got := catalog(filtered); len(got) != 3 {
		t.Fatalf("clearing filter retained %v", got)
	}
}
