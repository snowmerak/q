package builtin

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Register adds the root-jailed builtin filesystem tools to server.
func Register(server *mcp.Server, root string) (*FS, error) {
	fs, err := NewFS(root)
	if err != nil {
		return nil, err
	}
	readOnly := true
	destructive := true
	nonDestructive := false

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_file",
		Description: "Read a text file with LINE#HASH anchors. Use these exact anchors for edit_file; re-read after a stale-anchor error.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, input ReadFileInput) (*mcp.CallToolResult, ReadFileOutput, error) {
		output, err := fs.ReadFile(input)
		if err != nil {
			return nil, ReadFileOutput{}, err
		}
		return textResult(output.Content), output, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "edit_file",
		Description: "Atomically apply non-overlapping hash-anchored replace, append, or prepend edits. All anchors refer to one read_file snapshot.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive},
	}, func(_ context.Context, _ *mcp.CallToolRequest, input EditFileInput) (*mcp.CallToolResult, EditFileOutput, error) {
		output, err := fs.EditFile(input)
		if err != nil {
			return nil, EditFileOutput{}, err
		}
		message := output.Diff
		if output.Anchors != "" {
			message += "\n--- Fresh anchors ---\n" + output.Anchors
		}
		return textResult(message), output, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "write_file",
		Description: "Create a text file atomically, or replace it only when overwrite is explicitly true. Prefer edit_file for existing text files.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive, IdempotentHint: true},
	}, valueHandler(fs.WriteFile))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_directory",
		Description: "List the immediate children of a directory inside the workspace.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: true},
	}, valueHandler(fs.ListDirectory))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_directory",
		Description: "Create a directory inside the workspace; set parents to create missing ancestors.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &nonDestructive, IdempotentHint: true},
	}, valueHandler(fs.CreateDirectory))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "move_path",
		Description: "Move or rename a file, directory, or symlink inside the workspace without overwriting the destination.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive},
	}, valueHandler(fs.MovePath))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "copy_path",
		Description: "Copy a regular file or directory tree inside the workspace without overwriting the destination. Symlinks and special files are rejected.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &nonDestructive},
	}, valueHandler(fs.CopyPath))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "remove_path",
		Description: "Remove a file or empty directory inside the workspace; recursive must be true for a non-empty directory.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive, IdempotentHint: true},
	}, valueHandler(fs.RemovePath))

	return fs, nil
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// valueHandler supplies concise JSON text alongside the SDK-generated
// structured output for simple tools.
func valueHandler[In, Out any](fn func(In) (Out, error)) mcp.ToolHandlerFor[In, Out] {
	return func(_ context.Context, _ *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
		output, err := fn(input)
		if err != nil {
			var zero Out
			return nil, zero, err
		}
		body, err := json.Marshal(output)
		if err != nil {
			var zero Out
			return nil, zero, err
		}
		return textResult(string(body)), output, nil
	}
}
