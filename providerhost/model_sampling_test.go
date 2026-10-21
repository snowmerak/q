package providerhost

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
)

func TestModelSamplingReachesUpstreamAndReloads(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			var seen map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/models" {
					_, _ = io.WriteString(w, `{"data":[{"id":"model"}]}`)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				seen = body
				w.Header().Set("Content-Type", "application/json")
				if path == "/v1/responses" {
					_, _ = io.WriteString(w, `{"id":"resp","model":"model","status":"completed","output":[]}`)
				} else {
					_, _ = io.WriteString(w, `{"id":"chat","model":"model","choices":[]}`)
				}
			}))
			defer upstream.Close()
			instance, err := gateway.New(gateway.Config{Providers: []gateway.ProviderConfig{{
				ID: "local", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL + "/v1", Models: []string{"model"},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = instance.Close() }()
			store := config.Store{Dir: t.TempDir()}
			value := config.Default()
			value.Provider.Model = "local/model"
			value.ModelSampling = map[string]config.SamplingConfig{"local/model": {
				Temperature: new(float64(0)), TopP: new(float64(1)), TopK: new(0), MinP: new(float64(0)),
				RepetitionPenalty: new(float64(1)), PresencePenalty: new(float64(0)), FrequencyPenalty: new(float64(0)),
			}}
			if err := store.Save(value); err != nil {
				t.Fatal(err)
			}
			handler := ModelGroupHandler(instance.Handler(), store)
			call := func() {
				t.Helper()
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"local/model","messages":[{"role":"user","content":"hi"}],"input":"hi","temperature":0.8,"top_p":0.7,"max_output_tokens":32}`)))
				if response.Code != http.StatusOK {
					t.Fatalf("response = %d %s", response.Code, response.Body.String())
				}
			}
			call()
			for key, want := range map[string]float64{"temperature": 0, "top_p": 1, "top_k": 0, "min_p": 0, "repetition_penalty": 1, "presence_penalty": 0, "frequency_penalty": 0} {
				if seen[key] != want {
					t.Errorf("upstream %s = %v, want %v", key, seen[key], want)
				}
			}
			// The same handler observes the next saved snapshot without a restart.
			value.ModelSampling["local/model"] = config.SamplingConfig{Temperature: new(0.3)}
			if err := store.Save(value); err != nil {
				t.Fatal(err)
			}
			call()
			if seen["temperature"] != 0.3 || seen["top_p"] != 0.7 || seen["top_k"] != nil {
				t.Fatalf("reloaded upstream = %#v", seen)
			}
			delete(value.ModelSampling, "local/model")
			if err := store.Save(value); err != nil {
				t.Fatal(err)
			}
			call()
			if seen["temperature"] != 0.8 {
				t.Fatalf("cleared override = %#v", seen)
			}
		})
	}
}

func TestModelSamplingUsesEachFallbackWithoutLeakingValues(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	writeModelGroupConfig(t, store, config.ModelGroupConfig{Candidates: []config.ModelCandidateConfig{{Model: "primary/model"}, {Model: "backup/model"}}})
	value, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	value.ModelSampling = map[string]config.SamplingConfig{
		"primary/model": {Temperature: new(float64(0)), TopK: new(0)},
		"backup/model":  {Temperature: new(0.4)},
	}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	var seen []map[string]any
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		seen = append(seen, body)
		if body["model"] == "primary/model" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"model":"backup/model","choices":[]}`)
	})
	response := httptest.NewRecorder()
	ModelGroupHandler(inner, store).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"group/heavy","temperature":0.9,"top_p":0.8}`)))
	if response.Code != http.StatusOK || len(seen) != 2 {
		t.Fatalf("response = %d, calls = %#v", response.Code, seen)
	}
	if seen[0]["temperature"] != float64(0) || seen[0]["top_k"] != float64(0) || seen[1]["temperature"] != 0.4 || seen[1]["top_k"] != nil || seen[1]["top_p"] != 0.8 {
		t.Fatalf("candidate sampling = %#v", seen)
	}
}
