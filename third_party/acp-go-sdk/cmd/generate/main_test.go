package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedBindings_MatchAndHaveNoCodeAfterReturn(t *testing.T) {
	root := findRepoRoot()
	out := t.TempDir()
	generate(filepath.Join(root, "schema"), out)
	for _, name := range generatedFiles {
		expected, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bytes.ReplaceAll(expected, []byte("\r\n"), []byte("\n")), actual) {
			t.Errorf("%s is stale; regenerate the bindings", name)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, actual, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			block, ok := node.(*ast.BlockStmt)
			if !ok {
				return true
			}
			for i, statement := range block.List {
				if _, ok := statement.(*ast.ReturnStmt); ok && i != len(block.List)-1 {
					t.Errorf("unreachable generated code after %s", fset.Position(statement.Pos()))
				}
			}
			return true
		})
	}
}
