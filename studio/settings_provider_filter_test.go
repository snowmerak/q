package studio

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/providerhost"
)

func TestProviderModelFilterSaveDiscoverRejectRenameAndClear(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-6","context_length":64000},{"id":"anthropic/claude"}]}`)
	}))
	defer upstream.Close()
	store := config.Store{Dir: t.TempDir()}
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	pattern := `^anthropic/`
	update := gatewayProviderUpdate{ID: "router", Prefix: "custom", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL + "/v1", ModelFilterRegex: &pattern}
	saved := serveJSON(t, handler, "POST", "/api/v1/settings/gateway/providers", update)
	if saved.Code != 200 {
		t.Fatal(saved.Body.String())
	}
	var snapshot settingsSnapshot
	if err := json.Unmarshal(saved.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Providers.Items[0].ModelFilterRegex != pattern {
		t.Fatal("snapshot omitted filter")
	}
	catalog := serveJSON(t, handler, "GET", "/api/v1/settings/models", nil)
	var models modelCatalog
	if err := json.Unmarshal(catalog.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	if catalog.Code != 200 || len(models.Models) != 1 || models.Models[0].ID != "custom/anthropic/claude" {
		t.Fatalf("filtered catalog = %d %s", catalog.Code, catalog.Body)
	}
	providers := providerhost.Store{Dir: store.Dir}
	beforeMain, _ := os.ReadFile(store.Path())
	beforeProviders, _ := os.ReadFile(providers.Path())
	invalid := "["
	update.ModelFilterRegex = &invalid
	rejected := serveJSON(t, handler, "PUT", "/api/v1/settings/gateway/providers/router", update)
	if rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid regex = %d %s", rejected.Code, rejected.Body)
	}
	afterMain, _ := os.ReadFile(store.Path())
	afterProviders, _ := os.ReadFile(providers.Path())
	if !reflect.DeepEqual(beforeMain, afterMain) || !reflect.DeepEqual(beforeProviders, afterProviders) {
		t.Fatal("invalid regex changed settings")
	}
	// Existing clients can omit the new field; renaming must move its ownership.
	update.ModelFilterRegex = nil
	update.ID = "renamed"
	saved = serveJSON(t, handler, "PUT", "/api/v1/settings/gateway/providers/router", update)
	if saved.Code != 200 {
		t.Fatal(saved.Body.String())
	}
	loaded, err := store.Load()
	if err != nil || loaded.ProviderModelFilters["renamed"] != pattern || len(loaded.ProviderModelFilters) != 1 {
		t.Fatalf("renamed filters = %v err=%v", loaded.ProviderModelFilters, err)
	}
	state, err := newSettingsService(store).loadTransferState()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := state.bundle()
	if err != nil {
		t.Fatal(err)
	}
	target := transferState{}
	if err := target.merge("providers", "model-filter/renamed", bundle.Sections["providers"]["model-filter/renamed"]); err != nil {
		t.Fatal(err)
	}
	if target.main.ProviderModelFilters["renamed"] != pattern {
		t.Fatal("transfer lost filter")
	}
	empty := ""
	update.ModelFilterRegex = &empty
	saved = serveJSON(t, handler, "PUT", "/api/v1/settings/gateway/providers/renamed", update)
	if saved.Code != 200 {
		t.Fatal(saved.Body.String())
	}
	catalog = serveJSON(t, handler, "GET", "/api/v1/settings/models", nil)
	_ = json.Unmarshal(catalog.Body.Bytes(), &models)
	if catalog.Code != 200 || len(models.Models) != 2 {
		t.Fatalf("cleared catalog = %d %s", catalog.Code, catalog.Body)
	}
	update.ModelFilterRegex = &pattern
	if r := serveJSON(t, handler, "PUT", "/api/v1/settings/gateway/providers/renamed", update); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	// Leave a second provider so deleting this one is permitted.
	update.ID, update.Prefix, update.ModelFilterRegex = "other", "other", nil
	if r := serveJSON(t, handler, "POST", "/api/v1/settings/gateway/providers", update); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r := serveJSON(t, handler, "DELETE", "/api/v1/settings/gateway/providers/renamed", nil); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	loaded, err = store.Load()
	if err != nil || len(loaded.ProviderModelFilters) != 0 {
		t.Fatalf("deleted filters = %v err=%v", loaded.ProviderModelFilters, err)
	}
}
