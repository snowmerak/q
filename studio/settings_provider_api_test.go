package studio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/providerhost"
)

func TestProviderAPIModeInitializesOnceAndPersists(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(studioTestModel))
	defer upstream.Close()
	for _, kind := range []string{"openai", "grok", "openrouter", "anthropic", "generic"} {
		t.Run(kind, func(t *testing.T) {
			store := config.Store{Dir: t.TempDir()}
			handler, err := newHandler(store)
			if err != nil {
				t.Fatal(err)
			}
			update := gatewayProviderUpdate{ID: "provider", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL + "/v1"}
			created := serveJSON(t, handler, http.MethodPost, "/api/v1/settings/gateway/providers", update)
			if created.Code != http.StatusOK {
				t.Fatal(created.Body.String())
			}
			update.Kind = kind
			saved := serveJSON(t, handler, http.MethodPut, "/api/v1/settings/gateway/providers/provider", update)
			if saved.Code != 200 {
				t.Fatal(saved.Body.String())
			}
			var snapshot settingsSnapshot
			_ = json.Unmarshal(saved.Body.Bytes(), &snapshot)
			expected := "chat_completions"
			if kind == "openai" || kind == "grok" || kind == "openrouter" {
				expected = "responses"
			}
			if snapshot.Providers.Items[0].APIMode != expected {
				t.Fatalf("initial mode=%s", snapshot.Providers.Items[0].APIMode)
			}
			chat := "chat_completions"
			update.APIMode = &chat
			if r := serveJSON(t, handler, http.MethodPut, "/api/v1/settings/gateway/providers/provider", update); r.Code != 200 {
				t.Fatal(r.Body.String())
			}
			update.APIMode = nil
			update.Prefix = "models"
			if r := serveJSON(t, handler, http.MethodPut, "/api/v1/settings/gateway/providers/provider", update); r.Code != 200 {
				t.Fatal(r.Body.String())
			}
			handler, err = newHandler(store)
			if err != nil {
				t.Fatal(err)
			}
			r := serveJSON(t, handler, http.MethodGet, "/api/v1/settings", nil)
			_ = json.Unmarshal(r.Body.Bytes(), &snapshot)
			if r.Code != 200 || snapshot.Providers.Items[0].APIMode != "chat_completions" {
				t.Fatalf("reloaded mode changed: %s", r.Body)
			}
			loaded, err := store.Load()
			if err != nil || loaded.ProviderAPIModes["provider"] != "chat_completions" {
				t.Fatalf("saved mode=%v err=%v", loaded.ProviderAPIModes, err)
			}
			update.ID = "renamed"
			if r := serveJSON(t, handler, http.MethodPut, "/api/v1/settings/gateway/providers/provider", update); r.Code != 200 {
				t.Fatal(r.Body.String())
			}
			loaded, _ = store.Load()
			if loaded.ProviderAPIModes["renamed"] != "chat_completions" {
				t.Fatal("rename lost option")
			}
			if _, ok := loaded.ProviderAPIModes["provider"]; ok {
				t.Fatal("rename retained old option")
			}
			// A conflicting provider default must not erase an explicit model choice.
			responses := "responses"
			update.APIMode = &responses
			if r := serveJSON(t, handler, http.MethodPut, "/api/v1/settings/gateway/providers/renamed", update); r.Code != 200 {
				t.Fatal(r.Body.String())
			}
			model := "models/test-model"
			if r := serveJSON(t, handler, http.MethodPut, "/api/v1/settings/model-api-mode", modelAPIModeUpdate{Model: model, Mode: "chat_completions"}); r.Code != 200 {
				t.Fatal(r.Body.String())
			}
			loaded, _ = store.Load()
			if loaded.ModelAPIModes[model] != "chat_completions" {
				t.Fatal("model Chat choice was erased")
			}
			if r := serveJSON(t, handler, http.MethodPut, "/api/v1/settings/model-api-mode", modelAPIModeUpdate{Model: model, Mode: ""}); r.Code != 200 {
				t.Fatal(r.Body.String())
			}
			loaded, _ = store.Load()
			if _, ok := loaded.ModelAPIModes[model]; ok {
				t.Fatal("inherit did not remove override")
			}
			// Export/import retains the provider option in the Providers section.
			service := newSettingsService(store)
			state, err := service.loadTransferState()
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := state.bundle()
			if err != nil {
				t.Fatal(err)
			}
			raw := bundle.Sections["providers"]["api-mode/renamed"]
			target := transferState{}
			if err := target.merge("providers", "api-mode/renamed", raw); err != nil {
				t.Fatal(err)
			}
			if target.main.ProviderAPIModes["renamed"] != "responses" {
				t.Fatal("transfer lost provider option")
			}
		})
	}
}

type providerModeFailure struct{ calls int }

func (f *providerModeFailure) ApplyGateway(context.Context, gateway.Config) error {
	f.calls++
	return errors.New("rejected provider update")
}

func TestProviderAPIModeFailedRoutingUpdateRestoresOption(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	main := config.Default()
	main.UseManagedGateway()
	main.ProviderAPIModes = map[string]string{"provider": "chat_completions"}
	if err := store.Save(main); err != nil {
		t.Fatal(err)
	}
	providers := gateway.Config{Providers: []gateway.ProviderConfig{{ID: "provider", Type: "openai-compatible", Enabled: true, BaseURL: "http://127.0.0.1:1/v1"}}}
	providerStore := providerhost.Store{Dir: store.Dir}
	if err := providerStore.Save(providers); err != nil {
		t.Fatal(err)
	}
	beforeMain, _ := os.ReadFile(store.Path())
	beforeProviders, _ := os.ReadFile(providerStore.Path())
	failure := &providerModeFailure{}
	service := newSettingsService(store, failure)
	previous := providers.Providers[0]
	next := previous
	next.Kind = "grok"
	providers.Providers[0] = next
	if err := service.saveProviderSettings(httptest.NewRequest("PUT", "/", nil), providers, previous, next, nil, nil); err == nil {
		t.Fatal("routing update unexpectedly succeeded")
	}
	afterMain, _ := os.ReadFile(store.Path())
	afterProviders, _ := os.ReadFile(providerStore.Path())
	if !reflect.DeepEqual(beforeMain, afterMain) || !reflect.DeepEqual(beforeProviders, afterProviders) {
		t.Fatal("failed routing update changed settings")
	}
}
