//go:build !windows

package studio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/providerenv"
	"github.com/snowmerak/q/providerhost"
)

type environmentReloadRuntime struct {
	err      error
	resolved string
	calls    int
}

func (runtime *environmentReloadRuntime) ApplyGateway(_ context.Context, value gateway.Config) error {
	runtime.calls++
	runtime.resolved = providerenv.Getenv(value.Providers[0].APIKeyEnv)
	return runtime.err
}

func newEnvironmentReloadService(t *testing.T, runtime settingsRuntime) *settingsService {
	t.Helper()
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	value.ProviderEnvironment = config.ProviderEnvironmentConfig{
		Shell:   "/bin/sh",
		Command: `printf '{"Q_TEST_PROVIDER_KEY":"rotated"}'`,
	}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	providers := gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "openai", Type: "openai-compatible", Enabled: true,
		BaseURL: "http://127.0.0.1:1/v1", APIKeyEnv: "Q_TEST_PROVIDER_KEY",
	}}}
	if err := (providerhost.Store{Dir: store.Dir}).Save(providers); err != nil {
		t.Fatal(err)
	}
	return newSettingsService(store, runtime)
}

func reloadProviderEnvironment(service *settingsService) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/settings/provider-environment/reload", nil)
	service.serveProviderEnvironmentReload(recorder, request)
	return recorder
}

func TestProviderEnvironmentReloadRestartsGatewayWithRefreshedValues(t *testing.T) {
	previous := providerenv.Replace(nil)
	defer providerenv.Replace(previous)
	t.Setenv("Q_TEST_PROVIDER_KEY", "inherited")
	runtime := &environmentReloadRuntime{}
	service := newEnvironmentReloadService(t, runtime)
	changed := providerenv.Changed()

	recorder := reloadProviderEnvironment(service)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if strings.Contains(recorder.Body.String(), "rotated") {
		t.Fatalf("response exposes a value: %s", recorder.Body)
	}
	var result providerenv.Result
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Loaded) != 1 || result.Loaded[0] != "Q_TEST_PROVIDER_KEY" {
		t.Fatalf("result = %+v", result)
	}
	if runtime.calls != 1 || runtime.resolved != "rotated" {
		t.Fatalf("Gateway replacement saw %q after %d calls", runtime.resolved, runtime.calls)
	}
	if got := providerenv.Getenv("Q_TEST_PROVIDER_KEY"); got != "rotated" {
		t.Fatalf("lookup = %q", got)
	}
	if got := os.Getenv("Q_TEST_PROVIDER_KEY"); got != "inherited" {
		t.Fatalf("process environment changed to %q", got)
	}
	select {
	case <-changed:
	default:
		t.Fatal("standalone services were not notified")
	}
}

func TestProviderEnvironmentReloadKeepsWorkingValuesWhenGatewayFails(t *testing.T) {
	previous := providerenv.Replace(map[string]string{"Q_TEST_PROVIDER_KEY": "working"})
	defer providerenv.Replace(previous)
	runtime := &environmentReloadRuntime{err: errors.New("child exited")}
	service := newEnvironmentReloadService(t, runtime)
	changed := providerenv.Changed()

	if recorder := reloadProviderEnvironment(service); recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if got := providerenv.Getenv("Q_TEST_PROVIDER_KEY"); got != "working" {
		t.Fatalf("lookup = %q", got)
	}
	select {
	case <-changed:
		t.Fatal("consumers were notified after a failed replacement")
	default:
	}
}
