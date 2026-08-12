package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const managerHelperArgument = "-test.run=^TestManagerLanguageServer$"

func TestManagerQueriesAndDiagnostics(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(context.Background(), root, GlobalConfig{
		Servers: map[string]ServerConfig{
			"test": {Languages: []string{"go"}, Command: os.Args[0], Args: []string{managerHelperArgument}},
		},
		Languages: map[string]string{"go": "test"},
	}, WorkspaceConfig{Version: WorkspaceConfigVersion, Roots: []RootConfig{{Path: ".", Language: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})

	if got := manager.Status(context.Background()).Sessions[0].State; got != "idle" {
		t.Fatalf("initial state = %q", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hover, err := manager.Hover(ctx, PositionRequest{Path: "main.go", Line: 3, Column: 6})
	if err != nil {
		t.Fatal(err)
	}
	if hover == nil || hover.Contents != "test hover" {
		t.Fatalf("hover = %#v", hover)
	}
	if got := manager.Status(context.Background()).Sessions[0].State; got != "running" {
		t.Fatalf("started state = %q", got)
	}

	definitions, err := manager.Definition(ctx, PositionRequest{Path: "main.go", Line: 3, Column: 6})
	if err != nil || len(definitions) != 1 || definitions[0].Path != "main.go" || definitions[0].Range.Start.Line != 3 {
		t.Fatalf("definitions = %#v, err = %v", definitions, err)
	}
	references, err := manager.References(ctx, ReferencesRequest{Path: "main.go", Line: 3, Column: 6, IncludeDeclaration: true})
	if err != nil || len(references) != 1 {
		t.Fatalf("references = %#v, err = %v", references, err)
	}
	symbols, err := manager.DocumentSymbols(ctx, FileRequest{Path: "main.go"})
	if err != nil || len(symbols) != 1 || symbols[0].Name != "main" || symbols[0].KindName != "function" {
		t.Fatalf("document symbols = %#v, err = %v", symbols, err)
	}
	workspaceSymbols, err := manager.WorkspaceSymbols(ctx, WorkspaceSymbolsRequest{Query: "main"})
	if err != nil || len(workspaceSymbols) != 1 || workspaceSymbols[0].Location.Path != "main.go" {
		t.Fatalf("workspace symbols = %#v, err = %v", workspaceSymbols, err)
	}
	diagnostics, err := manager.Diagnostics(ctx, DiagnosticsRequest{Path: "main.go"})
	if err != nil || diagnostics.Status != DiagnosticStatusIssues || !diagnostics.Published || len(diagnostics.Diagnostics) != 1 || diagnostics.Diagnostics[0].SeverityName != "warning" {
		t.Fatalf("diagnostics = %#v, err = %v", diagnostics, err)
	}
}

func TestDiagnosticStatusDistinguishesCleanAndUnavailable(t *testing.T) {
	manager := &Manager{root: t.TempDir()}
	clean := manager.diagnosticsResult("file:///external/test.go", diagnosticSnapshot{}, true)
	if clean.Status != DiagnosticStatusClean || !clean.Published || len(clean.Diagnostics) != 0 {
		t.Fatalf("clean diagnostics = %#v", clean)
	}
	unavailable := manager.diagnosticsResult("file:///external/test.go", diagnosticSnapshot{}, false)
	if unavailable.Status != DiagnosticStatusUnavailable || unavailable.Published {
		t.Fatalf("unavailable diagnostics = %#v", unavailable)
	}
}

func TestManagerRejectsApplyEdit(t *testing.T) {
	session := &managedSession{}
	_, err := session.serverRequest(context.Background(), "workspace/applyEdit", nil)
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) || responseErr.Code != InvalidRequestCode {
		t.Fatalf("error = %#v", err)
	}
}

func TestManagerRoutesToDeepestRoot(t *testing.T) {
	manager := &Manager{workspace: WorkspaceConfig{Roots: []RootConfig{
		{Path: ".", Language: "typescript"},
		{Path: "frontend", Language: "typescript"},
	}}}
	root, err := manager.route("frontend/src/app.ts", "")
	if err != nil || root.Path != "frontend" {
		t.Fatalf("root = %#v, err = %v", root, err)
	}
}

// This test body becomes the subprocess language server only when invoked
// with the exact argument used by TestManagerQueriesAndDiagnostics.
func TestManagerLanguageServer(t *testing.T) {
	if !slices.Contains(os.Args, managerHelperArgument) {
		return
	}
	runManagerLanguageServer()
	os.Exit(0)
}

func runManagerLanguageServer() {
	reader := bufio.NewReader(os.Stdin)
	var documentURI DocumentURI
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
			writeManagerResult(message.ID, map[string]any{"capabilities": map[string]any{
				"hoverProvider": true, "definitionProvider": true, "referencesProvider": true,
				"documentSymbolProvider": true, "workspaceSymbolProvider": true,
			}})
		case "textDocument/didOpen":
			var params struct {
				TextDocument struct {
					URI DocumentURI `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(message.Params, &params)
			documentURI = params.TextDocument.URI
			_ = writeFrame(os.Stdout, wireMessage{JSONRPC: "2.0", Method: "textDocument/publishDiagnostics", Params: mustJSON(map[string]any{
				"uri": documentURI, "version": 1, "diagnostics": []any{map[string]any{
					"range": testRange(), "severity": 2, "source": "test", "message": "test warning",
				}},
			})}, 0)
		case "textDocument/hover":
			writeManagerResult(message.ID, map[string]any{"contents": "test hover", "range": testRange()})
		case "textDocument/definition", "textDocument/references":
			writeManagerResult(message.ID, []any{map[string]any{"uri": documentURI, "range": testRange()}})
		case "textDocument/documentSymbol":
			writeManagerResult(message.ID, []any{map[string]any{"name": "main", "kind": 12, "range": testRange(), "selectionRange": testRange()}})
		case "workspace/symbol":
			writeManagerResult(message.ID, []any{map[string]any{"name": "main", "kind": 12, "location": map[string]any{"uri": documentURI, "range": testRange()}}})
		case "shutdown":
			writeManagerResult(message.ID, nil)
		case "exit":
			return
		}
	}
}

func writeManagerResult(id json.RawMessage, result any) {
	_ = writeFrame(os.Stdout, wireMessage{JSONRPC: "2.0", ID: id, Result: mustJSON(result)}, 0)
}

func mustJSON(value any) json.RawMessage {
	body, _ := json.Marshal(value)
	return body
}

func testRange() map[string]any {
	return map[string]any{"start": map[string]any{"line": 2, "character": 5}, "end": map[string]any{"line": 2, "character": 9}}
}
