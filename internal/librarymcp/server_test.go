package librarymcp

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/internal/workitemtools"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
)

func connect(t *testing.T, client *library.Client, binding workitemtools.Binding) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server, err := NewServer(client, binding).Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestWorkspaceSourceToolsAndRetiredLearning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	runtime, err := library.EnsureWithOptions(t.Context(), library.EnsureOptions{Dir: t.TempDir(), Config: library.Config{Host: "127.0.0.1", Port: port}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	client := runtime.Client()
	w, err := client.CreateWorkspace(t.Context(), workitems.WorkspaceInput{Name: "Project", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	session := connect(t, client, workitemtools.Binding{WorkspaceID: w.ID, SessionID: "actor"})
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"learn", "request_learning", "search_propositions", "get_proposition"} {
		if names[name] {
			t.Fatalf("retired tool still advertised: %s", name)
		}
	}
	for _, name := range []string{"issue_create", "issue_reply", "post_get", "search_work", "search_skills", "get_skill"} {
		if !names[name] {
			t.Fatalf("missing tool: %s", name)
		}
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "issue_create", Arguments: map[string]any{"title": "Decision", "content": "Retain source evidence", "refs": []string{"test://source"}, "request_id": "once"}})
	if err != nil || result.IsError {
		t.Fatalf("create: %+v %v", result, err)
	}
	body, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var item workitems.Item
	if err := json.Unmarshal(body, &item); err != nil || item.WorkspaceID != w.ID || item.ID == "" {
		t.Fatalf("source acknowledgement: %s %v", body, err)
	}
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "issue_reply", Arguments: map[string]any{"id": item.ID, "content": "A verified lesson", "request_id": "reply-once"}})
	if err != nil || result.IsError {
		t.Fatalf("reply: %+v %v", result, err)
	}
	thread, err := client.ReadThread(t.Context(), item.ID, "", 20)
	if err != nil || len(thread.Posts) != 2 || thread.Posts[1].Author != "agent/actor" {
		t.Fatalf("thread source: %+v %v", thread, err)
	}
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "ticket_create", Arguments: map[string]any{"title": "Inspect", "content": "Find evidence", "request_id": "auto-ticket"}})
	if err != nil || result.IsError {
		t.Fatalf("ticket issue: %+v %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var ticket workitems.Item
	if err := json.Unmarshal(encoded, &ticket); err != nil || ticket.ExecutionState != "queued" || ticket.State != "todo" {
		t.Fatalf("MCP ticket was not automatically admitted: %s %v", encoded, err)
	}
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "request_learning", Arguments: map[string]any{"content": "old"}})
	if err == nil && !result.IsError {
		t.Fatal("retired learning tool succeeded")
	}
}

func TestLibraryUnavailableDoesNotPreventMCPInitialization(t *testing.T) {
	session := connect(t, library.NewClient("http://127.0.0.1:1/v1", "", 100*time.Millisecond), workitemtools.Binding{WorkspaceID: "project"})
	if _, err := session.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "search_work", Arguments: map[string]any{"query": "unavailable"}})
	if err == nil && !result.IsError {
		t.Fatal("unavailable Library reported success")
	}
}

func TestReservedRoleToolCatalogs(t *testing.T) {
	for _, role := range []string{"coordinator", "integrator"} {
		t.Run(role, func(t *testing.T) {
			session := connect(t, library.NewClient("http://127.0.0.1:1/v1", "", time.Second), workitemtools.Binding{WorkspaceID: "project", SessionID: role, Manager: role == "integrator"})
			catalog, err := session.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for _, tool := range catalog.Tools {
				names[tool.Name] = true
			}
			for _, name := range []string{"issue_create", "ticket_create", "ticket_get", "ticket_worker_read"} {
				if !names[name] {
					t.Fatalf("%s lacks %s", role, name)
				}
			}
			for _, name := range []string{"manager_inbox", "ticket_review"} {
				if names[name] != (role == "integrator") {
					t.Fatalf("%s has incorrect review access: %s=%t", role, name, names[name])
				}
			}
		})
	}
}
