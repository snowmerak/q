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
	supervisor.Activate(prepared)
	if supervisor.Endpoint() != prepared.endpoint {
		t.Fatalf("Endpoint() = %q", supervisor.Endpoint())
	}
	if _, err := supervisor.Prepare(ctx, gateway.Config{}); err == nil {
		t.Fatal("invalid replacement unexpectedly started")
	}
	if supervisor.Endpoint() != prepared.endpoint {
		t.Fatalf("failed replacement changed endpoint to %q", supervisor.Endpoint())
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(prepared.configPath); !os.IsNotExist(err) {
		t.Fatalf("runtime snapshot still exists: %v", err)
	}
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
	err := RunChild(ctx, os.Args[index+2], EncodeReady(json.NewEncoder(os.Stdout)))
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
