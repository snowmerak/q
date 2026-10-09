package providerhost

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
)

func TestOpenRouterIdentitySentUpstream(t *testing.T) {
	requests := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- request.Header.Clone()
		_, _ = io.WriteString(writer, `{"object":"list","data":[{"id":"test-model"}]}`)
	}))
	defer upstream.Close()

	configured := gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "router", Type: "openrouter", Enabled: true,
		BaseURL: upstream.URL + "/v1", APIKey: "test-key",
	}}}
	instance, err := gateway.NewContext(context.Background(), LocalConfig(configured, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()

	select {
	case headers := <-requests:
		if got := headers.Get("HTTP-Referer"); got != "https://q.saturday.ne.kr" {
			t.Errorf("HTTP-Referer = %q", got)
		}
		if got := headers.Get("X-OpenRouter-Title"); got != "Q" {
			t.Errorf("X-OpenRouter-Title = %q", got)
		}
	default:
		t.Fatal("Gateway made no upstream request")
	}
	if configured.Providers[0].Headers != nil {
		t.Fatal("runtime headers mutated saved configuration")
	}
}

func TestOpenRouterIdentityPreservesConfiguredHeaders(t *testing.T) {
	headers := map[string]string{
		"http-referer":       "https://example.com",
		"x-openrouter-title": "My Q",
		"X-Other":            "kept",
	}
	configured := gateway.Config{Providers: []gateway.ProviderConfig{
		{ID: "router", Type: "openrouter", Headers: headers},
		{ID: "generic", Type: "openai-compatible"},
	}}
	value := LocalConfig(configured, t.TempDir())
	if len(value.Providers[0].Headers) != len(headers) || value.Providers[0].Headers["http-referer"] != "https://example.com" ||
		value.Providers[0].Headers["x-openrouter-title"] != "My Q" || value.Providers[0].Headers["X-Other"] != "kept" {
		t.Fatalf("configured headers changed: %#v", value.Providers[0].Headers)
	}
	if value.Providers[1].Headers != nil {
		t.Fatalf("non-OpenRouter headers = %#v", value.Providers[1].Headers)
	}
	value.Providers[0].Headers["X-Other"] = "runtime-only"
	if headers["X-Other"] != "kept" {
		t.Fatal("runtime headers mutated saved configuration")
	}
}
