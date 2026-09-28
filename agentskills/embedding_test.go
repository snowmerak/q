package agentskills

import "testing"

func TestEmbeddingPartsKeepTagsSeparate(t *testing.T) {
	parts, err := EmbeddingParts("debugging", "Investigate software defects", []string{"go", "api"})
	if err != nil {
		t.Fatal(err)
	}
	want := []EmbeddingPart{
		{ID: "name", Text: "debugging"},
		{ID: "description", Text: "Investigate software defects"},
		{ID: "tag-0", Text: "go"},
		{ID: "tag-1", Text: "api"},
	}
	if len(parts) != len(want) {
		t.Fatalf("parts = %#v", parts)
	}
	for index := range want {
		if parts[index] != want[index] {
			t.Fatalf("part %d = %#v, want %#v", index, parts[index], want[index])
		}
	}
}
