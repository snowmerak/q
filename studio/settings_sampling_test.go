package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSettingsModelSamplingSaveRejectAndClear(t *testing.T) {
	store := transferTestStore(t)
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/model-sampling", strings.NewReader(body)))
		return response
	}
	response := put(`{"model":"local/model","sampling":{"temperature":0,"top_p":1,"top_k":0,"min_p":0,"repetition_penalty":1,"presence_penalty":0,"frequency_penalty":0}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("save = %d %s", response.Code, response.Body.String())
	}
	value, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	sampling := value.ModelSampling["local/model"]
	if sampling.Temperature == nil || *sampling.Temperature != 0 || sampling.TopK == nil || *sampling.TopK != 0 || sampling.FrequencyPenalty == nil {
		t.Fatalf("saved = %#v", sampling)
	}
	before, _ := os.ReadFile(store.Path())
	for _, body := range []string{
		`{"model":"local/model","sampling":{"top_p":2}}`,
		`{"model":"local/model","sampling":{"top_k":0.5}}`,
		`{"model":"group/main","sampling":{"temperature":0}}`,
		`{"model":"","sampling":{"temperature":0}}`,
		`{"model":"local/model","sampling":{"unsupported":1}}`,
	} {
		response = put(body)
		if response.Code < 400 {
			t.Fatalf("accepted %s: %s", body, response.Body.String())
		}
		after, _ := os.ReadFile(store.Path())
		if string(before) != string(after) {
			t.Fatal("rejected update changed settings")
		}
	}
	// Sampling participates in the existing portable settings workflow.
	exported := httptest.NewRecorder()
	handler.ServeHTTP(exported, httptest.NewRequest(http.MethodGet, "/api/v1/settings/transfer", nil))
	var bundle settingsBundle
	if exported.Code != http.StatusOK || json.Unmarshal(exported.Body.Bytes(), &bundle) != nil || bundle.Sections["models"]["sampling/local/model"] == nil {
		t.Fatalf("export = %d %s", exported.Code, exported.Body.String())
	}
	response = put(`{"model":"local/model","sampling":{}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("clear = %d %s", response.Code, response.Body.String())
	}
	value, _ = store.Load()
	if _, exists := value.ModelSampling["local/model"]; exists {
		t.Fatal("clear retained sampling overrides")
	}
	response = transferRequest(t, handler, "/api/v1/settings/transfer/import", transferTestBundle(t, map[string]map[string]any{"models": {"sampling/local/model": sampling}}))
	if response.Code != http.StatusOK {
		t.Fatalf("import = %d %s", response.Code, response.Body.String())
	}
	value, _ = store.Load()
	if value.ModelSampling["local/model"].Temperature == nil || *value.ModelSampling["local/model"].Temperature != 0 {
		t.Fatal("import lost explicit zero")
	}
}
