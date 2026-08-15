package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/providerhost"
)

func TestParseGatewayOptions(t *testing.T) {
	defaults, err := parseGatewayOptions(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.hostSet || defaults.portSet || defaults.host != "" || defaults.port != -1 {
		t.Fatalf("defaults = %#v", defaults)
	}

	configured, err := parseGatewayOptions([]string{"--host", "0.0.0.0", "--port", "0"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if configured.host != "0.0.0.0" || configured.port != 0 || !configured.hostSet || !configured.portSet {
		t.Fatalf("configured = %#v", configured)
	}
}

func TestListenGatewayFallsBackOnlyForConfiguredPort(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	configured := gatewayconfig.ServerConfig{Host: "127.0.0.1", Port: port}

	listener, fallback, err := listenGateway(gatewayCommandOptions{}, configured)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if !fallback || listener.Addr().(*net.TCPAddr).Port == port {
		t.Fatalf("fallback=%v address=%s", fallback, listener.Addr())
	}

	_, _, err = listenGateway(gatewayCommandOptions{port: port, portSet: true}, configured)
	if err == nil {
		t.Fatal("explicit occupied port unexpectedly fell back")
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

	directory := t.TempDir()
	providerStore := providerhost.Store{Dir: directory}
	if err := providerStore.Save(gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "local", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL + "/v1",
	}}}); err != nil {
		t.Fatal(err)
	}
	settingsStore := gatewayconfig.Store{Dir: directory}
	var master [32]byte
	master[0] = 42
	if _, err := settingsStore.EnsureMasterKey(master); err != nil {
		t.Fatal(err)
	}
	generated, err := gatewayconfig.GenerateAPIKey(master, "test client", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	settings, err := gatewayconfig.AddAPIKey(gatewayconfig.Default(), generated)
	if err != nil {
		t.Fatal(err)
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stdoutReader, stdoutWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- runGatewayWithStore(ctx, nil, stdoutWriter, io.Discard, providerStore, settingsStore)
		_ = stdoutWriter.Close()
	}()

	outputReader := bufio.NewReader(stdoutReader)
	line, err := outputReader.ReadString('\n')
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	endpoint := strings.TrimSpace(strings.TrimPrefix(line, "q gateway listening on "))
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
	request.Header.Set("Authorization", "Bearer "+generated.Secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
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
	_ = response.Body.Close()

	if _, err := settingsStore.RevokeAPIKey(settings, generated.Record.ID, time.Now()); err != nil {
		cancel()
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err = http.Get(endpoint + "/models")
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("Gateway did not disable authentication after the last key was revoked; status = %d", response.StatusCode)
		}
		time.Sleep(50 * time.Millisecond)
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

func TestRunGatewayWithStoreAllowsRequestsWithoutConfiguredAPIKeys(t *testing.T) {
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

	directory := t.TempDir()
	providerStore := providerhost.Store{Dir: directory}
	if err := providerStore.Save(gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "local", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL + "/v1",
	}}}); err != nil {
		t.Fatal(err)
	}
	settingsStore := gatewayconfig.Store{Dir: directory}
	ctx, cancel := context.WithCancel(context.Background())
	stdoutReader, stdoutWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- runGatewayWithStore(ctx, nil, stdoutWriter, io.Discard, providerStore, settingsStore)
		_ = stdoutWriter.Close()
	}()

	line, err := bufio.NewReader(stdoutReader).ReadString('\n')
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	endpoint := strings.TrimSpace(strings.TrimPrefix(line, "q gateway listening on "))
	response, err := http.Get(endpoint + "/models")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("unauthenticated status = %d", response.StatusCode)
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
