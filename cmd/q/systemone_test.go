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
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer provider-key" {
			t.Errorf("upstream authorization = %q", request.Header.Get("Authorization"))
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
	value.Providers[0] = systemoneconfig.ProviderConfig{
		ID: "provider", URI: upstream.URL + "/v1/systemone", APIKey: "provider-key", Model: "jev",
	}
	value.Selected = "provider"
	value.Server.APIKey = "client-key"
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
	request.Header.Set("Authorization", "Bearer client-key")
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
	if len(catalog.Models) != 1 || catalog.Models[0].Name != "provider/jev" {
		t.Fatalf("models = %#v", catalog.Models)
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
