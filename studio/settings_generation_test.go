package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmprovider "github.com/snowmerak/llm-provider"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/providerhost"
)

func TestSettingsOutputLimitAndRecoveryRoundTrip(t *testing.T) {
	store := transferTestStore(t)
	providers := providerhost.Store{Dir: store.Dir}
	p := gateway.Config{Providers: []gateway.ProviderConfig{{ID: "local", Type: "openai-compatible", Enabled: true, BaseURL: "http://127.0.0.1:1/v1", Models: []string{"model"}}}}
	if err := providers.Save(p); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	put := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(http.MethodPut, path, strings.NewReader(body)))
		return r
	}
	r := put("/api/v1/settings/model-output-limit", `{"model":"local/model","output_limit":8192,"output_token_field":"max_tokens"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("save=%d %s", r.Code, r.Body.String())
	}
	r = put("/api/v1/settings/generation-recovery", `{"max_attempts":0}`)
	if r.Code != http.StatusOK {
		t.Fatalf("recovery=%d %s", r.Code, r.Body.String())
	}
	value, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if value.ModelOutputLimits["local/model"] != 8192 || value.ModelOutputTokenFields["local/model"] != "max_tokens" || value.GenerationRecovery.Attempts() != 0 {
		t.Fatalf("saved=%+v", value)
	}
	for _, body := range []string{`{"model":"group/main","output_limit":5}`, `{"model":"local/model","output_limit":0}`, `{"model":"local/model","output_limit":1.5}`, `{"model":"local/model","output_limit":8,"output_token_field":"wrong"}`} {
		if r := put("/api/v1/settings/model-output-limit", body); r.Code < 400 {
			t.Fatalf("accepted %s", body)
		}
	}
	if r := put("/api/v1/settings/generation-recovery", `{"max_attempts":-1}`); r.Code < 400 {
		t.Fatal("accepted negative retries")
	}
	exported := httptest.NewRecorder()
	handler.ServeHTTP(exported, httptest.NewRequest(http.MethodGet, "/api/v1/settings/transfer", nil))
	var bundle settingsBundle
	if json.Unmarshal(exported.Body.Bytes(), &bundle) != nil || bundle.Sections["models"]["output-limit/local/model"] == nil || bundle.Sections["models"]["generation-recovery"] == nil {
		t.Fatalf("export=%s", exported.Body.String())
	}
	if r := put("/api/v1/settings/model-output-limit", `{"model":"local/model","output_limit":null}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	value, _ = store.Load()
	if _, exists := value.ModelOutputLimits["local/model"]; exists {
		t.Fatal("clear retained limit")
	}
	r = transferRequest(t, handler, "/api/v1/settings/transfer/import", bundle)
	if r.Code != 200 {
		t.Fatalf("import=%d %s", r.Code, r.Body.String())
	}
	value, _ = store.Load()
	if value.ModelOutputLimits["local/model"] != 8192 || value.GenerationRecovery.Attempts() != 0 {
		t.Fatal("import lost behavior")
	}
	p.Providers[0].ModelMetadata = map[string]llmprovider.ModelMetadata{"model": {MaxOutputTokens: 8192}}
	if err := providers.Save(p); err != nil {
		t.Fatal(err)
	}
	if r := put("/api/v1/settings/model-output-limit", `{"model":"local/model","output_limit":16384}`); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("accepted known excessive limit: %d %s", r.Code, r.Body.String())
	}
}
