package systemoneconfig

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestStoreRoundTripAndAPIKeyFallback(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	initial, err := store.LoadOrDefault()
	if err != nil || initial != Default() {
		t.Fatalf("default = %#v, %v", initial, err)
	}
	t.Setenv("TYPESAFE_API_KEY", "environment-key")
	if initial.ResolveAPIKey() != "environment-key" {
		t.Fatal("environment API key was not used")
	}
	configured := Config{URI: "https://api.example.test/v2/systemone", APIKey: "saved-key", Model: "jev-preview"}
	if err := store.Save(configured); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadOrDefault()
	if err != nil || loaded != configured {
		t.Fatalf("loaded = %#v, %v", loaded, err)
	}
	if loaded.BaseURL() != "https://api.example.test/v2" || loaded.ResolveAPIKey() != "saved-key" {
		t.Fatalf("client settings = %q, %q", loaded.BaseURL(), loaded.ResolveAPIKey())
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

func TestNewClientUsesSavedEndpointAndKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" || request.Header.Get("Authorization") != "Bearer saved-key" {
			t.Errorf("request path = %q, authorization = %q", request.URL.Path, request.Header.Get("Authorization"))
		}
		_, _ = writer.Write([]byte(`{"models":[{"name":"jev-preview"}]}`))
	}))
	defer server.Close()
	store := Store{Dir: t.TempDir()}
	if err := store.Save(Config{URI: server.URL + "/v1/systemone", APIKey: "saved-key", Model: "jev-preview"}); err != nil {
		t.Fatal(err)
	}
	client, model, err := store.NewClient()
	if err != nil || model != "jev-preview" {
		t.Fatalf("NewClient = %q, %v", model, err)
	}
	models, err := client.ListModels(t.Context())
	if err != nil || len(models.Models) != 1 || models.Models[0].Name != "jev-preview" {
		t.Fatalf("models = %#v, %v", models, err)
	}
}

func TestValidateRejectsInvalidEndpointAndSecret(t *testing.T) {
	for _, uri := range []string{"", "https://user:pass@example.test/v1/systemone", "https://example.test/v1/systemone?key=secret", "https://example.test/v1/models"} {
		value := Default()
		value.URI = uri
		if err := value.Validate(); err == nil {
			t.Errorf("accepted URI %q", uri)
		}
	}
	value := Default()
	value.APIKey = "secret\nother"
	if err := value.Validate(); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("API key validation = %v", err)
	}
}

func TestBaseURLHandlesEncodedEndpointPath(t *testing.T) {
	value := Config{URI: "https://example.test/custom/%73ystemone", Model: "jev-latest"}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := value.BaseURL(); got != "https://example.test/custom" {
		t.Fatalf("BaseURL = %q", got)
	}
}
