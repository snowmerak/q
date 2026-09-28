package studio

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/systemoneconfig"
)

func TestHandlerServesGlobalStatusAndSPA(t *testing.T) {
	handler, err := NewHandler()
	if err != nil {
		t.Fatal(err)
	}
	if server, ok := handler.(*Server); ok {
		t.Cleanup(func() {
			if err := server.Close(); err != nil {
				t.Error(err)
			}
		})
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	statusRecorder := httptest.NewRecorder()
	handler.ServeHTTP(statusRecorder, statusRequest)
	var status statusResponse
	if err := json.Unmarshal(statusRecorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if statusRecorder.Code != http.StatusOK || status.Version != apiVersion || status.Service != "studio" || !status.Ready {
		t.Fatalf("status = %d %#v", statusRecorder.Code, status)
	}
	if strings.Contains(statusRecorder.Body.String(), "workspace") {
		t.Fatalf("global Studio status leaked workspace context: %s", statusRecorder.Body.String())
	}

	for _, target := range []string{"/", "/sessions/example"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("Accept", "text/html")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		body, _ := io.ReadAll(response.Result().Body)
		if response.Code != http.StatusOK || !strings.Contains(string(body), "Q Studio") {
			t.Fatalf("GET %s = %d %q", target, response.Code, body)
		}
	}

	missingAsset := httptest.NewRecorder()
	handler.ServeHTTP(missingAsset, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if missingAsset.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d", missingAsset.Code)
	}

	missingAPI := httptest.NewRecorder()
	handler.ServeHTTP(missingAPI, httptest.NewRequest(http.MethodGet, "/api/v1/missing", nil))
	if missingAPI.Code != http.StatusNotFound {
		t.Fatalf("missing API status = %d", missingAPI.Code)
	}
}

func TestSettingsAPIReadsAndUpdatesGlobalStores(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/models":
			_, _ = io.WriteString(writer, `{"object":"list","data":[{"id":"model-a","object":"model","context_length":32000}]}`)
		case "/v1/models":
			_, _ = io.WriteString(writer, `{"models":[{"name":"jev-a","description":"test decision model","release_date":"2026-01-01"}]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer upstream.Close()
	value := config.Default()
	value.Provider.Model = "provider/default-model"
	value.Embedding = config.EmbeddingConfig{Model: "provider/embedding-model", Dimensions: 768}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	if err := (providerhost.Store{Dir: store.Dir}).Save(gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "test", Type: "openai-compatible", Prefix: "test", Enabled: true, BaseURL: upstream.URL, APIKey: "top-secret",
	}}}); err != nil {
		t.Fatal(err)
	}
	systemOneStore := systemoneconfig.Store{Dir: store.Dir}
	systemOneValue := systemoneconfig.Default()
	systemOneValue.Providers = []systemoneconfig.ProviderConfig{{ID: "decision", URI: upstream.URL + "/v1/systemone"}}
	systemOneValue.DefaultModel = "decision/jev-a"
	if err := systemOneStore.Save(systemOneValue); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET settings = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "top-secret") {
		t.Fatal("settings response exposed an inline provider API key")
	}
	var snapshot settingsSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Scope != "global" || !snapshot.Runtime.Configured || snapshot.Models.DefaultModel != value.Provider.Model {
		t.Fatalf("settings snapshot = %#v", snapshot)
	}
	if len(snapshot.Providers.Items) != 1 || snapshot.Providers.Items[0].ID != "test" {
		t.Fatalf("provider settings = %#v", snapshot.Providers)
	}
	if len(snapshot.SystemOne.Providers) != 1 || snapshot.SystemOne.Providers[0].ID != "decision" || snapshot.SystemOne.DefaultModel != "decision/jev-a" {
		t.Fatalf("System One settings = %#v", snapshot.SystemOne)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/models", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"test/model-a"`) {
		t.Fatalf("GET models = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/models/reviewer", bytes.NewBufferString(`{"model":"test/model-a","reasoning_effort":"high"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT reviewer model = %d %s", response.Code, response.Body.String())
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Agents.Roles[config.AgentRoleReviewer].Model != "test/model-a" || loaded.Agents.Roles[config.AgentRoleReviewer].ReasoningEffort != "high" {
		t.Fatalf("saved reviewer assignment = %#v", loaded.Agents.Roles[config.AgentRoleReviewer])
	}

	runtimeBody := `{"max_parallel":5,"context":{"window":120000,"trigger_ratio":0.8,"target_ratio":0.25,"recent_ratio":0.08},"loom":{"maximum_artifact_mib":96,"maximum_store_mib":512,"gc_disabled":false,"gc_trigger_ratio":0.82,"gc_target_ratio":0.61,"gc_grace_hours":3}}`
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/runtime", bytes.NewBufferString(runtimeBody)))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT runtime = %d %s", response.Code, response.Body.String())
	}
	loaded, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Agents.MaxParallel != 5 || loaded.Context.Window != 120000 || loaded.Loom.MaximumStoreMiB != 512 {
		t.Fatalf("saved runtime = %#v", loaded)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/services/gateway", bytes.NewBufferString(`{"host":"127.0.0.2","port":8181}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT gateway = %d %s", response.Code, response.Body.String())
	}
	gateway, err := (gatewayconfig.Store{Dir: store.Dir}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if gateway.Server.Host != "127.0.0.2" || gateway.Server.Port != 8181 {
		t.Fatalf("saved Gateway = %#v", gateway)
	}

	providerBody := `{"id":"test","type":"openai-compatible","kind":"generic","prefix":"models","enabled":true,"base_url":"` + upstream.URL + `","api_key_env":""}`
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/gateway/providers/test", bytes.NewBufferString(providerBody)))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT provider = %d %s", response.Code, response.Body.String())
	}
	providers, err := (providerhost.Store{Dir: store.Dir}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(providers.Providers) != 1 || providers.Providers[0].Prefix != "models" || providers.Providers[0].Kind != "generic" || providers.Providers[0].APIKey != "top-secret" {
		t.Fatalf("saved providers = %#v", providers.Providers)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/system-one/models", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"decision/jev-a"`) {
		t.Fatalf("GET System One models = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/system-one/models/agent-skill-decision", bytes.NewBufferString(`{"model":"decision/jev-a"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT System One role model = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/system-one/providers/decision", bytes.NewBufferString(`{"id":"decisions","uri":"`+upstream.URL+`/v1/systemone","api_key_env":""}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT System One provider = %d %s", response.Code, response.Body.String())
	}
	systemOneValue, err = systemOneStore.LoadOrDefault()
	if err != nil {
		t.Fatal(err)
	}
	if systemOneValue.DefaultModel != "decisions/jev-a" || systemOneValue.RoleModels[systemoneconfig.RoleAgentSkillDecision] != "decisions/jev-a" {
		t.Fatalf("renamed System One model references = %#v", systemOneValue)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/settings/system-one/api-keys", bytes.NewBufferString(`{"alias":"Studio test"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("POST System One API key = %d %s", response.Code, response.Body.String())
	}
	var created apiKeyCreateResponse
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Secret, "qso_") || created.Settings.SystemOne.ActiveAPIKeys != 1 || len(created.Settings.SystemOne.APIKeys) != 1 {
		t.Fatalf("created System One API key = %#v", created)
	}
	keyID := created.Settings.SystemOne.APIKeys[0].ID
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/settings/system-one/api-keys/"+keyID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("DELETE System One API key = %d %s", response.Code, response.Body.String())
	}
	systemOneValue, err = systemOneStore.LoadOrDefault()
	if err != nil || systemOneValue.ActiveKeyCount() != 0 || systemOneValue.APIKeys[0].RevokedAt == nil {
		t.Fatalf("revoked System One API key = %#v, %v", systemOneValue.APIKeys, err)
	}
}

func TestSettingsAPIRejectsInvalidAndUnknownUpdates(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "provider/model"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		target string
		body   string
		status int
	}{
		{"/api/v1/settings/services/missing", `{"host":"127.0.0.1","port":0}`, http.StatusNotFound},
		{"/api/v1/settings/services/gateway", `{"host":"example.com","port":0}`, http.StatusUnprocessableEntity},
		{"/api/v1/settings/runtime", `{"unknown":true}`, http.StatusBadRequest},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, test.target, bytes.NewBufferString(test.body)))
		if response.Code != test.status {
			t.Fatalf("PUT %s = %d %s; want %d", test.target, response.Code, response.Body.String(), test.status)
		}
	}
}
