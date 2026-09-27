package systemoneserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/systemoneconfig"
)

func TestServerRoutesModelsAndDecisionsAcrossProviders(t *testing.T) {
	first := testUpstream(t, "first-key", "first")
	defer first.Close()
	second := testUpstream(t, "second-key", "second")
	defer second.Close()

	value := systemoneconfig.Default()
	value.Providers = []systemoneconfig.ProviderConfig{
		{ID: "first", URI: first.URL + "/v1/systemone", APIKey: "first-key"},
		{ID: "second", URI: second.URL + "/v1/systemone", APIKey: "second-key"},
	}
	value.DefaultModel = "first/jev"
	value.Server.APIKey = "client-key"
	instance, err := New(value)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(instance.Handler())
	defer server.Close()

	response := send(t, http.MethodGet, server.URL+"/v1/models", nil, "client-key", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("models status = %d", response.StatusCode)
	}
	var catalog struct {
		Models []struct{ Name string } `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(catalog.Models) != 4 || catalog.Models[0].Name != "first/jev" ||
		catalog.Models[1].Name != "first/jev-preview" || catalog.Models[2].Name != "second/jev" ||
		catalog.Models[3].Name != "second/jev-preview" {
		t.Fatalf("models = %#v", catalog.Models)
	}

	for _, test := range []struct {
		model, wantProvider string
	}{
		{"first/jev", "first"},
		{"first/jev-preview", "first"},
		{"second/jev", "second"},
		{"jev", "first"},
	} {
		body := []byte(`{"model":"` + test.model + `","state":{"value":3},"questions":{"q":{"type":"choice","criteria":{"yes":"Y","no":"N"}}}}`)
		response = send(t, http.MethodPost, server.URL+"/v1/systemone", body, "client-key", "once-123")
		if response.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(response.Body)
			t.Fatalf("%s status = %d: %s", test.model, response.StatusCode, data)
		}
		var result struct {
			Model   string `json:"model"`
			Answers map[string]struct {
				Choice string `json:"choice"`
			} `json:"answers"`
		}
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if result.Model != test.model || result.Answers["q"].Choice != test.wantProvider {
			t.Fatalf("%s result = %#v", test.model, result)
		}
		if response.Header.Get("X-Request-Id") != test.wantProvider+"-request" {
			t.Fatalf("request ID = %q", response.Header.Get("X-Request-Id"))
		}
	}
}

func TestServerRejectsMissingClientKeyAndUnknownProvider(t *testing.T) {
	upstream := testUpstream(t, "provider-key", "first")
	defer upstream.Close()
	value := systemoneconfig.Default()
	value.Providers[0] = systemoneconfig.ProviderConfig{ID: "first", URI: upstream.URL + "/v1/systemone", APIKey: "provider-key"}
	value.DefaultModel = "first/jev"
	value.Server.APIKey = "client-key"
	instance, err := New(value)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(instance.Handler())
	defer server.Close()
	body := []byte(`{"model":"missing/jev","state":"x","questions":{}}`)
	response := send(t, http.MethodPost, server.URL+"/v1/systemone", body, "", "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d", response.StatusCode)
	}
	response.Body.Close()
	response = send(t, http.MethodPost, server.URL+"/v1/systemone", body, "client-key", "")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown provider status = %d", response.StatusCode)
	}
	response.Body.Close()
	body = []byte(`{"model":"first/jev","state":"x","questions":{}}`)
	response = send(t, http.MethodPost, server.URL+"/v1/systemone", body, "client-key", "invalid key")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid idempotency key status = %d", response.StatusCode)
	}
	response.Body.Close()
}

func TestServerManagedKeysEnableAndDisableAuthentication(t *testing.T) {
	upstream := testUpstream(t, "provider-key", "first")
	defer upstream.Close()
	store := systemoneconfig.Store{Dir: t.TempDir()}
	value := systemoneconfig.Default()
	value.Providers[0] = systemoneconfig.ProviderConfig{ID: "first", URI: upstream.URL + "/v1/systemone", APIKey: "provider-key"}
	value.DefaultModel = "first/jev"
	var err error
	value, first, err := store.CreateAPIKey(value, "first client", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	value, second, err := store.CreateAPIKey(value, "second client", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	masterKey, err := store.LoadMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	instance, err := NewWithMasterKey(value, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(instance.Handler())
	defer server.Close()
	checkStatus := func(key string, want int) {
		t.Helper()
		response := send(t, http.MethodGet, server.URL+"/v1/models", nil, key, "")
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("key %q: status = %d, want %d", key, response.StatusCode, want)
		}
	}
	checkStatus("", http.StatusUnauthorized)
	checkStatus(first.Secret, http.StatusOK)
	value, err = store.RevokeAPIKey(value, first.Record.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.ReloadAuthentication(value, masterKey); err != nil {
		t.Fatal(err)
	}
	checkStatus(first.Secret, http.StatusUnauthorized)
	checkStatus(second.Secret, http.StatusOK)
	value, err = store.RevokeAPIKey(value, second.Record.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.ReloadAuthentication(value, masterKey); err != nil {
		t.Fatal(err)
	}
	checkStatus("", http.StatusOK)
}

func TestServerPreservesProviderErrorAndRateLimitHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-System-One-Error-Code", "rate_limited")
		writer.Header().Set("X-RateLimit-Remaining", "0")
		writer.Header().Set("Retry-After", "2")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(writer, `{"error":{"message":"try again"}}`)
	}))
	defer upstream.Close()
	value := systemoneconfig.Default()
	value.Providers[0].URI = upstream.URL + "/v1/systemone"
	value.Providers[0].APIKey = "provider-key"
	instance, err := New(value)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(instance.Handler())
	defer server.Close()
	response := send(t, http.MethodPost, server.URL+"/v1/systemone",
		[]byte(`{"model":"jev","state":"x","questions":{}}`), "", "")
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusTooManyRequests ||
		response.Header.Get("X-System-One-Error-Code") != "rate_limited" ||
		response.Header.Get("X-RateLimit-Remaining") != "0" ||
		response.Header.Get("Retry-After") != "2" ||
		!strings.Contains(string(body), "try again") {
		t.Fatalf("provider error = %d %#v %s", response.StatusCode, response.Header, body)
	}
}

func testUpstream(t *testing.T, key, name string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+key {
			t.Errorf("%s upstream authorization = %q", name, request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(writer, `{"models":[{"name":"jev"},{"name":"jev-preview"}]}`)
		case "/v1/systemone":
			if request.Header.Get("Idempotency-Key") != "once-123" {
				t.Errorf("%s upstream idempotency key = %q", name, request.Header.Get("Idempotency-Key"))
			}
			data, _ := io.ReadAll(request.Body)
			if !bytes.Contains(data, []byte(`"model":"jev"`)) && !bytes.Contains(data, []byte(`"model":"jev-preview"`)) {
				t.Errorf("%s upstream body = %s", name, data)
			}
			writer.Header().Set("X-Request-Id", name+"-request")
			_, _ = io.WriteString(writer, `{"model":"jev","answers":{"q":{"type":"choice","choice":"`+name+`"}}}`)
		default:
			t.Errorf("%s upstream path = %s", name, request.URL.Path)
			http.NotFound(writer, request)
		}
	}))
}

func send(t *testing.T, method, url string, body []byte, key, idempotency string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	if idempotency != "" {
		request.Header.Set("Idempotency-Key", idempotency)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
