package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

func TestManagerAutomaticallyDiscoversMissingRoots(t *testing.T) {
	for _, query := range []string{"hover", "definition", "references", "symbols", "diagnostics", "workspace"} {
		t.Run(query, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var scans atomic.Int32
			manager, err := NewManager(t.Context(), root, GlobalConfig{
				Servers:   map[string]ServerConfig{"test": {Languages: []string{"go"}, Command: os.Args[0], Args: []string{managerHelperArgument}}},
				Languages: map[string]string{"go": "test"},
			}, WorkspaceConfig{}, WithRootDiscovery(func(context.Context) ([]RootConfig, error) {
				scans.Add(1)
				return []RootConfig{{Path: ".", Language: "go"}}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			if status := manager.Status(t.Context()); len(status.Sessions) != 0 || scans.Load() != 0 {
				t.Fatalf("status triggered discovery: %+v", status)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			for range 2 {
				position := PositionRequest{Path: "main.go", Line: 3, Column: 6}
				switch query {
				case "hover":
					_, err = manager.Hover(ctx, position)
				case "definition":
					_, err = manager.Definition(ctx, position)
				case "references":
					_, err = manager.References(ctx, ReferencesRequest{Path: "main.go", Line: 3, Column: 6})
				case "symbols":
					_, err = manager.DocumentSymbols(ctx, FileRequest{Path: "main.go"})
				case "diagnostics":
					_, err = manager.Diagnostics(ctx, DiagnosticsRequest{Path: "main.go"})
				case "workspace":
					_, err = manager.sessionsForWorkspace(ctx, "", "go")
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			status := manager.Status(ctx)
			if scans.Load() != 1 || len(status.Sessions) != 1 || status.Sessions[0].State != "running" || status.Sessions[0].Source != RootSourceDiscovered {
				t.Fatalf("scans=%d status=%+v", scans.Load(), status)
			}
		})
	}
}

func TestManagerDiscoveryPreservesDisabledAndManualSettings(t *testing.T) {
	global := GlobalConfig{
		Servers: map[string]ServerConfig{
			"custom-go":     {Languages: []string{"go"}, Command: "custom-gopls", Disabled: true},
			"custom-python": {Languages: []string{"python"}, Command: "custom-python", Disabled: true},
			"rust-analyzer": {Languages: []string{"rust"}, Command: "custom-rust"},
		},
		Languages: map[string]string{"go": "custom-go", "rust": "rust-analyzer"},
	}
	workspace := WorkspaceConfig{Version: WorkspaceConfigVersion, Roots: []RootConfig{
		{Path: "disabled", Language: "go", Disabled: true, Source: RootSourceManual},
		{Path: "manual", Language: "rust", Server: "rust-analyzer", Source: RootSourceManual},
	}}
	roots := []RootConfig{
		{Path: "disabled", Language: "go"}, {Path: "disabled/nested", Language: "go"},
		{Path: "manual", Language: "rust"}, {Path: ".", Language: "go"},
	}
	servers := []DiscoveredServer{
		{ID: "gopls", Config: ServerConfig{Languages: []string{"go"}, Command: "gopls"}},
		{ID: "pyright", Config: ServerConfig{Languages: []string{"python"}, Command: "pyright-langserver"}},
		{ID: "rust-analyzer", Config: ServerConfig{Languages: []string{"rust"}, Command: "rust-analyzer"}},
		{ID: "typescript", Config: ServerConfig{Languages: []string{"typescript", "javascript"}, Command: "typescript-language-server"}},
	}
	mergedGlobal, mergedWorkspace, err := mergeDiscoveredConfiguration(global, workspace, roots, servers)
	if err != nil {
		t.Fatal(err)
	}
	for id, server := range global.Servers {
		if !reflect.DeepEqual(mergedGlobal.Servers[id], server) {
			t.Fatalf("changed configured server %s", id)
		}
	}
	if len(mergedGlobal.Servers) != 4 || mergedGlobal.Languages["go"] != "custom-go" || mergedGlobal.Languages["python"] != "" || mergedGlobal.Languages["typescript"] != "typescript" {
		t.Fatalf("merged global = %+v", mergedGlobal)
	}
	if len(mergedWorkspace.Roots) != 3 || !reflect.DeepEqual(mergedWorkspace.Roots[:2], workspace.Roots) {
		t.Fatalf("merged roots = %+v", mergedWorkspace.Roots)
	}
	manager := &Manager{workspace: mergedWorkspace}
	if _, err := manager.route("disabled/main.go", ""); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled root fell back to discovered ancestor: %v", err)
	}
	againGlobal, againWorkspace, err := mergeDiscoveredConfiguration(mergedGlobal, mergedWorkspace, roots, servers)
	if err != nil || !reflect.DeepEqual(mergedGlobal, againGlobal) || !reflect.DeepEqual(mergedWorkspace, againWorkspace) {
		t.Fatalf("repeated discovery changed settings: global=%+v workspace=%+v err=%v", againGlobal, againWorkspace, err)
	}
}

func TestManagerDiscoveryRunsOnceForConcurrentFailures(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var scans atomic.Int32
	manager, err := NewManager(t.Context(), t.TempDir(), GlobalConfig{}, WorkspaceConfig{}, WithRootDiscovery(func(ctx context.Context) ([]RootConfig, error) {
		scans.Add(1)
		close(started)
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			_, err := manager.sessionsForWorkspace(ctx, "", "go")
			if err == nil || !strings.Contains(err.Error(), "no enabled, resolved project roots") {
				t.Errorf("unexpected result: %v", err)
			}
			manager.Status(ctx)
		})
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitCtx, cancelWait := context.WithCancel(ctx)
	cancelWait()
	if err := manager.autoDiscover(waitCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled discovery waiter: %v", err)
	}
	close(release)
	workers.Wait()
	if err := manager.autoDiscover(ctx); err != nil || scans.Load() != 1 {
		t.Fatalf("repeated scan: scans=%d err=%v", scans.Load(), err)
	}
}

func TestManagerCancelledDiscoveryCanRetry(t *testing.T) {
	var scans int
	manager, err := NewManager(t.Context(), t.TempDir(), GlobalConfig{}, WorkspaceConfig{}, WithRootDiscovery(func(ctx context.Context) ([]RootConfig, error) {
		scans++
		return nil, ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := manager.autoDiscover(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled discovery = %v", err)
	}
	if err := manager.autoDiscover(t.Context()); err != nil || scans != 2 {
		t.Fatalf("retry scans=%d err=%v", scans, err)
	}
}

func TestManagerDoesNotDiscoverForDisabledRootsOrProcessFailures(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled=%v", disabled), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644); err != nil {
				t.Fatal(err)
			}
			scans := 0
			manager, err := NewManager(t.Context(), root, GlobalConfig{
				Servers:   map[string]ServerConfig{"go": {Languages: []string{"go"}, Command: filepath.Join(root, "missing-server")}},
				Languages: map[string]string{"go": "go"},
			}, WorkspaceConfig{Roots: []RootConfig{{Path: ".", Language: "go", Disabled: disabled}}}, WithRootDiscovery(func(context.Context) ([]RootConfig, error) {
				scans++
				return nil, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			if _, err := manager.DocumentSymbols(t.Context(), FileRequest{Path: "main.go"}); err == nil {
				t.Fatal("expected disabled root or failed server start")
			}
			if scans != 0 {
				t.Fatalf("unexpected discovery: %d", scans)
			}
		})
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
