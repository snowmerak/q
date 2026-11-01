package providerhost

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func TestOutputLimitsReachActualProviderAndPinRecovery(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			var seen map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/models" {
					_, _ = io.WriteString(w, `{"data":[{"id":"model"}]}`)
					return
				}
				if r.Header.Get(client.OutputLimitSnapshotHeader) != "" {
					t.Error("internal snapshot leaked upstream")
				}
				seen = nil
				if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
					t.Error(err)
				}
				_, _ = io.WriteString(w, `{"id":"ok","model":"model","choices":[],"status":"completed","output":[]}`)
			}))
			defer upstream.Close()
			g, err := gateway.New(gateway.Config{Providers: []gateway.ProviderConfig{{ID: "local", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL + "/v1", Models: []string{"model"}}}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = g.Close() }()
			store := config.Store{Dir: t.TempDir()}
			value := config.Default()
			value.Provider.Model = "local/model"
			value.ModelOutputLimits = map[string]int{"local/model": 128}
			if err := store.Save(value); err != nil {
				t.Fatal(err)
			}
			handler := ModelGroupHandler(g.Handler(), store)
			call := func(snapshot string, requested int) *httptest.ResponseRecorder {
				t.Helper()
				fields := map[string]any{"model": "local/model", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "input": "hi"}
				if requested > 0 {
					fields["max_tokens"] = requested
				}
				body, _ := json.Marshal(fields)
				request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
				request.Header.Set(client.OutputLimitSnapshotHeader, snapshot)
				r := httptest.NewRecorder()
				handler.ServeHTTP(r, request)
				if r.Code != 200 {
					t.Fatalf("request=%d %s", r.Code, r.Body.String())
				}
				return r
			}
			r := call("", 64)
			field := "max_completion_tokens"
			if path == "/v1/responses" {
				field = "max_output_tokens"
			}
			if seen[field] != float64(64) || seen["max_tokens"] != nil {
				t.Fatalf("wire=%+v", seen)
			}
			snapshot := r.Header().Get(client.OutputLimitSnapshotHeader)
			value.ModelOutputLimits["local/model"] = 8
			if err := store.Save(value); err != nil {
				t.Fatal(err)
			}
			call(snapshot, 64)
			if seen[field] != float64(64) {
				t.Fatalf("recovery did not retain limit: %+v", seen)
			}
			call("", 64)
			if seen[field] != float64(8) {
				t.Fatalf("next generation did not reload: %+v", seen)
			}
			delete(value.ModelOutputLimits, "local/model")
			if err := store.Save(value); err != nil {
				t.Fatal(err)
			}
			call("", 0)
			if seen[field] != nil {
				t.Fatalf("clear left a limit: %+v", seen)
			}
		})
	}
}

func TestOutputLimitsAreSelectedAfterFallback(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "a/model"
	value.ModelGroups = map[string]config.ModelGroupConfig{"work": {Candidates: []config.ModelCandidateConfig{{Model: "a/model"}, {Model: "b/model", ReasoningEffort: "high"}}}}
	value.ModelOutputLimits = map[string]int{"a/model": 16, "b/model": 32}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	var models []string
	var limits []int
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
			Limit int    `json:"max_completion_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		models = append(models, request.Model)
		limits = append(limits, request.Limit)
		if request.Model == "a/model" {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"model":"b/model","choices":[]}`)
	})
	r := httptest.NewRecorder()
	ModelGroupHandler(inner, store).ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"group/work","messages":[{"role":"user","content":"hi"}]}`)))
	if r.Code != 200 || len(limits) != 2 || limits[0] != 16 || limits[1] != 32 {
		t.Fatalf("models=%v limits=%v status=%d", models, limits, r.Code)
	}
	var snapshot client.OutputLimitSnapshot
	if json.Unmarshal([]byte(r.Header().Get(client.OutputLimitSnapshotHeader)), &snapshot) != nil || snapshot.Model != "b/model" || snapshot.Limit != 32 || snapshot.ReasoningEffort != "high" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}
