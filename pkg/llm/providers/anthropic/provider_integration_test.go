package anthropic

import (
	"os"
	"testing"
)

func TestIntegrationListModels(t *testing.T) {
	if os.Getenv("ANTHROPIC_MODEL_LIST_INTEGRATION") == "" {
		t.Skip("set ANTHROPIC_MODEL_LIST_INTEGRATION=1 to query the real Claude Models API")
	}
	models, err := New().ListModels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		if model.ID != "claude-sonnet-5" {
			continue
		}
		if model.Created <= 0 || model.ContextLength <= 0 || model.MaxOutputTokens <= 0 {
			t.Fatalf("incomplete Claude model metadata: %#v", model)
		}
		t.Logf("Claude model metadata: created=%d context_length=%d max_output_tokens=%d",
			model.Created, model.ContextLength, model.MaxOutputTokens)
		return
	}
	t.Fatal("Claude Models API did not return claude-sonnet-5")
}
