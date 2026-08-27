package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/third_party/acp-go-sdk"
	qtools "github.com/snowmerak/q/tools"
)

func TestACPMCPStderrIsReturnedInJSONRPCError(t *testing.T) {
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	runtime, err := qtools.NewRuntime(t.Context(), workspaceStore.Root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	agent.state.toolRuntime = runtime
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	requestReader, requestWriter := io.Pipe()
	responseReader, responseWriter := io.Pipe()
	closePipes := func() {
		_ = requestReader.Close()
		_ = requestWriter.Close()
		_ = responseReader.Close()
		_ = responseWriter.Close()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	stopClose := context.AfterFunc(ctx, closePipes)
	defer stopClose()
	defer closePipes()
	connection := acp.NewAgentSideConnection(agent, responseWriter, requestReader)
	agent.setConnection(connection)
	encoder, decoder := json.NewEncoder(requestWriter), json.NewDecoder(responseReader)
	request := func(id int, method string, params any) {
		t.Helper()
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
	}
	request(1, "initialize", acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	var initialized json.RawMessage
	if err := decoder.Decode(&initialized); err != nil {
		t.Fatal(err)
	}
	request(2, "session/new", acp.NewSessionRequest{
		Cwd: workspaceStore.Root,
		McpServers: []acp.McpServer{{Stdio: &acp.McpServerStdio{
			Name: "diagnostic-server", Command: executable,
			Args: []string{"-test.run=^TestACPMCPStderrHelperProcess$", "--", "acp-mcp-stderr"},
			Env:  []acp.EnvVariable{{Name: "Q_ACP_MCP_TEST_TOKEN", Value: "wire-test-secret"}},
		}}},
	})
	var response struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Error   *struct {
			Code int `json:"code"`
			Data struct {
				Error string `json:"error"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("MCP stderr corrupted the ACP JSON-RPC stream: %v", err)
	}
	if response.JSONRPC != "2.0" || response.ID != 2 || response.Error == nil || response.Error.Code != -32603 {
		t.Fatalf("unexpected ACP response: %+v", response)
	}
	detail := response.Error.Data.Error
	for _, expected := range []string{"connect ACP MCP server acp_session_1", "MCP server stderr (recent):", "synthetic MCP initialization failure", "[REDACTED]"} {
		if !strings.Contains(detail, expected) {
			t.Fatalf("ACP error missing %q: %s", expected, detail)
		}
	}
	if strings.Contains(detail, "wire-test-secret") {
		t.Fatal("ACP error exposed a session credential")
	}
	if len(agent.sessions) != 0 {
		t.Fatal("failed MCP initialization published an ACP session")
	}
}

func TestACPMCPStderrHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" || os.Args[len(os.Args)-1] != "acp-mcp-stderr" {
		return
	}
	fmt.Fprintf(os.Stderr, "synthetic MCP initialization failure: %s\n", os.Getenv("Q_ACP_MCP_TEST_TOKEN"))
	os.Exit(1)
}
