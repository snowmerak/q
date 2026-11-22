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

	"github.com/snowmerak/decisions"
	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/systemoneconfig"
)

func TestServerRoutesDecisionProtocols(t *testing.T) {
	for _, fixture := range []struct {
		api      decisions.API
		catalog  string
		decision string
	}{
		{
			api:     decisions.DecisionAPI,
			catalog: `{"data":[{"id":"decision-model"}]}`,
			decision: `{"model":"decision-model","usage":{"input_tokens":7,"output_tokens":1},"answers":[` +
				`{"name":"ok","type":"predicate","probability":0.8},` +
				`{"name":"team","type":"choice","choice":"yes","confidence":0.75,"probabilities":[{"value":"yes","probability":0.75},{"value":"none","probability":0.25}]},` +
				`{"name":"rank","type":"score","score":0.75,"confidence":0.75,"probabilities":[{"value":0,"probability":0.25},{"value":1,"probability":0.75}]}]}`,
		},
		{
			api:     decisions.GeminiDecisionAPI,
			catalog: `{"models":[{"name":"models/decision-model","supportedGenerationMethods":["createDecision"]},{"name":"models/chat-model","supportedGenerationMethods":["generateContent"]}]}`,
			decision: `{"usage":{"total_input_tokens":7,"total_output_tokens":1},"answers":{` +
				`"ok":{"type":"noul","noul":0.8,"confidence":0.9},` +
				`"team":{"type":"choice","choice":"yes","confidence":0.75,"probabilities":{"yes":0.75,"none":0.25}},` +
				`"rank":{"type":"score","score":0.75,"confidence":0.75,"probabilities":{"0":0.25,"1":0.75}}}}`,
		},
	} {
		t.Run(string(fixture.api), func(t *testing.T) {
			t.Setenv("Q_TEST_DECISIONS_KEY", "fixture-key")
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				bearer, google := "Bearer fixture-key", ""
				if fixture.api == decisions.GeminiDecisionAPI {
					bearer, google = "", "fixture-key"
				}
				if request.Header.Get("Authorization") != bearer || request.Header.Get("X-Goog-Api-Key") != google {
					t.Error("incorrect upstream authentication headers")
				}
				switch {
				case request.Method == http.MethodGet && request.URL.Path == "/prefix/v1/models":
					_, _ = io.WriteString(writer, fixture.catalog)
				case request.Method == http.MethodPost && request.URL.Path == "/prefix/v1/decisions":
					if request.Header.Get("Idempotency-Key") != "fixture-id" {
						t.Error("idempotency key was lost")
					}
					var payload struct {
						Model string `json:"model"`
						Input string `json:"input"`
					}
					if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.Model != "decision-model" || payload.Input != `{"ticket":"sample"}` {
						t.Errorf("translated payload = %+v, %v", payload, err)
					}
					writer.Header().Set("X-Request-ID", "upstream-request")
					_, _ = io.WriteString(writer, fixture.decision)
				default:
					http.NotFound(writer, request)
				}
			}))
			defer upstream.Close()

			value := systemoneconfig.Default()
			value.Providers = []systemoneconfig.ProviderConfig{{ID: "fixture", API: fixture.api, URI: upstream.URL + "/prefix/v1/decisions", APIKeyEnv: "Q_TEST_DECISIONS_KEY"}}
			value.DefaultModel = "fixture/decision-model"
			instance, err := New(value)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(instance.Handler())
			defer server.Close()
			client, err := systemone.New(systemone.Config{BaseURL: server.URL + "/v1"})
			if err != nil {
				t.Fatal(err)
			}
			models, err := client.ListModels(t.Context())
			if err != nil || len(models.Models) != 1 || models.Models[0].Name != value.DefaultModel {
				t.Fatalf("catalog = %+v, %v", models, err)
			}
			result, err := client.Evaluate(t.Context(), systemone.Request{
				Model: value.DefaultModel,
				State: map[string]string{"ticket": "sample"},
				Questions: map[string]systemone.Question{
					"ok":   {Type: systemone.QuestionNoul, Instructions: "Does this match?"},
					"team": {Type: systemone.QuestionChoice, Instructions: "Choose a team", Criteria: map[string]any{"yes": map[string]string{"role": "engineer"}, "none": "No team"}},
					"rank": {Type: systemone.QuestionScore, Instructions: map[string]string{"task": "Rank this item"}, Criteria: []string{"low", "high"}},
				},
			}, systemone.CallOptions{IdempotencyKey: "fixture-id"})
			if err != nil {
				t.Fatal(err)
			}
			if result.Model != value.DefaultModel || result.Answers["team"].Choice != "yes" ||
				result.Answers["rank"].Score == nil || *result.Answers["rank"].Score != 0.75 ||
				result.Answers["ok"].Noul == nil || *result.Answers["ok"].Noul != 0.8 ||
				result.Header.Get("X-Request-ID") != "upstream-request" ||
				result.Usage == nil || result.Usage.InputTokens == nil || *result.Usage.InputTokens != 7 {
				t.Fatalf("normalized decision = %+v", result)
			}
		})
	}
}

func TestServerRoutesModelsAndDecisionsAcrossProviders(t *testing.T) {
	t.Setenv("SYSTEMONE_FIRST_PROVIDER_KEY", "first-key")
	t.Setenv("SYSTEMONE_SECOND_PROVIDER_KEY", "second-key")
	first := testUpstream(t, "first-key", "first")
	defer first.Close()
	second := testUpstream(t, "second-key", "second")
	defer second.Close()

	value := systemoneconfig.Default()
	value.Providers = []systemoneconfig.ProviderConfig{
		{ID: "first", URI: first.URL + "/v1/systemone", APIKeyEnv: "SYSTEMONE_FIRST_PROVIDER_KEY"},
		{ID: "second", URI: second.URL + "/v1/systemone", APIKeyEnv: "SYSTEMONE_SECOND_PROVIDER_KEY"},
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
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
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
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
		if result.Model != test.model || result.Answers["q"].Choice != test.wantProvider {
			t.Fatalf("%s result = %#v", test.model, result)
		}
		if response.Header.Get("X-Request-Id") != test.wantProvider+"-request" {
			t.Fatalf("request ID = %q", response.Header.Get("X-Request-Id"))
		}
	}
}

func TestServerRejectsMissingClientKeyAndUnknownProvider(t *testing.T) {
	t.Setenv("SYSTEMONE_PROVIDER_KEY", "provider-key")
	upstream := testUpstream(t, "provider-key", "first")
	defer upstream.Close()
	value := systemoneconfig.Default()
	value.Providers[0] = systemoneconfig.ProviderConfig{ID: "first", URI: upstream.URL + "/v1/systemone", APIKeyEnv: "SYSTEMONE_PROVIDER_KEY"}
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
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
	response = send(t, http.MethodPost, server.URL+"/v1/systemone", body, "client-key", "")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown provider status = %d", response.StatusCode)
	}
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
	body = []byte(`{"model":"first/jev","state":"x","questions":{}}`)
	response = send(t, http.MethodPost, server.URL+"/v1/systemone", body, "client-key", "invalid key")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid idempotency key status = %d", response.StatusCode)
	}
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
}

func TestServerManagedKeysEnableAndDisableAuthentication(t *testing.T) {
	t.Setenv("SYSTEMONE_PROVIDER_KEY", "provider-key")
	upstream := testUpstream(t, "provider-key", "first")
	defer upstream.Close()
	store := systemoneconfig.Store{Dir: t.TempDir()}
	value := systemoneconfig.Default()
	value.Providers[0] = systemoneconfig.ProviderConfig{ID: "first", URI: upstream.URL + "/v1/systemone", APIKeyEnv: "SYSTEMONE_PROVIDER_KEY"}
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
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
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
	t.Setenv("SYSTEMONE_PROVIDER_KEY", "provider-key")
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
	value.Providers[0].APIKeyEnv = "SYSTEMONE_PROVIDER_KEY"
	instance, err := New(value)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(instance.Handler())
	defer server.Close()
	response := send(t, http.MethodPost, server.URL+"/v1/systemone",
		[]byte(`{"model":"jev","state":"x","questions":{}}`), "", "")
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
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
