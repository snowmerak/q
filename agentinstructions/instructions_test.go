package agentinstructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/snowmerak/q/client"
)

func TestLoaderLoadsRootAndApplicableNestedInstructions(t *testing.T) {
	root := t.TempDir()
	writeInstruction(t, filepath.Join(root, "AGENTS.md"), "root rule")
	writeInstruction(t, filepath.Join(root, "app", "AGENTS.md"), "app rule")
	writeInstruction(t, filepath.Join(root, "docs", "AGENTS.md"), "docs rule")

	loader := New(root, nil)
	rootMessages := loader.Root()
	if len(rootMessages) != 1 || Sources(rootMessages)[0] != "AGENTS.md" || !strings.Contains(rootMessages[0].Content, "root rule") {
		t.Fatalf("root messages = %#v", rootMessages)
	}
	nested := loader.ForToolCalls([]client.ToolCall{{
		Function: client.FunctionCall{Name: "edit_file", Arguments: `{"path":"app/model.go"}`},
	}})
	if len(nested) != 1 || Sources(nested)[0] != "app/AGENTS.md" || !strings.Contains(nested[0].Content, "app rule") {
		t.Fatalf("nested messages = %#v", nested)
	}
	if repeated := loader.ForPaths([]string{"app/model.go"}); len(repeated) != 0 {
		t.Fatalf("repeated messages = %#v", repeated)
	}
}

func TestLoaderRejectsEscapingAndUnstructuredPaths(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	outside := filepath.Join(parent, "outside")
	writeInstruction(t, filepath.Join(root, "AGENTS.md"), "root rule")
	writeInstruction(t, filepath.Join(outside, "AGENTS.md"), "outside secret")
	loader := New(root, nil)
	_ = loader.Root()

	messages := loader.ForToolCalls([]client.ToolCall{
		{Function: client.FunctionCall{Name: "read_file", Arguments: `{"path":"../outside/file.go"}`}},
		{Function: client.FunctionCall{Name: "run_command", Arguments: `{"command":"type ../outside/AGENTS.md"}`}},
	})
	if len(messages) != 0 {
		t.Fatalf("escaping instructions loaded: %#v", messages)
	}

	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if linked := loader.ForPaths([]string{"linked/file.go"}); len(linked) != 0 {
		t.Fatalf("outside symlink instructions loaded: %#v", linked)
	}
}

func TestLoaderBoundsLargeUTF8Instructions(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("가", MaximumFileBytes)
	writeInstruction(t, filepath.Join(root, "AGENTS.md"), body)

	messages := New(root, nil).Root()
	if len(messages) != 1 || !utf8.ValidString(messages[0].Content) {
		t.Fatalf("messages = %#v", messages)
	}
	if !strings.Contains(messages[0].Content, "truncated by q") {
		t.Fatalf("missing truncation marker: %q", messages[0].Content[len(messages[0].Content)-100:])
	}
}

func TestLoaderRejectsInvalidText(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte{'o', 'k', 0xff, 'x'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if messages := New(root, nil).Root(); len(messages) != 0 {
		t.Fatalf("invalid UTF-8 instructions loaded: %#v", messages)
	}
}

func TestPrepareKeepsRepositoryInstructionsInLeadingOrder(t *testing.T) {
	root := t.TempDir()
	writeInstruction(t, filepath.Join(root, "AGENTS.md"), "root rule")
	writeInstruction(t, filepath.Join(root, "app", "AGENTS.md"), "app rule")
	messages := []client.Message{
		{Role: client.RoleSystem, Content: "system"},
		{Role: client.RoleDeveloper, Name: "q_workspace", Content: "runtime"},
		{Role: client.RoleUser, Content: "inspect"},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{Function: client.FunctionCall{Arguments: `{"path":"app/model.go"}`}}}},
	}

	prepared := Prepare(messages, root)
	if len(prepared) != 6 {
		t.Fatalf("prepared messages = %#v", prepared)
	}
	if prepared[0].Role != client.RoleSystem || Sources(prepared[1:3])[0] != "AGENTS.md" ||
		Sources(prepared[1:3])[1] != "app/AGENTS.md" || prepared[3].Name != "q_workspace" {
		t.Fatalf("leading messages = %#v", prepared[:4])
	}
}

func writeInstruction(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
