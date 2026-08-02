package memory

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestThresholdUsesSeventyEightPercent(t *testing.T) {
	m := New(Policy{ContextWindow: 1000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}, nil)
	m.Append(client.Message{Role: client.RoleUser, Content: strings.Repeat("x", 2200)})
	predicted := m.PredictedTokens()
	m.Configure(Policy{ContextWindow: int(float64(predicted)/.85) + 10, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07})
	if m.ShouldCompact() {
		t.Fatalf("predicted tokens = %d, compacted below 85%%", predicted)
	}
	m.Configure(Policy{ContextWindow: int(float64(predicted) / .85), TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07})
	if !m.ShouldCompact() {
		t.Fatalf("predicted tokens = %d, want compaction", m.PredictedTokens())
	}
}

func TestPlanAndApplyKeepSystemAndRecent(t *testing.T) {
	policy := Policy{ContextWindow: 4000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
	system := client.Message{Role: client.RoleSystem, Content: "keep system"}
	m := New(policy, []client.Message{system})
	for index := 0; index < 8; index++ {
		m.Append(client.Message{Role: client.RoleUser, Content: strings.Repeat("old context ", 80)})
		m.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("answer ", 80)})
	}
	latest := client.Message{Role: client.RoleUser, Content: "latest request"}
	m.Append(latest)

	plan, err := m.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Source) == 0 || len(plan.Recent) == 0 {
		t.Fatalf("plan = %#v", plan)
	}
	if err := m.Apply(plan, "goals and decisions"); err != nil {
		t.Fatal(err)
	}
	messages := m.Messages()
	if messages[0].Content != system.Content || messages[len(messages)-1].Content != latest.Content {
		t.Fatalf("compacted messages = %#v", messages)
	}
	if got := m.PredictedTokens(); got > plan.TargetTokens {
		t.Fatalf("compacted tokens = %d, target = %d", got, plan.TargetTokens)
	}
}

func TestUsageCalibratesProviderOverhead(t *testing.T) {
	m := New(Policy{ContextWindow: 100000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}, []client.Message{{Role: client.RoleUser, Content: "hi"}})
	estimate := m.LocalEstimate()
	m.ObserveUsage(16000, estimate)
	stats := m.Stats()
	if stats.ProviderOverhead != 16000-estimate {
		t.Fatalf("overhead = %d", stats.ProviderOverhead)
	}
}

func TestToolCallAndResultsFormOneRetentionUnit(t *testing.T) {
	messages := []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "call-1"}}},
		{Role: client.RoleTool, ToolCallID: "call-1", Content: "first"},
		{Role: client.RoleTool, ToolCallID: "call-2", Content: "second"},
	}
	if got := previousUnitStart(messages, len(messages)); got != 0 {
		t.Fatalf("tool retention unit starts at %d", got)
	}
}
