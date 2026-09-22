package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

type acpDiffTestClient struct {
	call  client.ToolCall
	calls int
}

func (c *acpDiffTestClient) Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error) {
	c.calls++
	message := client.Message{Role: client.RoleAssistant, Content: "Done"}
	if c.calls == 1 {
		message = client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{c.call}}
	}
	return &client.ChatResponse{Choices: []client.Choice{{Message: message}}}, nil
}

func (*acpDiffTestClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (*acpDiffTestClient) Close() error                                       { return nil }

type acpDiffTestTools struct {
	fakeAgentTools
	root    string
	content string
	fail    bool
}

func (*acpDiffTestTools) Tools() []client.Tool {
	return []client.Tool{
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "write_file"}},
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "edit_file"}},
	}
}

func (r *acpDiffTestTools) Call(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return client.ToolResult{}, err
	}
	if err := os.WriteFile(filepath.Join(r.root, input.Path), []byte(r.content), 0o644); err != nil {
		return client.ToolResult{}, err
	}
	return client.ToolResult{Content: `{"ok":true}`, IsError: r.fail}, nil
}

func TestACPAgentReportsWriteAndEditDiffs(t *testing.T) {
	for _, test := range []struct {
		name, tool, oldText, newText string
		hadFile, fail, wantDiff      bool
	}{
		{name: "new write", tool: "write_file", newText: "hello\n", wantDiff: true},
		{name: "overwrite", tool: "write_file", oldText: "before\n", newText: "after\n", hadFile: true, wantDiff: true},
		{name: "edit", tool: "edit_file", oldText: "before\n", newText: "after\n", hadFile: true, wantDiff: true},
		{name: "unchanged", tool: "write_file", oldText: "same\n", newText: "same\n", hadFile: true},
		{name: "changed despite tool error", tool: "edit_file", oldText: "before\n", newText: "after\n", hadFile: true, fail: true, wantDiff: true},
		{name: "binary", tool: "write_file", oldText: "before\n", newText: "after\x00", hadFile: true},
		{name: "binary before", tool: "write_file", oldText: "before\x00", newText: "after\n", hadFile: true},
		{name: "large", tool: "write_file", oldText: "before\n", newText: strings.Repeat("x", maxACPFileDiffBytes+1), hadFile: true},
		{name: "large before", tool: "write_file", oldText: strings.Repeat("x", maxACPFileDiffBytes+1), newText: "after\n", hadFile: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			modelClient := &acpDiffTestClient{call: client.ToolCall{
				ID: "edit-1", Type: client.ToolTypeFunction,
				Function: client.FunctionCall{Name: test.tool, Arguments: `{"path":"sample.txt"}`},
			}}
			toolRuntime := &acpDiffTestTools{content: test.newText, fail: test.fail}
			agent, store, connection := testACPAgent(t, modelClient, toolRuntime)
			toolRuntime.root = store.Root
			path := filepath.Join(store.Root, "sample.txt")
			if test.hadFile {
				if err := os.WriteFile(path, []byte(test.oldText), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			sessionID := openTestACPSession(t, agent, store.Root)
			if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
				SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("change sample.txt")},
			}); err != nil {
				t.Fatal(err)
			}
			var update *acp.SessionToolCallUpdate
			for _, notification := range connection.snapshot() {
				if next := notification.Update.ToolCallUpdate; next != nil && next.ToolCallId == "edit-1" {
					update = next
					body, err := json.Marshal(notification.Update)
					if err != nil {
						t.Fatal(err)
					}
					var decoded acp.SessionUpdate
					if err := json.Unmarshal(body, &decoded); err != nil {
						t.Fatalf("invalid ACP update %s: %v", body, err)
					}
					if test.wantDiff && (decoded.ToolCallUpdate == nil || len(decoded.ToolCallUpdate.Content) != 2 ||
						decoded.ToolCallUpdate.Content[1].Diff == nil) {
						t.Fatalf("diff lost in ACP encoding: %s", body)
					}
				}
			}
			if update == nil || len(update.Content) == 0 || update.Content[0].Content == nil ||
				update.Content[0].Content.Content.Text == nil {
				t.Fatalf("missing tool result text: %#v", update)
			}
			if test.fail {
				if got, ok := update.RawOutput.(string); !ok || !strings.HasPrefix(got, "Tool error:") {
					t.Fatalf("raw error output = %#v", update.RawOutput)
				}
			} else if got, ok := update.RawOutput.(map[string]any); !ok || got["ok"] != true {
				t.Fatalf("raw output = %#v", update.RawOutput)
			}
			want := acp.ToolCallStatusCompleted
			if test.fail {
				want = acp.ToolCallStatusFailed
			}
			if update.Status == nil || *update.Status != want {
				t.Fatalf("status = %#v, want %q", update.Status, want)
			}
			if !test.wantDiff {
				if len(update.Content) != 1 {
					t.Fatalf("unexpected diff content = %#v", update.Content)
				}
				return
			}
			if len(update.Content) != 2 || update.Content[1].Diff == nil {
				t.Fatalf("missing diff content = %#v", update.Content)
			}
			diff := update.Content[1].Diff
			if diff.Type != "diff" || diff.Path != path || diff.NewText != test.newText {
				t.Fatalf("diff = %#v", diff)
			}
			if test.hadFile {
				if diff.OldText == nil || *diff.OldText != test.oldText {
					t.Fatalf("old text = %#v", diff.OldText)
				}
			} else if diff.OldText != nil {
				t.Fatalf("new file has old text = %q", *diff.OldText)
			}
		})
	}
}

func TestACPFileDiffRejectsOutsidePath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if _, _, ok := acpFileDiffPath(root, `{"path":`+strconv.Quote(outside)+`}`); ok {
		t.Fatal("accepted path outside workspace")
	}
}
