package tools_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/tools"
)

func TestRuntimeAndOrchestrationSchemasKeepOptionalFieldsInResponses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	runtime, err := tools.NewRuntime(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	catalog := append(runtime.Tools(), agentloop.OrchestrationTools()...)
	if len(catalog) <= 3 {
		t.Fatal("builtin MCP tools missing from catalog")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool
			Tools  []struct {
				Name       string
				Strict     *bool
				Parameters json.RawMessage
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(body.Tools) != len(catalog) {
			t.Errorf("sent %d tools, want %d", len(body.Tools), len(catalog))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for index, sent := range body.Tools {
			original := catalog[index].Function
			if sent.Name != original.Name || sent.Strict == nil || *sent.Strict {
				t.Errorf("%s: optional input contract was promoted to strict mode", sent.Name)
			}
			expected, err := json.Marshal(original.Parameters)
			if err != nil {
				t.Error(err)
			}
			if !bytes.Equal(expected, sent.Parameters) {
				t.Errorf("%s: schema changed in transit", sent.Name)
			}
		}
		terminal := `{"id":"resp-test","model":"test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]}`
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", terminal)
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, terminal)
		}
	}))
	t.Cleanup(server.Close)
	configured, err := client.New(client.Config{BaseURL: server.URL, DisableAPIKey: true, ModelAPIModes: map[string]string{"test": "responses"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	request := client.ChatRequest{Model: "test", Messages: []client.Message{{Role: client.RoleUser, Content: "hello"}}, Tools: catalog}
	if response, err := configured.Chat(t.Context(), request); err != nil || response.Choices[0].Message.Content != "OK" {
		t.Fatalf("chat = %+v, %v", response, err)
	}
	stream, err := configured.ChatStream(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	for {
		if _, err := stream.Recv(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("validated %d builtin MCP and orchestration tools in both Responses modes", len(catalog))
}
