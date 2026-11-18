package librarymcp

import (
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/internal/bundledskills"
	"github.com/snowmerak/q/library"
)

func TestSkillsMCPFindsAndReadsBundledSkill(t *testing.T) {
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
	t.Cleanup(func() { _ = runtime.Close() })
	session := connectServer(t, NewSkillsServer(runtime.Client()))

	catalog, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Tools) != 2 {
		t.Fatalf("skill server exposed unrelated tools: %v", catalog.Tools)
	}
	for _, tool := range catalog.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("%s is not marked read-only", tool.Name)
		}
	}

	search, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "search_skills", Arguments: map[string]any{"query": "q-cdp"}})
	if err != nil || search.IsError {
		t.Fatalf("skill search failed: %v, %v", search, err)
	}
	body, err := json.Marshal(search.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var result library.SkillSearchResponse
	if err := json.Unmarshal(body, &result); err != nil || len(result.Hits) != 1 || result.Hits[0].Title != "q-cdp" {
		t.Fatalf("bundled skill discovery: %s, %v", body, err)
	}
	id := result.Hits[0].ID
	for _, path := range []string{"", "references/javascript.md"} {
		args := map[string]any{"id": id}
		wantPath := path
		if path == "" {
			wantPath = "SKILL.md"
		} else {
			args["path"] = path
		}
		read, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_skill", Arguments: args})
		if err != nil || read.IsError {
			t.Fatalf("read %s: %v, %v", wantPath, read, err)
		}
		body, err := json.Marshal(read.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var got skillOutput
		if err := json.Unmarshal(body, &got); err != nil || got.Path != wantPath || got.Content != bundledskills.CDP().Files[wantPath] {
			t.Fatalf("read %s did not return complete text (%d bytes): %v", wantPath, len(got.Content), err)
		}
	}
	read, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_skill", Arguments: map[string]any{"id": id, "path": "../other/SKILL.md"}})
	if err == nil && !read.IsError {
		t.Fatal("skill resource lookup escaped its directory")
	}
	if !strings.Contains(session.InitializeResult().Instructions, "search_skills") || !strings.Contains(session.InitializeResult().Instructions, "get_skill") {
		t.Fatal("skill discovery instructions are missing")
	}
}
