package systemoneconfig

import (
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestStoreRoundTripAndSelectedClient(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	initial, err := store.LoadOrDefault()
	if err != nil || !reflect.DeepEqual(initial, Default()) {
		t.Fatalf("default = %#v, %v", initial, err)
	}
	t.Setenv("TYPESAFE_API_KEY", "environment-key")
	if got := initial.Providers[0].ResolveAPIKey(); got != "environment-key" {
		t.Fatalf("default key = %q", got)
	}
	value := Default()
	value.Providers = append(value.Providers, ProviderConfig{
		ID: "second", URI: "https://api.example.test/v2/systemone",
		APIKey: "saved-key", Model: "jev-preview",
	})
	value.Selected = "second"
	value.Server.Port = 9393
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadOrDefault()
	if err != nil || !reflect.DeepEqual(loaded, value) {
		t.Fatalf("loaded = %#v, %v", loaded, err)
	}
	if got := loaded.Providers[1].BaseURL(); got != "https://api.example.test/v2" {
		t.Fatalf("base URL = %q", got)
	}
	body, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"api_key": "saved-key"`) || strings.Contains(string(body), "environment-key") {
		t.Fatal("saved settings did not preserve only the configured API key")
	}
	client, model, err := store.NewClient()
	if err != nil || client == nil || model != "jev-preview" {
		t.Fatalf("client from saved settings = %v, %q, %v", client, model, err)
	}
}

func TestLegacySingleProviderSettingsMigrateOnLoad(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := os.WriteFile(store.Path(), []byte(`{"uri":"https://example.test/v1/systemone","api_key":"old-key","model":"jev-preview"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := store.LoadOrDefault()
	if err != nil || len(value.Providers) != 1 || value.Selected != "typesafe" ||
		value.Providers[0].URI != "https://example.test/v1/systemone" ||
		value.Providers[0].APIKey != "old-key" || value.Providers[0].Model != "jev-preview" {
		t.Fatalf("migrated = %#v, %v", value, err)
	}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(store.Path())
	if !strings.Contains(string(body), `"providers"`) {
		t.Fatalf("saved shape = %s", body)
	}
}

func TestProviderClientUsesEndpointAndKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" || request.Header.Get("Authorization") != "Bearer saved-key" {
			t.Errorf("request path = %q, authorization = %q", request.URL.Path, request.Header.Get("Authorization"))
		}
		_, _ = writer.Write([]byte(`{"models":[{"name":"jev-preview"}]}`))
	}))
	defer server.Close()
	provider := ProviderConfig{ID: "test", URI: server.URL + "/v1/systemone", APIKey: "saved-key", Model: "jev-preview"}
	client, err := provider.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	models, err := client.ListModels(t.Context())
	if err != nil || len(models.Models) != 1 || models.Models[0].Name != "jev-preview" {
		t.Fatalf("models = %#v, %v", models, err)
	}
}

func TestValidateRejectsInvalidProviderAndPublicServerWithoutKey(t *testing.T) {
	for _, uri := range []string{"", "https://user:pass@example.test/v1/systemone", "https://example.test/v1/systemone?key=secret", "https://example.test/v1/models"} {
		value := Default()
		value.Providers[0].URI = uri
		if err := value.Validate(); err == nil {
			t.Errorf("accepted URI %q", uri)
		}
	}
	value := Default()
	value.Providers[0].APIKey = "secret\nother"
	if err := value.Validate(); err == nil || strings.Contains(err.Error(), "secret\nother") {
		t.Fatalf("API key validation = %v", err)
	}
	value = Default()
	value.Server.Host = "0.0.0.0"
	if err := value.Validate(); err == nil {
		t.Fatal("public server without API key was accepted")
	}
	value.Server.APIKey = "client-key"
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	value.Providers = append(value.Providers, value.Providers[0])
	if err := value.Validate(); err == nil {
		t.Fatal("duplicate provider ID was accepted")
	}
}

func TestBaseURLHandlesEncodedEndpointPath(t *testing.T) {
	provider := ProviderConfig{ID: "test", URI: "https://example.test/custom/%73ystemone", Model: DefaultModel}
	if err := provider.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := provider.BaseURL(); got != "https://example.test/custom" {
		t.Fatalf("BaseURL = %q", got)
	}
}
