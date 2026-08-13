package builtin

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/lsp"
)

type Dependencies struct {
	Archive      Archive
	Loom         *LoomRuntime
	Skills       *agentskills.Registry
	GlobalSkills GlobalSkillLibrary
	Propositions PropositionLibrary
	LSP          *lsp.Manager
}

// Register adds the root-jailed builtin tools to server. Optional workspace
// services are included when supplied through dependencies.
func Register(server *mcp.Server, root string, dependencies Dependencies) (*FS, error) {
	fs, err := NewFS(root)
	if err != nil {
		return nil, err
	}
	readOnly := true
	destructive := true
	nonDestructive := false

	mcp.AddTool(server, &mcp.Tool{
		Name:        "learn",
		Description: "Close the current learning segment and enqueue it for asynchronous Thinker extraction. Returns immediately; an empty segment is a no-op.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ LearnInput) (*mcp.CallToolResult, LearnOutput, error) {
		return structuredTextResult(LearnOutput{Enqueued: true})
	})

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
		Description: "Atomically write complete text file content, creating the file or replacing an existing regular file. Prefer edit_file when a targeted change is practical.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive, IdempotentHint: true},
	}, valueHandler(fs.WriteFile))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_directory",
		Description: "List the immediate children of a directory inside the workspace, excluding q-owned .q metadata and entries matched by the workspace-root .qignore file. Explicit file access remains available.",
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

	mcp.AddTool(server, &mcp.Tool{
		Name:        "run_command",
		Description: "Start a shell command in the workspace and return immediately with a command_id. Commands may modify files; use wait with the latest next_offset until it finishes.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive},
	}, valueHandler(fs.RunCommand))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "cmd_status",
		Description: "Take one immediate non-blocking snapshot of a command. Do not poll this tool in a loop; use wait to follow a running command.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
	}, valueHandler(fs.CommandStatus))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "wait",
		Description: "Wait up to 60 seconds for a run_command process and return status and output. Pass the latest next_offset; if status is still running, call wait again.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
	}, valueHandler(fs.WaitCommand))

	if dependencies.Archive != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "search_archive",
			Description: "Search durable workspace history for prior conversations, decisions, agent tasks, failures, and tool results. Returns bounded excerpts; use get_archive_record for a selected full record.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: true},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input SearchArchiveInput) (*mcp.CallToolResult, SearchArchiveOutput, error) {
			output, err := SearchArchive(ctx, dependencies.Archive, input)
			if err != nil {
				return nil, SearchArchiveOutput{}, err
			}
			return structuredTextResult(output)
		})

		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_archive_record",
			Description: "Get one durable workspace archive record by ID. Content is paginated by Unicode character offset; payload is optional and omitted when too large.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: true},
		}, func(_ context.Context, _ *mcp.CallToolRequest, input GetArchiveRecordInput) (*mcp.CallToolResult, GetArchiveRecordOutput, error) {
			output, err := GetArchiveRecord(dependencies.Archive, input)
			if err != nil {
				return nil, GetArchiveRecordOutput{}, err
			}
			return structuredTextResult(output)
		})
	}
	if dependencies.Loom != nil && dependencies.Loom.Store != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "loom_inspect",
			Description: "Inspect immutable data captured from an MCP result or produced by a Loom JavaScript transform without reading its content.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: true},
		}, contextValueHandler(dependencies.Loom.Inspect))
		mcp.AddTool(server, &mcp.Tool{
			Name:        "loom_read",
			Description: "Read a byte range from a Loom artifact. Results are UTF-8 when valid and base64 otherwise; paginate with next_offset while more is true.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: true},
		}, contextValueHandler(dependencies.Loom.Read))
		mcp.AddTool(server, &mcp.Tool{
			Name: "loom_eval",
			Description: "Run restricted JavaScript over Loom artifacts. Pass refs in inputs; scripts can call loom.inspect(ref), loom.read(ref, offset, limit), " +
				"loom.get(ref), and loom.json(ref, JSONPointer), and must return a JSON value. The result is stored as a new immutable artifact.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
		}, contextValueHandler(dependencies.Loom.Eval))
	}
	if dependencies.Skills != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "search_skills",
			Description: "Search global Agent Skills through q Library and project skills through the workspace index, then merge the results. Use get_skill with a selected result ID.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: true},
		}, contextValueHandler(func(ctx context.Context, input SearchSkillsInput) (SearchSkillsOutput, error) {
			return searchSkills(ctx, dependencies.Archive, dependencies.GlobalSkills, input)
		}))
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_skill",
			Description: "Store one selected SKILL.md or skill-relative resource as an immutable Loom artifact. Read or transform the returned artifact with Loom tools.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: true},
		}, contextValueHandler(func(ctx context.Context, input GetSkillInput) (GetSkillOutput, error) {
			return getSkill(ctx, dependencies.Skills, dependencies.GlobalSkills, dependencies.Loom, input)
		}))
	}
	if dependencies.Propositions != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "search_propositions",
			Description: "Search durable global propositions in q Library. Matching includes canonical proposition text and generated query variants, with a configurable created_at recency boost. Use get_proposition for a selected result.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: true},
		}, contextValueHandler(func(ctx context.Context, input SearchPropositionsInput) (SearchPropositionsOutput, error) {
			return searchPropositions(ctx, dependencies.Propositions, input)
		}))
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_proposition",
			Description: "Get one durable global proposition by the exact ID returned from search_propositions, including provenance and extraction metadata.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: true},
		}, contextValueHandler(func(ctx context.Context, input GetPropositionInput) (GetPropositionOutput, error) {
			return getProposition(ctx, dependencies.Propositions, input)
		}))
	}
	if dependencies.LSP != nil {
		registerLSP(server, dependencies.LSP)
	}

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
		return structuredTextResult(output)
	}
}
func contextValueHandler[In, Out any](fn func(context.Context, In) (Out, error)) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
		output, err := fn(ctx, input)
		if err != nil {
			var zero Out
			return nil, zero, err
		}
		return structuredTextResult(output)
	}
}

func structuredTextResult[Out any](output Out) (*mcp.CallToolResult, Out, error) {
	body, err := json.Marshal(output)
	if err != nil {
		var zero Out
		return nil, zero, err
	}
	return textResult(string(body)), output, nil
}
