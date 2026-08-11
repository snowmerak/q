package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/providerhost"
)

func TestParseGatewayOptions(t *testing.T) {
	defaults, err := parseGatewayOptions(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.host != defaultGatewayHost || defaults.port != defaultGatewayPort {
		t.Fatalf("defaults = %#v", defaults)
	}

	configured, err := parseGatewayOptions([]string{"--host", "0.0.0.0", "--port", "0"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if configured.host != "0.0.0.0" || configured.port != 0 {
		t.Fatalf("configured = %#v", configured)
	}
}

func TestParseGatewayOptionsRejectsInvalidValues(t *testing.T) {
	tests := [][]string{
		{"--host", "localhost"},
		{"--port", "-1"},
		{"--port", "65536"},
		{"unexpected"},
	}
	for _, args := range tests {
		if _, err := parseGatewayOptions(args, io.Discard); err == nil {
			t.Fatalf("parseGatewayOptions(%q) unexpectedly succeeded", args)
		}
	}
}

func TestRunGatewayWithStoreServesConfiguredProviders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			http.NotFound(writer, request)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"object": "list",
			"data":   []map[string]any{{"id": "test-model", "object": "model"}},
		})
	}))
	defer upstream.Close()

	store := providerhost.Store{Dir: t.TempDir()}
	if err := store.Save(gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "local", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL + "/v1",
	}}}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stdoutReader, stdoutWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- runGatewayWithStore(ctx, nil, stdoutWriter, io.Discard, store)
		_ = stdoutWriter.Close()
	}()

	outputReader := bufio.NewReader(stdoutReader)
	line, err := outputReader.ReadString('\n')
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	endpoint := strings.TrimSpace(strings.TrimPrefix(line, "q gateway listening on "))
	apiKeyLine, err := outputReader.ReadString('\n')
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	apiKey := strings.TrimSpace(strings.TrimPrefix(apiKeyLine, "q gateway API key: "))
	unauthorized, err := http.Get(endpoint + "/models")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		cancel()
		t.Fatalf("unauthorized status = %d", unauthorized.StatusCode)
	}
	request, err := http.NewRequest(http.MethodGet, endpoint+"/models", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		cancel()
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != "local/test-model" {
		cancel()
		t.Fatalf("models = %#v", payload.Data)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gateway did not stop after cancellation")
	}
}
