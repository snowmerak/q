package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/providerhost"
)

type notifyWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func newNotifyWriter() *notifyWriter { return &notifyWriter{} }

func (writer *notifyWriter) Write(body []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buffer.Write(body)
}

func (writer *notifyWriter) String() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buffer.String()
}

func TestStudioServiceStatusAndShutdownSmoke(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ctx, cancel := context.WithCancel(t.Context())
	output := newNotifyWriter()
	diagnostics := newNotifyWriter()
	opened := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runStudioAt(ctx, nil, output, diagnostics, func(url string) error {
			opened <- url
			return nil
		}, "/")
	}()

	var url string
	select {
	case url = <-opened:
	case err := <-done:
		t.Fatalf("q studio stopped before readiness: %v; diagnostics: %s", err, diagnostics.String())
	case <-time.After(10 * time.Second):
		t.Fatalf("q studio did not report readiness; output: %s; diagnostics: %s", output.String(), diagnostics.String())
	}
	if !strings.Contains(output.String(), "q studio listening on "+url) {
		t.Fatalf("readiness output = %q", output.String())
	}

	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(url + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || strings.Contains(string(body), "workspace") {
		t.Fatalf("status = %d %q; read: %v; close: %v", response.StatusCode, body, readErr, closeErr)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v; diagnostics: %s", err, diagnostics.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("q studio did not shut down")
	}
}

func TestStudioStartsStandaloneGatewayAfterProviderSetup(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			http.NotFound(writer, request)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"object": "list", "data": []map[string]any{{"id": "test-model"}},
		})
	}))
	defer upstream.Close()

	directory := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	output := newNotifyWriter()
	diagnostics := newNotifyWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runStudioGateway(ctx, directory, output, diagnostics)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("standalone Gateway did not stop with Studio")
		}
	}()

	if err := (providerhost.Store{Dir: directory}).Save(gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "test", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL + "/v1",
	}}}); err != nil {
		t.Fatal(err)
	}

	var endpoint string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(output.String(), "\n") {
			if address, found := strings.CutPrefix(line, "q gateway listening on "); found {
				endpoint = address
				break
			}
		}
		if endpoint != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if endpoint == "" {
		t.Fatalf("standalone Gateway did not start: %s", diagnostics.String())
	}
	response, err := http.Get(endpoint + "/models")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Gateway models status = %d", response.StatusCode)
	}
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	if len(models.Data) != 1 || models.Data[0].ID != "test/test-model" {
		t.Fatalf("Gateway models = %#v", models.Data)
	}
	if err := (providerhost.Store{Dir: directory}).Save(gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "updated", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL + "/v1",
	}}}); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline.Add(10 * time.Second)) {
		if strings.Count(output.String(), "q gateway listening on ") >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if strings.Count(output.String(), "q gateway listening on ") < 2 {
		t.Fatalf("Gateway did not reload saved providers: %s; %s", output.String(), diagnostics.String())
	}
	endpoint = strings.TrimPrefix(lines[len(lines)-1], "q gateway listening on ")
	reloaded, err := http.Get(endpoint + "/models")
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Body.Close()
	if err := json.NewDecoder(reloaded.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	if len(models.Data) != 1 || models.Data[0].ID != "updated/test-model" {
		t.Fatalf("reloaded Gateway models = %#v", models.Data)
	}
}

func TestParseStudioOptions(t *testing.T) {
	options, err := parseStudioOptions([]string{"--host", "0.0.0.0", "--port", "7070", "--no-open"}, io.Discard)
	if err != nil || options.host != "0.0.0.0" || options.port != 7070 || !options.noOpen {
		t.Fatalf("options = %#v, %v", options, err)
	}
	defaults, err := parseStudioOptions(nil, io.Discard)
	if err != nil || defaults.host != "127.0.0.1" || defaults.port != 0 || defaults.noOpen {
		t.Fatalf("defaults = %#v, %v", defaults, err)
	}
	if _, err := parseStudioOptions([]string{"--host", "localhost"}, io.Discard); err == nil {
		t.Fatal("non-IP host was accepted")
	}
	if _, err := parseStudioOptions([]string{"--port", "65536"}, io.Discard); err == nil {
		t.Fatal("invalid port was accepted")
	}
}

func TestStudioClientHostUsesLoopbackForWildcardListeners(t *testing.T) {
	tests := map[string]string{
		"0.0.0.0":  "127.0.0.1",
		"::":       "::1",
		"10.0.0.8": "10.0.0.8",
	}
	for host, want := range tests {
		if got := studioClientHost(host); got != want {
			t.Errorf("studioClientHost(%q) = %q, want %q", host, got, want)
		}
	}
}
