package studio

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/systemoneconfig"
)

func TestStudioProviderCreateDeletePersistsAndRejectsDuplicates(t *testing.T) {
	for _, kind := range []string{"gateway", "system-one"} {
		t.Run(kind, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
			}))
			defer upstream.Close()
			store := config.Store{Dir: t.TempDir()}
			main := config.Default()
			main.Provider.Model = "original/test-model"
			if err := store.Save(main); err != nil {
				t.Fatal(err)
			}
			providers := providerhost.Store{Dir: store.Dir}
			if err := providers.Save(gateway.Config{Providers: []gateway.ProviderConfig{{ID: "original", Type: "openai-compatible", Prefix: "original", Enabled: true, BaseURL: upstream.URL}}}); err != nil {
				t.Fatal(err)
			}
			decisions := systemoneconfig.Store{Dir: store.Dir}
			value := systemoneconfig.Default()
			value.Providers = []systemoneconfig.ProviderConfig{{ID: "original", URI: upstream.URL + "/v1/systemone"}}
			value.DefaultModel = "original/test-model"
			if err := decisions.Save(value); err != nil {
				t.Fatal(err)
			}
			handler, err := newHandler(store)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := "/api/v1/settings/" + kind + "/providers"
			var update any = map[string]any{"id": "extra", "type": "openai-compatible", "prefix": "extra", "enabled": true, "base_url": upstream.URL}
			if kind == "system-one" {
				update = map[string]any{"id": "extra", "uri": upstream.URL + "/v1/systemone", "api_key_env": ""}
			}
			created := serveJSON(t, handler, http.MethodPost, endpoint, update)
			if created.Code != http.StatusOK {
				t.Fatalf("create = %d %s", created.Code, created.Body.String())
			}
			duplicate := serveJSON(t, handler, http.MethodPost, endpoint, update)
			if duplicate.Code != http.StatusUnprocessableEntity {
				t.Fatalf("duplicate = %d %s", duplicate.Code, duplicate.Body.String())
			}
			assertCount := func(want int) {
				t.Helper()
				if kind == "gateway" {
					loaded, err := providers.Load()
					if err != nil || len(loaded.Providers) != want {
						t.Fatalf("persisted providers = %#v, %v", loaded, err)
					}
				} else {
					loaded, err := decisions.LoadOrDefault()
					if err != nil || len(loaded.Providers) != want {
						t.Fatalf("persisted providers = %#v, %v", loaded, err)
					}
				}
			}
			assertCount(2)
			// A new handler reads the stored configuration, not the first
			// handler's in-memory state.
			handler, err = newHandler(store)
			if err != nil {
				t.Fatal(err)
			}
			deleted := serveJSON(t, handler, http.MethodDelete, endpoint+"/extra", nil)
			if deleted.Code != http.StatusOK {
				t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
			}
			assertCount(1)
			missing := serveJSON(t, handler, http.MethodDelete, endpoint+"/extra", nil)
			if missing.Code != http.StatusNotFound {
				t.Fatalf("missing = %d %s", missing.Code, missing.Body.String())
			}
			last := serveJSON(t, handler, http.MethodDelete, endpoint+"/original", nil)
			if last.Code != http.StatusUnprocessableEntity {
				t.Fatalf("last provider = %d %s", last.Code, last.Body.String())
			}
			assertCount(1)
		})
	}
}

type failingSettingsRuntime struct{ settingsRuntimeRunner }

func (*failingSettingsRuntime) ApplyGateway(context.Context, gateway.Config) error {
	return errors.New("test child startup failed")
}

func TestStudioFailedProviderApplyPreservesSavedConfiguration(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	main := config.Default()
	main.Provider.Model = "original/test-model"
	if err := store.Save(main); err != nil {
		t.Fatal(err)
	}
	providers := providerhost.Store{Dir: store.Dir}
	if err := providers.Save(gateway.Config{Providers: []gateway.ProviderConfig{{ID: "original", Type: "openai-compatible", Prefix: "original", Enabled: true, BaseURL: "http://127.0.0.1:1"}}}); err != nil {
		t.Fatal(err)
	}
	path := providers.Path()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newHandlerWithRunner(store, &failingSettingsRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	response := serveJSON(t, handler, http.MethodPut, "/api/v1/settings/gateway/providers/original", map[string]any{"id": "original", "type": "openai-compatible", "prefix": "original", "enabled": true, "base_url": "http://127.0.0.1:2"})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("failed apply = %d %s", response.Code, response.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed apply changed settings: %s, %v", after, err)
	}
}
