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

func TestPlanWithRetentionKeepsPrefixAndCompactsLaterSystemMessages(t *testing.T) {
	policy := Policy{ContextWindow: 4000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
	anchors := []client.Message{
		{Role: client.RoleSystem, Content: "exact role contract"},
		{Role: client.RoleUser, Content: "exact approved plan and feedback"},
	}
	m := New(policy, anchors)
	reminder := client.Message{Role: client.RoleSystem, Content: strings.Repeat("transient reminder ", 120)}
	m.Append(reminder)
	for index := 0; index < 6; index++ {
		m.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("working history ", 100)})
	}
	m.Append(client.Message{Role: client.RoleUser, Content: "latest loop state"})

	plan, err := m.PlanWithRetention(Retention{ImmutablePrefix: len(anchors), AllowTargetGrowth: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Immutable) != len(anchors) {
		t.Fatalf("immutable = %#v", plan.Immutable)
	}
	if plan.Immutable[0].Content != anchors[0].Content || plan.Immutable[1].Content != anchors[1].Content {
		t.Fatalf("anchors changed: %#v", plan.Immutable)
	}
	for _, message := range plan.Immutable {
		if message.Content == reminder.Content {
			t.Fatal("transient system reminder became immutable")
		}
	}
	if err := m.Apply(plan, "completed work and remaining state"); err != nil {
		t.Fatal(err)
	}
	got := m.Messages()
	if got[0].Content != anchors[0].Content || got[1].Content != anchors[1].Content {
		t.Fatalf("compacted anchors changed: %#v", got[:2])
	}
}

func TestAnchoredPlanCanGrowTargetWithoutChangingLargeTask(t *testing.T) {
	policy := Policy{ContextWindow: 16_000, TriggerRatio: .80, TargetRatio: .22, RecentRatio: .07}
	anchor := client.Message{Role: client.RoleSystem, Content: strings.Repeat("approved plan ", 1200)}
	m := New(policy, []client.Message{anchor})
	m.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("old analysis ", 1600)})
	plan, err := m.PlanWithRetention(Retention{ImmutablePrefix: 1, AllowTargetGrowth: true, SummarizeOversizedRecent: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.TargetTokens <= int(float64(policy.ContextWindow)*policy.TargetRatio) {
		t.Fatal("target did not grow around the exact task anchor")
	}
	if err := m.Apply(plan, "remaining verification"); err != nil {
		t.Fatal(err)
	}
	if m.Messages()[0].Content != anchor.Content {
		t.Fatal("large task anchor was truncated")
	}
}

func TestRetentionPinsWholePendingToolExchange(t *testing.T) {
	m := New(Policy{ContextWindow: 8000, TriggerRatio: .80, TargetRatio: .30, RecentRatio: .07}, nil)
	call := client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{
		{ID: "answered", Function: client.FunctionCall{Name: "read_file"}},
		{ID: "pending", Function: client.FunctionCall{Name: "read_file"}},
	}}
	result := client.Message{Role: client.RoleTool, ToolCallID: "answered", Content: "exact result"}
	m.Append(call)
	m.Append(result)
	m.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("old history ", 1000)})
	m.Append(client.Message{Role: client.RoleUser, Content: "latest"})
	plan, err := m.PlanWithRetention(Retention{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Immutable) != 2 || len(plan.Immutable[0].ToolCalls) != 2 || plan.Immutable[1].ToolCallID != "answered" {
		t.Fatalf("pending tool exchange was separated: %#v", plan.Immutable)
	}
}
