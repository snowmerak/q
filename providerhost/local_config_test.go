package providerhost

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
)

func TestCodexLocalConfigCreatesEphemeralThreadsWithoutChangingSavedOptions(t *testing.T) {
	for _, providerType := range []string{"codex", "codex-app-server"} {
		for _, overrides := range []map[string]any{nil, {"ephemeral": false, "serviceName": "custom"}} {
			configured := gateway.Config{Providers: []gateway.ProviderConfig{{ID: "local", Type: providerType, Codex: gateway.CodexConfig{Ephemeral: new(false), ThreadStart: overrides}}}}
			value := LocalConfig(configured, t.TempDir())
			codex := value.Providers[0].Codex
			if codex.Ephemeral == nil || !*codex.Ephemeral || overrides != nil && codex.ThreadStart["ephemeral"] != true {
				t.Fatalf("non-ephemeral Codex thread: %+v", codex)
			}
			if *configured.Providers[0].Codex.Ephemeral || overrides != nil && overrides["ephemeral"] != false {
				t.Fatal("runtime policy changed saved Codex options")
			}
			if overrides != nil && codex.ThreadStart["serviceName"] != "custom" {
				t.Fatal("unrelated thread options were lost")
			}
			if second := LocalConfig(value, t.TempDir()); !reflect.DeepEqual(value, second) {
				t.Fatal("ephemeral thread policy is not idempotent")
			}
		}
	}
}

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
	defer func() {
		if err := instance.Close(); err != nil {
			t.Errorf("close Gateway: %v", err)
		}
	}()

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
