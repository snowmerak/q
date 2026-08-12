package builtin

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/lsp"
)

type lspStatusInput struct{}

func registerLSP(server *mcp.Server, manager *lsp.Manager) {
	readOnly := true
	idempotent := true
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: idempotent}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "lsp_status",
		Description: "Show configured LSP project roots, selected servers, and lazy session state without starting idle servers.",
		Annotations: annotations,
	}, contextValueHandler(func(ctx context.Context, _ lspStatusInput) (lsp.StatusResult, error) {
		return manager.Status(ctx), nil
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "lsp_diagnostics",
		Description: "Read diagnostics published by the language server for a workspace file. This synchronizes an in-memory document and never writes the file.",
		Annotations: annotations,
	}, contextValueHandler(manager.Diagnostics))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "lsp_hover",
		Description: "Read language-server hover information at a 1-based UTF-16 line and column in a workspace file.",
		Annotations: annotations,
	}, contextValueHandler(manager.Hover))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "lsp_definition",
		Description: "Find definitions at a 1-based UTF-16 line and column in a workspace file.",
		Annotations: annotations,
	}, contextValueHandler(manager.Definition))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "lsp_references",
		Description: "Find references at a 1-based UTF-16 line and column in a workspace file.",
		Annotations: annotations,
	}, contextValueHandler(manager.References))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "lsp_document_symbols",
		Description: "List and flatten language-server symbols for one workspace file, preserving parent container names.",
		Annotations: annotations,
	}, contextValueHandler(manager.DocumentSymbols))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "lsp_workspace_symbols",
		Description: "Search symbols across configured project roots. A path targets its deepest matching root; without a path, enabled roots are queried concurrently and results are deduplicated.",
		Annotations: annotations,
	}, contextValueHandler(manager.WorkspaceSymbols))
}
