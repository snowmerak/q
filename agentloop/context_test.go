package agentloop

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
)

func TestLoopContextUsesConfiguredTrigger(t *testing.T) {
	history := []client.Message{{Role: client.RoleUser, Content: strings.Repeat("context ", 1_000)}}
	tools := OrchestrationTools()
	probe := newLoopContext(memory.Policy{}, history, tools)
	predicted := probe.manager.PredictedTokens()
	contextWindowAtEightyTwoPercent := (predicted*100 + 81) / 82
	loopContext := newLoopContext(memory.Policy{
		ContextWindow: contextWindowAtEightyTwoPercent,
		TriggerRatio:  .85,
		TargetRatio:   .22,
		RecentRatio:   .07,
	}, history, tools)
	if loopContext.shouldCompact() {
		t.Fatalf("loop compacted at about 82%% with configured 85%% trigger: predicted = %d, window = %d", predicted, contextWindowAtEightyTwoPercent)
	}
}
