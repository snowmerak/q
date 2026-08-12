package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type pipeServer struct {
	reader *bufio.Reader
	writer io.WriteCloser
	input  io.ReadCloser
	mu     sync.Mutex
}

func newPipeClient(t *testing.T, handler Handler) (*Client, *pipeServer) {
	t.Helper()
	clientInput, serverOutput := io.Pipe()
	serverInput, clientOutput := io.Pipe()
	client := newClient(context.Background(), clientInput, clientOutput, nil, handler, 0)
	server := &pipeServer{reader: bufio.NewReader(serverInput), writer: serverOutput, input: serverInput}
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.writer.Close()
		_ = server.input.Close()
	})
	return client, server
}

func (s *pipeServer) read(t *testing.T) wireMessage {
	t.Helper()
	body, err := readFrame(s.reader, 0)
	if err != nil {
		t.Fatal(err)
	}
	var message wireMessage
	if err := json.Unmarshal(body, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func (s *pipeServer) write(t *testing.T, message wireMessage) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeFrame(s.writer, message, 0); err != nil {
		t.Fatal(err)
	}
}

func TestClientMatchesConcurrentResponsesByID(t *testing.T) {
	client, server := newPipeClient(t, nil)
	results := make(chan string, 2)
	errorsChannel := make(chan error, 2)
	for _, method := range []string{"first", "second"} {
		method := method
		go func() {
			var result string
			err := client.Request(context.Background(), method, nil, &result)
			results <- result
			errorsChannel <- err
		}()
	}
	first := server.read(t)
	second := server.read(t)
	server.write(t, wireMessage{JSONRPC: "2.0", ID: second.ID, Result: []byte(`"` + second.Method + `"`)})
	server.write(t, wireMessage{JSONRPC: "2.0", ID: first.ID, Result: []byte(`"` + first.Method + `"`)})
	seen := map[string]bool{}
	for range 2 {
		if err := <-errorsChannel; err != nil {
			t.Fatal(err)
		}
		seen[<-results] = true
	}
	if !seen["first"] || !seen["second"] {
		t.Fatalf("results = %#v", seen)
	}
}

func TestClientReturnsResponseError(t *testing.T) {
	client, server := newPipeClient(t, nil)
	done := make(chan error, 1)
	go func() { done <- client.Request(context.Background(), "unknown", nil, nil) }()
	request := server.read(t)
	server.write(t, wireMessage{JSONRPC: "2.0", ID: request.ID, Error: &ResponseError{Code: MethodNotFoundCode, Message: "unknown"}})
	err := <-done
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) || responseErr.Code != MethodNotFoundCode {
		t.Fatalf("error = %#v", err)
	}
}

func TestClientSendsCancellationNotification(t *testing.T) {
	client, server := newPipeClient(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Request(ctx, "slow", nil, nil) }()
	request := server.read(t)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	cancellation := server.read(t)
	if cancellation.Method != "$/cancelRequest" || string(cancellation.ID) != "" || !strings.Contains(string(cancellation.Params), string(request.ID)) {
		t.Fatalf("cancellation = %#v", cancellation)
	}
}

func TestClientHandlesServerCalls(t *testing.T) {
	notifications := make(chan string, 1)
	handler := HandlerFuncs{
		RequestFunc: func(_ context.Context, method string, params json.RawMessage) (any, error) {
			if method != "workspace/configuration" || !strings.Contains(string(params), "gopls") {
				t.Fatalf("request = %s %s", method, params)
			}
			return []any{map[string]bool{"semanticTokens": true}}, nil
		},
		NotificationFunc: func(_ context.Context, method string, _ json.RawMessage) {
			notifications <- method
		},
	}
	_, server := newPipeClient(t, handler)
	server.write(t, wireMessage{JSONRPC: "2.0", Method: "window/logMessage", Params: []byte(`{"message":"ready"}`)})
	server.write(t, wireMessage{JSONRPC: "2.0", ID: []byte("\"server-1\""), Method: "workspace/configuration", Params: []byte(`{"section":"gopls"}`)})
	if method := <-notifications; method != "window/logMessage" {
		t.Fatalf("notification = %q", method)
	}
	response := server.read(t)
	if string(response.ID) != `"server-1"` || response.Error != nil || !strings.Contains(string(response.Result), "semanticTokens") {
		t.Fatalf("response = %#v", response)
	}
}

func TestInitializeAndShutdownSequence(t *testing.T) {
	client, server := newPipeClient(t, nil)
	done := make(chan error, 1)
	go func() {
		_, err := client.Initialize(context.Background(), InitializeParams{ClientInfo: &ClientInfo{Name: "q", Version: "test"}})
		done <- err
	}()
	initialize := server.read(t)
	if initialize.Method != "initialize" || !strings.Contains(string(initialize.Params), `"capabilities":{}`) {
		t.Fatalf("initialize = %#v", initialize)
	}
	server.write(t, wireMessage{JSONRPC: "2.0", ID: initialize.ID, Result: []byte(`{"capabilities":{"hoverProvider":true},"serverInfo":{"name":"fake"}}`)})
	initialized := server.read(t)
	if initialized.Method != "initialized" || len(initialized.ID) != 0 {
		t.Fatalf("initialized = %#v", initialized)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- client.Shutdown(context.Background()) }()
	shutdown := server.read(t)
	if shutdown.Method != "shutdown" {
		t.Fatalf("shutdown = %#v", shutdown)
	}
	server.write(t, wireMessage{JSONRPC: "2.0", ID: shutdown.ID, Result: []byte("null")})
	exit := server.read(t)
	if exit.Method != "exit" || len(exit.ID) != 0 {
		t.Fatalf("exit = %#v", exit)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
}

func TestStartRunsLanguageServerProcess(t *testing.T) {
	if os.Getenv("Q_LSP_TEST_SERVER") == "1" {
		runTestLanguageServer()
		os.Exit(0)
	}
	client, err := Start(context.Background(), Config{
		Command: os.Args[0], Args: []string{"-test.run=^TestStartRunsLanguageServerProcess$"},
		Environment: append(os.Environ(), "Q_LSP_TEST_SERVER=1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initialized, err := client.Initialize(ctx, InitializeParams{})
	if err != nil {
		t.Fatal(err)
	}
	if initialized.ServerInfo == nil || initialized.ServerInfo.Name != "test-language-server" {
		t.Fatalf("initialize result = %#v", initialized)
	}
	var hover map[string]string
	if err := client.Request(ctx, "textDocument/hover", map[string]any{"test": true}, &hover); err != nil {
		t.Fatal(err)
	}
	if hover["contents"] != "hover result" {
		t.Fatalf("hover = %#v", hover)
	}
	if err := client.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestGoplsIntegration(t *testing.T) {
	if os.Getenv("Q_LSP_GOPLS_INTEGRATION") != "1" {
		t.Skip("set Q_LSP_GOPLS_INTEGRATION=1 to test an installed gopls")
	}
	rootURI, err := FileURI("..")
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	client, err := Start(context.Background(), Config{Command: "gopls", Directory: "..", Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	processID := os.Getpid()
	result, err := client.Initialize(ctx, InitializeParams{
		ProcessID: &processID, ClientInfo: &ClientInfo{Name: "q", Version: "test"},
		RootURI: &rootURI, Capabilities: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Capabilities) == 0 {
		t.Fatal("gopls returned no server capabilities")
	}
	if err := client.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v; stderr: %s", err, stderr.String())
	}
}

func runTestLanguageServer() {
	reader := bufio.NewReader(os.Stdin)
	for {
		body, err := readFrame(reader, 0)
		if err != nil {
			return
		}
		var message wireMessage
		if json.Unmarshal(body, &message) != nil {
			return
		}
		switch message.Method {
		case "initialize":
			_ = writeFrame(os.Stdout, wireMessage{JSONRPC: "2.0", ID: message.ID, Result: []byte(`{"capabilities":{},"serverInfo":{"name":"test-language-server"}}`)}, 0)
		case "textDocument/hover":
			_ = writeFrame(os.Stdout, wireMessage{JSONRPC: "2.0", ID: message.ID, Result: []byte(`{"contents":"hover result"}`)}, 0)
		case "shutdown":
			_ = writeFrame(os.Stdout, wireMessage{JSONRPC: "2.0", ID: message.ID, Result: []byte("null")}, 0)
		case "exit":
			return
		}
	}
}
