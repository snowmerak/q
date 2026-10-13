package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/loom"
)

func TestExportPreservesLargeArtifactBeforeWorkspaceCleanup(t *testing.T) {
	root := t.TempDir()
	store, err := loom.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"payload":"` + strings.Repeat("x", 100000) + `"}`)
	artifact, err := store.Put(t.Context(), body, loom.PutOptions{Kind: "mcp-result", MediaType: "application/json"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := json.Marshal(map[string]string{"loom_ref": artifact.Ref.String(), "preview": "short preview"})
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "receipt.json")
	if err := exportArtifact(t.Context(), root, output, string(receipt)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output + ".artifact.json")
	if err != nil || string(got) != string(body) {
		t.Fatalf("artifact was truncated: bytes=%d err=%v", len(got), err)
	}
}
