package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/snowmerak/q/v2/pkg/llm/gateway"
)

func TestParseOptionsAcceptsShortConfigFlagAndAlias(t *testing.T) {
	short, err := parseOptions([]string{"-f", "short.json"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if short.configPath != "short.json" {
		t.Fatalf("short config path = %q", short.configPath)
	}

	alias, err := parseOptions([]string{"-config", "alias.json"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if alias.configPath != "alias.json" {
		t.Fatalf("alias config path = %q", alias.configPath)
	}
}

func TestGatewayRuntimeReloadsOnlyValidReplacement(t *testing.T) {
	oldUpstream := modelServer(t, "old-model")
	defer oldUpstream.Close()
	newUpstream := modelServer(t, "new-model")
	defer newUpstream.Close()

	runtime, err := newGatewayRuntime(t.Context(), runtimeConfig(oldUpstream.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	assertRuntimeModel(t, runtime, "local/old-model")

	replaced, err := runtime.Reload(t.Context(), runtimeConfig(newUpstream.URL))
	if err != nil || !replaced {
		t.Fatalf("reload replaced=%v err=%v", replaced, err)
	}
	assertRuntimeModel(t, runtime, "local/new-model")

	replaced, err = runtime.Reload(t.Context(), gateway.Config{})
	if err == nil || replaced {
		t.Fatalf("invalid reload replaced=%v err=%v", replaced, err)
	}
	assertRuntimeModel(t, runtime, "local/new-model")
}

func TestGatewayRuntimeWaitsForInflightRequestBeforeClose(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runtime := &gatewayRuntime{current: &gatewayGeneration{
		handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(started)
			<-release
		}),
	}}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		runtime.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	<-started

	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		_ = runtime.Close()
	}()
	select {
	case <-closeDone:
		t.Fatal("runtime closed while a request was still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("request did not finish")
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("runtime did not close after request completion")
	}
}

func TestGatewayRuntimePublishesReloadBeforeOldRequestFinishes(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	oldGeneration := &gatewayGeneration{handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	})}
	runtime := &gatewayRuntime{current: oldGeneration}
	defer runtime.Close()
	go runtime.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	<-started

	upstream := modelServer(t, "replacement-model")
	defer upstream.Close()
	reloadDone := make(chan error, 1)
	go func() {
		_, err := runtime.Reload(t.Context(), runtimeConfig(upstream.URL))
		reloadDone <- err
	}()

	deadline := time.Now().Add(time.Second)
	for {
		runtime.mu.Lock()
		published := runtime.current != oldGeneration
		runtime.mu.Unlock()
		if published {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement generation was not published")
		}
		time.Sleep(time.Millisecond)
	}
	assertRuntimeModel(t, runtime, "local/replacement-model")
	select {
	case err := <-reloadDone:
		t.Fatalf("reload finished before the old request: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reload did not retire the old generation")
	}
}

func TestConfigWatcherDetectsAtomicReplacement(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "llm-provider.json")
	if err := os.WriteFile(configPath, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher, err := newConfigWatcher(configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	reloaded := make(chan struct{}, 1)
	go func() {
		defer close(done)
		watcher.Run(ctx, func(context.Context) {
			select {
			case reloaded <- struct{}{}:
			default:
			}
		})
	}()

	replacementPath := configPath + ".tmp"
	if err := os.WriteFile(replacementPath, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacementPath, configPath); err != nil {
		t.Fatal(err)
	}

	select {
	case <-reloaded:
	case <-time.After(3 * time.Second):
		t.Fatal("configuration replacement was not detected")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("configuration watcher did not stop")
	}
}

func modelServer(t *testing.T, model string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			http.NotFound(writer, request)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"object": "list",
			"data":   []map[string]any{{"id": model}},
		})
	}))
}

func runtimeConfig(baseURL string) gateway.Config {
	return gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "local", Type: "openai-compatible", Enabled: true, BaseURL: baseURL + "/v1",
	}}}
}

func assertRuntimeModel(t *testing.T, runtime http.Handler, expected string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	response := httptest.NewRecorder()
	runtime.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("model response status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0].ID != expected {
		t.Fatalf("models = %#v, want %q", envelope.Data, expected)
	}
}
