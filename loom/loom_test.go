package loom

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestStorePersistsImmutableArtifactAndReadsRanges(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Put(context.Background(), []byte(`{"items":[1,2,3]}`), PutOptions{
		Kind: "test", MediaType: "application/json", Source: map[string]string{"tool": "fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Ref == "" || artifact.Bytes != 17 || artifact.Digest == "" {
		t.Fatalf("artifact = %#v", artifact)
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := reopened.Inspect(context.Background(), artifact.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Digest != artifact.Digest || metadata.Source["tool"] != "fixture" {
		t.Fatalf("metadata = %#v", metadata)
	}
	first, err := reopened.Read(context.Background(), artifact.Ref, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Content) != `{"items"` || first.NextOffset != 8 || !first.More {
		t.Fatalf("first range = %#v", first)
	}
	remaining, err := reopened.Read(context.Background(), artifact.Ref, first.NextOffset, 64)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Content)+string(remaining.Content) != `{"items":[1,2,3]}` || remaining.More {
		t.Fatalf("remaining range = %#v", remaining)
	}
}

func TestStoreRejectsUnknownArtifact(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Inspect(context.Background(), Ref("loom://00000000000000000000000000000000"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("inspect error = %v", err)
	}
}

func TestEvaluatorReadsJSONAndStoresDerivedArtifact(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Put(context.Background(), []byte(`{"items":[{"kind":"a","value":2},{"kind":"b","value":5},{"kind":"a","value":3}]}`), PutOptions{
		Kind: "mcp-result", MediaType: "application/json",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (InProcessEvaluator{}).Evaluate(context.Background(), store, EvalRequest{
		Inputs: map[string]Ref{"source": source.Ref},
		Code: `
const rows = loom.json(inputs.source, "/items");
const totals = {};
for (const row of rows) totals[row.kind] = (totals[row.kind] || 0) + row.value;
return {totals, processType: typeof process, requireType: typeof require};`,
	})
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Totals      map[string]int `json:"totals"`
		ProcessType string         `json:"processType"`
		RequireType string         `json:"requireType"`
	}
	if err := json.Unmarshal(result.Value, &value); err != nil {
		t.Fatal(err)
	}
	if value.Totals["a"] != 5 || value.Totals["b"] != 5 || value.ProcessType != "undefined" || value.RequireType != "undefined" {
		t.Fatalf("script value = %#v", value)
	}
	if len(result.Artifact.Parents) != 1 || result.Artifact.Parents[0] != source.Ref || result.Artifact.Source["language"] != "javascript" {
		t.Fatalf("derived artifact = %#v", result.Artifact)
	}
	stored, err := store.ReadAll(context.Background(), result.Artifact.Ref, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(result.Value) {
		t.Fatalf("stored result = %s, returned = %s", stored, result.Value)
	}
}
func TestEvaluatorRejectsMultipleJSONValues(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Put(context.Background(), []byte(`{} {}`), PutOptions{Kind: "test", MediaType: "application/json"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (InProcessEvaluator{}).Evaluate(context.Background(), store, EvalRequest{
		Inputs: map[string]Ref{"source": source.Ref},
		Code:   `return loom.get(inputs.source);`,
	})
	if err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
}

func TestEvaluatorInterruptsRunawayScript(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err = (InProcessEvaluator{}).Evaluate(ctx, store, EvalRequest{Code: `for (;;) {}`})
	if err == nil {
		t.Fatal("runaway script was not interrupted")
	}
}
