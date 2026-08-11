package providerhost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
)

func TestSupervisorStartsAndStopsCurrentExecutableChild(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"object":"list","data":[{"id":"test-model","object":"model"}]}`))
	}))
	defer upstream.Close()

	t.Setenv("Q_GATEWAY_HELPER_PROCESS", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := Store{Dir: t.TempDir()}
	supervisor := &Supervisor{
		ctx: ctx, store: store, executable: os.Args[0],
		argsPrefix: []string{"-test.run=TestGatewayChildProcess", "--"},
	}
	prepared, err := supervisor.Prepare(ctx, gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "test", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL + "/v1",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(prepared.endpoint, "http://127.0.0.1:") || strings.HasSuffix(prepared.endpoint, ":0/v1") {
		t.Fatalf("endpoint = %q", prepared.endpoint)
	}
	if prepared.apiKey == "" {
		t.Fatal("prepared child has no API key")
	}
	supervisor.Activate(prepared)
	firstAPIKey := prepared.apiKey
	if supervisor.Endpoint() != prepared.endpoint {
		t.Fatalf("Endpoint() = %q", supervisor.Endpoint())
	}
	if supervisor.APIKey() != prepared.apiKey {
		t.Fatalf("APIKey() = %q", supervisor.APIKey())
	}
	if _, err := supervisor.Prepare(ctx, gateway.Config{}); err == nil {
		t.Fatal("invalid replacement unexpectedly started")
	}
	if supervisor.Endpoint() != prepared.endpoint {
		t.Fatalf("failed replacement changed endpoint to %q", supervisor.Endpoint())
	}
	if supervisor.APIKey() != firstAPIKey {
		t.Fatalf("failed replacement changed API key to %q", supervisor.APIKey())
	}
	replacement, err := supervisor.Prepare(ctx, gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "test", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL + "/v1",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.apiKey == firstAPIKey {
		t.Fatal("replacement reused the previous API key")
	}
	supervisor.Activate(replacement)
	if supervisor.APIKey() != replacement.apiKey {
		t.Fatalf("replacement APIKey() = %q", supervisor.APIKey())
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{prepared.configPath, replacement.configPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("runtime snapshot still exists: %s: %v", path, err)
		}
	}
}

func TestSupervisorAllowsUnavailableProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"offline"}}`))
	}))
	defer upstream.Close()

	t.Setenv("Q_GATEWAY_HELPER_PROCESS", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := &Supervisor{
		ctx: ctx, store: Store{Dir: t.TempDir()}, executable: os.Args[0],
		argsPrefix: []string{"-test.run=TestGatewayChildProcess", "--"},
	}
	prepared, err := supervisor.Prepare(ctx, gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "claude", Type: "anthropic", Enabled: true,
		BaseURL: upstream.URL + "/v1", APIKey: "unused-test-key",
	}}})
	if err != nil {
		t.Fatalf("unavailable provider prevented Gateway startup: %v", err)
	}
	defer prepared.close()
}

func TestGatewayChildProcess(t *testing.T) {
	if os.Getenv("Q_GATEWAY_HELPER_PROCESS") != "1" {
		return
	}
	index := -1
	for candidate, argument := range os.Args {
		if argument == ChildCommand {
			index = candidate
			break
		}
	}
	if index < 0 || len(os.Args) <= index+2 || os.Args[index+1] != "--config" {
		os.Exit(2)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
	apiKey := os.Getenv(ChildAPIKeyEnv)
	_ = os.Unsetenv(ChildAPIKeyEnv)
	err := RunChild(ctx, os.Args[index+2], apiKey, EncodeReady(json.NewEncoder(os.Stdout)))
	if err != nil {
		_, _ = os.Stderr.WriteString(err.Error())
		os.Exit(1)
	}
	select {
	case <-time.After(time.Millisecond):
	default:
	}
	os.Exit(0)
}
