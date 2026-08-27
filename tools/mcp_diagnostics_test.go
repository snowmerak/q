package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/mcpconfig"
)

func TestMCPDiagnosticsRedactsCredentialsAcrossWrites(t *testing.T) {
	t.Setenv("Q_MCP_DIAGNOSTIC_SOURCE", "mapped-test-secret")
	t.Setenv("Q_MCP_DIAGNOSTIC_HEADER", "Bearer header-test-secret")
	t.Setenv("Q_MCP_INHERITED_API_KEY", "inherited-test-secret")
	d := newMCPDiagnostics(mcpconfig.ServerConfig{
		Env:             map[string]string{"CHILD_TOKEN": "Q_MCP_DIAGNOSTIC_SOURCE"},
		ResolvedEnv:     map[string]string{"SESSION_TOKEN": "session-test-secret"},
		Headers:         map[string]string{"Authorization": "Q_MCP_DIAGNOSTIC_HEADER"},
		ResolvedHeaders: map[string]string{"X-Session": "resolved-header-secret"},
	})
	_, _ = d.Write([]byte("\x1b[31mfailed: mapped-test-sec"))
	_, _ = d.Write([]byte("ret session-test-secret Bearer header-test-secret inherited-test-secret resolved-header-secret\x1b[0m\n"))
	cause := fmt.Errorf("initialize: session-test-secret: %w", context.DeadlineExceeded)
	err := d.wrap(cause)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("diagnostics lost the original error chain")
	}
	for _, secret := range []string{"mapped-test-secret", "session-test-secret", "header-test-secret", "inherited-test-secret", "resolved-header-secret", "\x1b"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("diagnostics leaked a credential or terminal escape: %q", err)
		}
	}
	if !strings.Contains(err.Error(), "MCP server stderr (recent):\nfailed:") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("stderr diagnostic was not preserved: %q", err)
	}
}

func TestMCPDiagnosticsBoundsMemoryAndKeepsCompleteTailLines(t *testing.T) {
	d := newMCPDiagnostics(mcpconfig.ServerConfig{ResolvedEnv: map[string]string{"TOKEN": "long-test-secret"}})
	input := strings.Repeat("old log\n", maximumMCPStderrBytes) + "long-test-secret\n마지막 오류\n"
	if n, err := d.Write([]byte(input)); n != len(input) || err != nil {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	if len(d.body) > maximumMCPStderrBytes || cap(d.body) > maximumMCPStderrBytes*2 {
		t.Fatalf("unbounded stderr buffer: len=%d cap=%d", len(d.body), cap(d.body))
	}
	value := d.stderr()
	if !utf8.ValidString(value) || !strings.Contains(value, "stderr truncated") || !strings.HasSuffix(value, "마지막 오류") || strings.Contains(value, "long-test-secret") {
		t.Fatalf("invalid bounded stderr tail: %q", value)
	}
	if len(value) > maximumMCPStderrBytes+64 {
		t.Fatalf("stderr diagnostic too large: %d", len(value))
	}
	// The retained prefix cuts through a secret; do not expose its suffix.
	d = newMCPDiagnostics(mcpconfig.ServerConfig{ResolvedEnv: map[string]string{"TOKEN": "long-test-secret"}})
	_, _ = d.Write([]byte("long-test-secret" + strings.Repeat("x", maximumMCPStderrBytes-14) + "\n"))
	if strings.Contains(d.stderr(), "test-secret") {
		t.Fatal("truncation exposed a partial secret")
	}
}

func TestMCPDiagnosticsConcurrentWritesAndSnapshots(t *testing.T) {
	d := newMCPDiagnostics(mcpconfig.ServerConfig{})
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for range 100 {
				_, _ = d.Write([]byte("stderr line\n"))
				_ = d.wrap(io.EOF)
			}
		})
	}
	workers.Wait()
	if !strings.Contains(d.stderr(), "stderr line") || d.wrap(nil) != nil {
		t.Fatal("concurrent diagnostics lost stderr or converted success into failure")
	}
}

func diagnosticMCPServer(t *testing.T, mode string) mcpconfig.ServerConfig {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return mcpconfig.ServerConfig{
		Transport: mcpconfig.TransportStdio, Command: executable,
		Args:        []string{"-test.run=^TestMCPStderrHelperProcess$", "--", mode},
		ResolvedEnv: map[string]string{"Q_MCP_TEST_TOKEN": "diagnostic-test-secret"},
	}
}

func TestExternalMCPStartupAndListFailuresIncludeStderr(t *testing.T) {
	for _, mode := range []string{"startup", "list"} {
		t.Run(mode, func(t *testing.T) {
			_, _, err := connectExternalServer(t.Context(), t.TempDir(), "acp_session_1", diagnosticMCPServer(t, mode))
			if err == nil || !strings.Contains(err.Error(), "synthetic "+mode+" failure") || !strings.Contains(err.Error(), "MCP server stderr") {
				t.Fatalf("failure lost subprocess stderr: %v", err)
			}
			if strings.Contains(err.Error(), "diagnostic-test-secret") {
				t.Fatalf("failure leaked the child credential: %v", err)
			}
		})
	}
}

func TestExternalMCPCallFailuresIncludeStderrInBaseAndSessionScope(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		t.Run(fmt.Sprintf("scoped=%t", scoped), func(t *testing.T) {
			root := t.TempDir()
			runtime, err := NewRuntime(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			value := mcpconfig.Default()
			value.Servers["test"] = diagnosticMCPServer(t, "call")
			value.Roles[mcpconfig.RoleDefault] = []string{"test"}
			call := runtime.Call
			var statuses []ExternalStatus
			if scoped {
				scope, connected := runtime.NewExternalScope(t.Context(), root, value)
				defer scope.Close()
				call, statuses = scope.Call, connected
			} else {
				statuses = runtime.ConfigureExternal(t.Context(), root, value)
			}
			if len(statuses) != 1 || statuses[0].Error != "" {
				t.Fatalf("unexpected startup failure: %#v", statuses)
			}
			_, err = call(t.Context(), client.ToolCall{Function: client.FunctionCall{Name: "mcp_test__echo", Arguments: `{}`}})
			if err == nil || !strings.Contains(err.Error(), "synthetic call failure") || !strings.Contains(err.Error(), "call MCP tool test/echo") || strings.Contains(err.Error(), "diagnostic-test-secret") {
				t.Fatalf("call diagnostic missing or unsafe: %v", err)
			}
		})
	}
}

func TestExternalMCPSuccessfulStderrDoesNotBecomeAnError(t *testing.T) {
	server, routes, err := connectExternalServer(t.Context(), t.TempDir(), "test", diagnosticMCPServer(t, "success"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.session.Close()
	result, err := routes["mcp_test__echo"].callTool(t.Context(), map[string]any{})
	if err != nil || result.IsError {
		t.Fatalf("normal stderr turned into a failure: result=%v err=%v", result, err)
	}
}

// Invoked only as a subprocess. Keep stdout strictly MCP JSON-RPC, including
// on successful exit; the Go test runner must not append its PASS footer.
func TestMCPStderrHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	fail := func() {
		fmt.Fprintf(os.Stderr, "synthetic %s failure: %s\n", mode, os.Getenv("Q_MCP_TEST_TOKEN"))
		os.Exit(1)
	}
	if mode == "startup" {
		fail()
	}
	fmt.Fprintln(os.Stderr, "ordinary MCP startup log")
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := decoder.Decode(&request); err != nil {
			os.Exit(0)
		}
		if len(request.ID) == 0 {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			response["result"] = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "diagnostic-test", "version": "1"}}
		case "tools/list":
			if mode == "list" {
				fail()
			}
			response["result"] = map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
		case "tools/call":
			if mode == "call" {
				fail()
			}
			fmt.Fprintln(os.Stderr, "ordinary MCP call log")
			response["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "Method not found"}
		}
		if err := encoder.Encode(response); err != nil {
			os.Exit(1)
		}
	}
}
