package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/systemoneconfig"
	"github.com/snowmerak/q/systemoneserver"
)

func TestParseSystemOneOptions(t *testing.T) {
	options, err := parseSystemOneOptions([]string{"--host", "127.0.0.1", "--port", "0"}, io.Discard)
	if err != nil || !options.hostSet || !options.portSet || options.port != 0 {
		t.Fatalf("options = %#v, %v", options, err)
	}
	for _, args := range [][]string{{"--host", "localhost"}, {"--port", "-1"}, {"other"}} {
		if _, err := parseSystemOneOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted options %q", args)
		}
	}
	var help strings.Builder
	if _, err := parseSystemOneOptions([]string{"--help"}, &help); !errors.Is(err, flag.ErrHelp) ||
		!strings.Contains(help.String(), "q systemone start") {
		t.Fatalf("help = %q, %v", help.String(), err)
	}
}

func TestRunSystemOneWithStoreServesAndStops(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("SYSTEM_ONE_API_KEY", "unrelated-key")
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, present := request.Header["Authorization"]; present {
			t.Errorf("unexpected upstream authorization = %q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(writer, `{"models":[{"name":"jev"}]}`)
		case "/v1/systemone":
			_, _ = io.WriteString(writer, `{"model":"jev","answers":{"q":{"type":"noul","noul":0.8}}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer upstream.Close()
	store := systemoneconfig.Store{Dir: t.TempDir()}
	value := systemoneconfig.Default()
	value.Providers[0].URI = upstream.URL + "/v1/systemone"
	value.DefaultModel = "typesafe/jev"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() {
		done <- runSystemOneWithStore(ctx, []string{"--port", "0"}, writer, io.Discard, store)
		writer.Close()
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	baseURL := strings.TrimSpace(strings.TrimPrefix(line, "q systemone listening on "))
	request, err := http.NewRequest(http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(catalog.Models) != 1 || catalog.Models[0].Name != "typesafe/jev" {
		t.Fatalf("models = %#v", catalog.Models)
	}
	response, err = http.Post(baseURL+"/systemone", "application/json", strings.NewReader(`{"model":"typesafe/jev","state":"x","questions":{"q":{"type":"noul"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("decision status = %d", response.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("System One server did not stop after cancellation")
	}
}

func TestRunSystemOneWithManagedKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, `{"models":[{"name":"jev"}]}`)
	}))
	defer upstream.Close()
	store := systemoneconfig.Store{Dir: t.TempDir()}
	value := systemoneconfig.Default()
	value.Providers[0].URI = upstream.URL + "/v1/systemone"
	var generated systemoneconfig.GeneratedAPIKey
	var err error
	value, generated, err = store.CreateAPIKey(value, "desktop", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() {
		done <- runSystemOneWithStore(ctx, []string{"--port", "0"}, writer, io.Discard, store)
		writer.Close()
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	baseURL := strings.TrimSpace(strings.TrimPrefix(line, "q systemone listening on "))
	requestModels := func(key string) int {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, baseURL+"/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		if key != "" {
			request.Header.Set("Authorization", "Bearer "+key)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if got := requestModels(""); got != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d", got)
	}
	if got := requestModels(generated.Secret); got != http.StatusOK {
		t.Fatalf("managed key status = %d", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("System One server did not stop")
	}
}

func TestSystemOneKeyringReloadsWhileServing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, `{"models":[{"name":"jev"}]}`)
	}))
	defer upstream.Close()
	store := systemoneconfig.Store{Dir: t.TempDir()}
	value := systemoneconfig.Default()
	value.Providers[0].URI = upstream.URL + "/v1/systemone"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	instance, err := systemoneserver.New(value)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(instance.Handler())
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := watchSystemOneKeyring(ctx, store, instance, io.Discard)
	defer func() { cancel(); <-done }()
	status := func() int {
		response, err := http.Get(server.URL + "/v1/models")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if got := status(); got != http.StatusOK {
		t.Fatalf("no-key status = %d", got)
	}
	value, generated, err := store.CreateAPIKey(value, "desktop", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	waitStatus := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if status() == want {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("authentication did not change to status %d", want)
	}
	waitStatus(http.StatusUnauthorized)
	value, err = store.RevokeAPIKey(value, generated.Record.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(http.StatusOK)
}
