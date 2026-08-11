package providerhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
)

func TestRunChildReportsRandomLoopbackEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"object":"list","data":[{"id":"test-model","object":"model"}]}`))
	}))
	defer upstream.Close()

	store := Store{Dir: t.TempDir()}
	path, err := store.WriteRuntimeSnapshot(gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "test", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL + "/v1",
	}}})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan ReadyMessage, 1)
	done := make(chan error, 1)
	go func() {
		done <- RunChild(ctx, path, "test-gateway-key", func(message ReadyMessage) error {
			ready <- message
			return nil
		})
	}()

	var message ReadyMessage
	select {
	case message = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not become ready")
	}
	if message.Event != "ready" || message.BaseURL == "http://127.0.0.1:0" {
		t.Fatalf("ready message = %#v", message)
	}
	request, err := http.NewRequest(http.MethodGet, message.BaseURL+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-gateway-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != "test/test-model" {
		t.Fatalf("models = %#v", payload.Data)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not stop")
	}
}
