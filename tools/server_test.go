package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerListsAndCallsBuiltinTools(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(root)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	}()

	listed, err := clientSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 16 {
		t.Fatalf("listed %d tools; want 16", len(listed.Tools))
	}
	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "read_file", Arguments: map[string]any{"path": "hello.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.StructuredContent == nil || len(result.Content) != 1 {
		t.Fatalf("unexpected tool result: %+v", result)
	}
}
