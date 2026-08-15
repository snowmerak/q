package providerhost

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func writeModelGroupConfig(t *testing.T, store config.Store, group config.ModelGroupConfig) {
	t.Helper()
	value := config.Default()
	value.Provider.Model = "primary/model"
	value.ModelGroups = map[string]config.ModelGroupConfig{"heavy": group}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
}

func TestModelGroupHandlerExposesMinimumModelLimits(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	writeModelGroupConfig(t, store, config.ModelGroupConfig{Candidates: []config.ModelCandidateConfig{
		{Model: "primary/model"}, {Model: "backup/model"},
	}})
	inner := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"object":"list","data":[`+
			`{"id":"primary/model","object":"model","context_length":200000,"max_output_tokens":16000},`+
			`{"id":"backup/model","object":"model","context_length":128000,"max_output_tokens":8000}]}`)
	})
	handler := ModelGroupHandler(inner, store)

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data []struct {
			ID              string `json:"id"`
			ContextLength   int64  `json:"context_length"`
			MaxOutputTokens int64  `json:"max_output_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, model := range envelope.Data {
		if model.ID == "group/heavy" {
			found = true
			if model.ContextLength != 128000 || model.MaxOutputTokens != 8000 {
				t.Fatalf("group model = %#v", model)
			}
		}
	}
	if !found {
		t.Fatalf("models = %#v", envelope.Data)
	}

	detail := httptest.NewRecorder()
	handler.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/v1/models/group/heavy", nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"id":"group/heavy"`) {
		t.Fatalf("detail = %d %s", detail.Code, detail.Body.String())
	}
}

func TestSyntheticGroupModelsKeepsUnknownLimitUnknown(t *testing.T) {
	groups := map[string]config.ModelGroupConfig{
		"heavy": {Candidates: []config.ModelCandidateConfig{{Model: "known"}, {Model: "unknown-limits"}}},
	}
	models := syntheticGroupModels(groups, []client.Model{
		{ID: "known", ContextLength: 128_000, MaxOutputTokens: 8_000},
		{ID: "unknown-limits"},
	})
	if len(models) != 1 || models[0].ID != "group/heavy" || models[0].ContextLength != 0 || models[0].MaxOutputTokens != 0 {
		t.Fatalf("models = %#v", models)
	}
}

func TestModelGroupHandlerFallsBackAndRewritesExternalModel(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	writeModelGroupConfig(t, store, config.ModelGroupConfig{Candidates: []config.ModelCandidateConfig{
		{Model: "primary/model", ReasoningEffort: "high"},
		{Model: "backup/model", ReasoningEffort: "medium"},
	}})
	var mu sync.Mutex
	var calls []map[string]any
	inner := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		mu.Lock()
		calls = append(calls, body)
		mu.Unlock()
		if body["model"] == "primary/model" {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(writer, `{"error":{"message":"temporary"}}`)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Content-Length", "999")
		_, _ = io.WriteString(writer, `{"id":"chat","model":"backup/model","choices":[]}`)
	})
	handler := ModelGroupHandler(inner, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"group/heavy","messages":[{"role":"user","content":"hello"}]}`,
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"model":"group/heavy"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if value := response.Header().Get("Content-Length"); value != "" {
		t.Fatalf("rewritten response retained stale Content-Length %q", value)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0]["model"] != "primary/model" || calls[0]["reasoning_effort"] != "high" ||
		calls[1]["model"] != "backup/model" || calls[1]["reasoning_effort"] != "medium" {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestModelGroupHandlerCandidateTimeoutAndHTTP4xxPolicy(t *testing.T) {
	t.Run("timeout falls back", func(t *testing.T) {
		store := config.Store{Dir: t.TempDir()}
		writeModelGroupConfig(t, store, config.ModelGroupConfig{Candidates: []config.ModelCandidateConfig{
			{Model: "slow/model", Timeout: 5 * time.Millisecond}, {Model: "backup/model"},
		}})
		inner := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["model"] == "slow/model" {
				<-request.Context().Done()
				writer.WriteHeader(http.StatusGatewayTimeout)
				return
			}
			_, _ = io.WriteString(writer, `{"model":"backup/model","choices":[]}`)
		})
		response := httptest.NewRecorder()
		ModelGroupHandler(inner, store).ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"group/heavy","messages":[]}`),
		))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "group/heavy") {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("4xx stops", func(t *testing.T) {
		store := config.Store{Dir: t.TempDir()}
		writeModelGroupConfig(t, store, config.ModelGroupConfig{Candidates: []config.ModelCandidateConfig{
			{Model: "bad/model"}, {Model: "backup/model"},
		}})
		calls := 0
		inner := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			calls++
			writer.WriteHeader(http.StatusBadRequest)
		})
		response := httptest.NewRecorder()
		ModelGroupHandler(inner, store).ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"group/heavy","messages":[]}`),
		))
		if response.Code != http.StatusBadRequest || calls != 1 {
			t.Fatalf("response = %d, calls = %d", response.Code, calls)
		}
	})
}

func TestModelGroupHandlerPreservesStreamingAndRewritesChunkModel(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	writeModelGroupConfig(t, store, config.ModelGroupConfig{Candidates: []config.ModelCandidateConfig{{Model: "primary/model"}}})
	inner := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("group writer does not implement http.Flusher")
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "data: {\"model\":\"primary/model\",\"choices\":[]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
		flusher.Flush()
	})
	response := httptest.NewRecorder()
	ModelGroupHandler(inner, store).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"group/heavy","messages":[],"stream":true}`),
	))
	if response.Code != http.StatusOK || !response.Flushed ||
		!strings.Contains(response.Body.String(), `"model":"group/heavy"`) ||
		strings.Contains(response.Body.String(), `"model":"primary/model"`) {
		t.Fatalf("stream = status %d, flushed %v, body %s", response.Code, response.Flushed, response.Body.String())
	}
}

func TestModelGroupHandlerCircuitSkipsFailingCandidate(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	writeModelGroupConfig(t, store, config.ModelGroupConfig{Candidates: []config.ModelCandidateConfig{
		{Model: "primary/model"}, {Model: "backup/model"},
	}})
	primaryCalls := 0
	inner := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["model"] == "primary/model" {
			primaryCalls++
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(writer, `{"model":"backup/model","choices":[]}`)
	})
	handler := ModelGroupHandler(inner, store)
	for range defaultCircuitFailureThreshold + 1 {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"group/heavy","messages":[]}`),
		))
		if response.Code != http.StatusOK {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	}
	if primaryCalls != defaultCircuitFailureThreshold {
		t.Fatalf("primary calls = %d", primaryCalls)
	}
}
