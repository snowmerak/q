package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	qlibrary "github.com/snowmerak/q/library"
)

func TestLibraryCommandServesHealth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping q binary integration test in short mode")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	configDir := filepath.Join(home, ".q")
	value := qlibrary.Config{
		Version: qlibrary.ConfigVersion, Host: "127.0.0.1", Port: port,
	}
	if err := (qlibrary.ConfigStore{Dir: configDir}).Save(value); err != nil {
		t.Fatal(err)
	}
	binaryName := "q-test"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binary := filepath.Join(t.TempDir(), binaryName)
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build q: %v\n%s", err, output)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "library", "start")
	command.Env = environmentWithHome(os.Environ(), home)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped && command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	lineResult := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lineResult <- line
	}()

	endpoint := value.Endpoint() + "/health"
	var health qlibrary.Health
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, requestErr := http.Get(endpoint)
		if requestErr == nil {
			decodeErr := json.NewDecoder(response.Body).Decode(&health)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && health.Compatible() {
				break
			}
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			_ = command.Wait()
			stopped = true
			t.Fatalf("q library did not become ready: %s", stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if health.StoreID == "" || health.Generation == "" {
		t.Fatalf("health = %#v", health)
	}
	// Launch the actual bridge executable against this custom Library address.
	// Its stdout must contain only MCP frames, even though the Library service
	// itself reports status on a separate process's stdout.
	bridge, err := mcp.NewClient(&mcp.Implementation{Name: "q-test", Version: "0"}, nil).Connect(ctx, &mcp.CommandTransport{Command: exec.CommandContext(ctx, binary, "library", "mcp", "--config-dir", configDir)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := bridge.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 5 {
		t.Fatalf("Library MCP tools = %+v, %v", listed, err)
	}
	result, err := bridge.CallTool(ctx, &mcp.CallToolParams{Name: "search_propositions", Arguments: map[string]any{"query": ""}})
	if err != nil || result.IsError {
		t.Fatalf("Library MCP search = %+v, %v", result, err)
	}
	// This Library has no configured Librarian. Learning still acknowledges
	// the durable queue write through the actual stdio bridge executable.
	result, err = bridge.CallTool(ctx, &mcp.CallToolParams{Name: "request_learning", Arguments: map[string]any{
		"content": "The user confirmed this integration-test decision.", "refs": []string{"user:integration-test"}, "confidence": 1,
	}})
	if err != nil || result.IsError {
		t.Fatalf("Library MCP learning = %+v, %v", result, err)
	}
	ack, err := json.Marshal(result.StructuredContent)
	if err != nil || string(ack) != `{"enqueued":true}` {
		t.Fatalf("Library MCP acknowledgement = %s, %v", ack, err)
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	var output string
	select {
	case output = <-lineResult:
	case <-time.After(2 * time.Second):
		t.Fatal("q library did not report its endpoint")
	}

	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed q library unexpectedly exited successfully")
	}
	stopped = true
	if !strings.Contains(output, "q library listening on "+value.Endpoint()) {
		t.Fatalf("stdout = %q", output)
	}
}

func environmentWithHome(environment []string, home string) []string {
	result := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "HOME") || strings.EqualFold(name, "USERPROFILE") {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "HOME="+home, "USERPROFILE="+home)
}
