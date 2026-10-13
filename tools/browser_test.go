package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-rod/rod/lib/launcher"
	"github.com/snowmerak/q/client"
)

func TestBrowserToolsCaptureRenderedPageInLoom(t *testing.T) {
	if os.Getenv("Q_BROWSER_PATH") == "" {
		if _, found := launcher.LookPath(); !found {
			t.Skip("install Chrome/Chromium or set Q_BROWSER_PATH for browser integration")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "<title>MCP browser</title><main>Captured browser evidence</main>")
	}))
	defer server.Close()
	runtime, err := NewRuntime(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, name := range []string{"browser_open", "browser_read", "browser_snapshot", "browser_click", "browser_fill", "browser_close"} {
		if !runtimeHasTool(runtime, name) {
			t.Fatalf("missing browser tool %q", name)
		}
	}
	arguments, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := runtime.Call(t.Context(), client.ToolCall{
		ID: "browser-call", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "browser_open", Arguments: string(arguments)},
	})
	if err != nil || result.IsError {
		t.Fatalf("browser tool result = %#v, err = %v", result, err)
	}
	var receipt loomReceipt
	if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.LoomRef == "" || !receipt.Stored || receipt.Result == nil {
		t.Fatalf("browser evidence was not captured: %#v", receipt)
	}
	artifact, err := runtime.loom.Store.Inspect(t.Context(), receipt.LoomRef)
	if err != nil || artifact.Source["tool"] != "browser_open" {
		t.Fatalf("artifact = %#v, err = %v", artifact, err)
	}
}
