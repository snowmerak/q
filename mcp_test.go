package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// TestHelperProcess is a mock MCP server running inside the test subprocess.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		var req JsonRpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		var resp JsonRpcResponse
		resp.JsonRPC = "2.0"
		resp.ID = &req.ID

		switch req.Method {
		case "initialize":
			resp.Result = json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"mock-server","version":"1.0.0"}}`)
		case "tools/list":
			resp.Result = json.RawMessage(`{"tools":[{"name":"mock_tool","description":"A mock test tool","inputSchema":{"type":"object","properties":{"val":{"type":"string"}},"required":["val"]}}]}`)
		case "tools/call":
			// Extract parameters
			var params struct {
				Name      string `json:"name"`
				Arguments struct {
					Val string `json:"val"`
				} `json:"arguments"`
			}
			data, _ := json.Marshal(req.Params)
			_ = json.Unmarshal(data, &params)

			resultStr := fmt.Sprintf(`{"content":[{"type":"text","text":"echo:%s"}],"isError":false}`, params.Arguments.Val)
			resp.Result = json.RawMessage(resultStr)
		}

		respData, _ := json.Marshal(resp)
		fmt.Println(string(respData))
	}
}

func TestMCPClient(t *testing.T) {
	// Setup the command to run the helper process
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--")
	cmd.Env = append(os.Environ(), "WANT_HELPER_PROCESS=1")

	// We create a custom MCP client but manually override its Start so it runs the helper process cmd
	config := MCPServerConfig{
		Command: "npx", // ignored
		Args:    []string{},
	}
	client := NewMCPClient("mock", config)

	// Inject the mock subprocess
	client.cmd = cmd
	var err error
	client.stdin, err = cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe failed: %v", err)
	}
	client.stdout, err = cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe failed: %v", err)
	}
	client.stderr, err = cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe failed: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("Cmd start failed: %v", err)
	}
	client.IsRunning = true

	client.waitGroup.Add(1)
	go client.readLoop()
	client.waitGroup.Add(1)
	go client.logLoop()

	// Handshake
	if err := client.handshake(); err != nil {
		_ = client.Stop()
		t.Fatalf("Handshake failed: %v", err)
	}

	// Test ListTools
	tools, err := client.ListTools()
	if err != nil {
		_ = client.Stop()
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "mock_tool" {
		t.Errorf("Expected 1 tool named 'mock_tool', got: %+v", tools)
	}

	// Test CallTool
	args := map[string]string{"val": "hello"}
	result, err := client.CallTool("mock_tool", args)
	if err != nil {
		_ = client.Stop()
		t.Fatalf("CallTool failed: %v", err)
	}
	if result != "echo:hello" {
		t.Errorf("Expected 'echo:hello', got: '%s'", result)
	}

	_ = client.Stop()
}
