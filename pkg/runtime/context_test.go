package runtime

import (
	"context"
	"testing"

	"github.com/snowmerak/q/v2/pkg/app/config"
	"github.com/snowmerak/q/v2/pkg/domain"
)

func TestResolveModelsReasoningEffortHierarchy(t *testing.T) {
	cfg := config.Default()
	cfg.LLM.Roles = config.LLMRolesConfig{
		ChatModel:             "openai/chat-global",
		ChatReasoningEffort:   "low",
		MemoryModel:           "openai/mem-global",
		MemoryReasoningEffort: "minimal",
	}

	// Global defaults only
	resGlobal := ResolveModels(context.Background(), cfg, domain.Room{}, domain.Character{}, nil)
	if resGlobal.ReasoningEffort != "low" || resGlobal.MemoryReasoningEffort != "minimal" {
		t.Fatalf("expected global reasoning defaults (low, minimal), got (%q, %q)", resGlobal.ReasoningEffort, resGlobal.MemoryReasoningEffort)
	}

	// Character overrides global
	char := domain.Character{
		ModelPrefs: &domain.ModelPrefs{
			ReasoningEffort:       "medium",
			MemoryReasoningEffort: "high",
		},
	}
	resChar := ResolveModels(context.Background(), cfg, domain.Room{}, char, nil)
	if resChar.ReasoningEffort != "medium" || resChar.MemoryReasoningEffort != "high" {
		t.Fatalf("expected character reasoning overrides (medium, high), got (%q, %q)", resChar.ReasoningEffort, resChar.MemoryReasoningEffort)
	}

	// Room overrides character
	room := domain.Room{
		ModelOverrides: &domain.ModelPrefs{
			ReasoningEffort:       "xhigh",
			MemoryReasoningEffort: "medium",
		},
	}
	resRoom := ResolveModels(context.Background(), cfg, room, char, nil)
	if resRoom.ReasoningEffort != "xhigh" || resRoom.MemoryReasoningEffort != "medium" {
		t.Fatalf("expected room reasoning overrides (xhigh, medium), got (%q, %q)", resRoom.ReasoningEffort, resRoom.MemoryReasoningEffort)
	}
}
