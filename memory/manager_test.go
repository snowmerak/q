package memory

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestNewWithToolTokensPreservesUnreportedUsage(t *testing.T) {
	initial := []client.Message{{Role: client.RoleUser, Content: "Inspect the project."}}
	const toolTokens = 70
	m := NewWithToolTokens(Policy{}, initial, toolTokens)
	if m.PromptUsage() != (PromptUsage{}) {
		t.Fatalf("reserved tools reported as prompt usage: %#v", m.PromptUsage())
	}
	want := New(Policy{}, initial).PredictedTokens() + toolTokens
	if got := m.PredictedTokens(); got != want {
		t.Fatalf("predicted tokens = %d, want %d with tool reservation", got, want)
	}
	m.ObserveUsage(200, m.LocalEstimate())
	if got := m.PredictedTokens(); got != 200 {
		t.Fatalf("predicted tokens = %d after provider usage of 200", got)
	}
}

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
	for range 8 {
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
	if err := m.Apply(plan, checkpointResponse("goals and decisions")); err != nil {
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

func TestCheckpointCopyKeepsOriginalUntilCommit(t *testing.T) {
	policy := Policy{ContextWindow: 4000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
	m := New(policy, []client.Message{{Role: client.RoleSystem, Content: "keep system"}})
	for range 8 {
		m.Append(client.Message{Role: client.RoleUser, Content: strings.Repeat("old context ", 80)})
		m.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("answer ", 80)})
	}
	m.Append(client.Message{Role: client.RoleUser, Content: "latest request"})
	m.ObserveUsage(m.LocalEstimate()+100, m.LocalEstimate())
	plan, err := m.Plan()
	if err != nil {
		t.Fatal(err)
	}
	before := m.Messages()
	compacted, checkpoint, err := m.CheckpointCopy(plan, checkpointResponse("durable state"))
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint == "" || len(m.Messages()) != len(before) || m.Stats().Compactions != 0 {
		t.Fatalf("live manager changed before commit: %#v", m.Stats())
	}
	if compacted.Stats().Compactions != 1 || compacted.Stats().ProviderOverhead != m.Stats().ProviderOverhead {
		t.Fatalf("staged manager lost statistics: %#v", compacted.Stats())
	}
	if compacted.PromptUsage() != (PromptUsage{}) || m.PromptUsage().InputTokens == 0 {
		t.Fatal("compaction must discard the observation only for the new context")
	}
	if len(compacted.Messages()) >= len(before) {
		t.Fatalf("staged manager was not compacted: %d >= %d", len(compacted.Messages()), len(before))
	}
}

func TestApplyAcceptsCompactedContextAboveTarget(t *testing.T) {
	policy := Policy{ContextWindow: 10_000, TriggerRatio: .80, TargetRatio: .15, RecentRatio: .05}
	manager := New(policy, []client.Message{{Role: client.RoleSystem, Content: "keep system"}})
	for range 16 {
		manager.Append(client.Message{Role: client.RoleUser, Content: strings.Repeat("old context ", 120)})
		manager.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("old answer ", 120)})
	}
	manager.Append(client.Message{Role: client.RoleUser, Content: "latest request"})
	plan, err := manager.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if plan.TargetTokens != 1_500 {
		t.Fatalf("compaction target = %d", plan.TargetTokens)
	}
	if err := manager.Apply(plan, checkpointResponse(strings.Repeat("x", 9_000))); err != nil {
		t.Fatalf("context above target was rejected: %v", err)
	}
	if got := manager.PredictedTokens(); got <= plan.TargetTokens {
		t.Fatalf("compacted context = %d, want above advisory target %d", got, plan.TargetTokens)
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

func TestReportedUsageReplacesEstimateAndKeepsOnlyHistoryDelta(t *testing.T) {
	manager := New(Policy{ContextWindow: 1000, TriggerRatio: .85}, []client.Message{
		{Role: client.RoleUser, Content: strings.Repeat("x", 2500)},
	})
	if !manager.ShouldCompact() {
		t.Fatal("fixture must reach the threshold by local estimate")
	}
	requestEstimate := manager.LocalEstimate()
	manager.ObserveUsage(900, requestEstimate)
	manager.ObserveUsage(400, requestEstimate)
	manager.ObserveUsage(0, requestEstimate)
	if got := manager.PredictedTokens(); got != 400 || manager.ShouldCompact() {
		t.Fatalf("predicted tokens = %d, want the latest reported input below the threshold", got)
	}
	reply := client.Message{Role: client.RoleAssistant, Content: "short reply"}
	manager.Append(reply)
	want := 400 + manager.LocalEstimate() - requestEstimate
	if got := manager.PredictedTokens(); got != want {
		t.Fatalf("predicted tokens = %d, want %d after appending the reply", got, want)
	}
	if !manager.ShouldCompactAfterAppend(client.Message{Role: client.RoleUser, Content: strings.Repeat("y", 1400)}) {
		t.Fatal("a later input should still trigger compaction when its estimate fills the context")
	}
}

func TestApplyCarriesHigherPlanOverhead(t *testing.T) {
	policy := Policy{ContextWindow: 100_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
	history := []client.Message{{Role: client.RoleSystem, Content: "keep system"}}
	for range 8 {
		history = append(history, client.Message{Role: client.RoleUser, Content: strings.Repeat("old context ", 1_000)})
	}
	source := New(policy, history)
	local := source.LocalEstimate()
	source.ObserveUsage(local+3_000, local)
	plan, err := source.Plan()
	if err != nil {
		t.Fatal(err)
	}

	destination := New(policy, history)
	if err := destination.Apply(plan, checkpointResponse("durable state")); err != nil {
		t.Fatal(err)
	}
	if got := destination.Stats().ProviderOverhead; got != 3_000 {
		t.Fatalf("provider overhead = %d", got)
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
	for range 6 {
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
	if err := m.Apply(plan, checkpointResponse("completed work and remaining state")); err != nil {
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
	if err := m.Apply(plan, checkpointResponse("remaining verification")); err != nil {
		t.Fatal(err)
	}
	if m.Messages()[0].Content != anchor.Content {
		t.Fatal("large task anchor was truncated")
	}
}

func checkpointResponse(activeWork string) string {
	body, _ := json.Marshal(Checkpoint{ActiveWork: []string{activeWork}})
	return string(body)
}

func TestPlanDoesNotApplySeparateCheckpointOutputCeiling(t *testing.T) {
	manager := New(Policy{
		ContextWindow: 1_000_000,
		TriggerRatio:  .85,
		TargetRatio:   .22,
		RecentRatio:   .01,
	}, []client.Message{
		{Role: client.RoleUser, Content: strings.Repeat("old context ", 50_000)},
		{Role: client.RoleUser, Content: "recent request"},
	})
	plan, err := manager.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if plan.OutputBudget <= 65_536 {
		t.Fatalf("output budget = %d; want dynamic target above the former ceiling", plan.OutputBudget)
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

func TestInPrefixMessageAndEligibility(t *testing.T) {
	basePlan := Plan{OutputBudget: 512}
	msg := basePlan.InPrefixMessage()
	if msg.Role != client.RoleUser || !strings.Contains(msg.Content, "Do NOT call any tools") ||
		!strings.Contains(msg.Content, "using no more than 512 tokens") ||
		strings.Contains(msg.Content, "maintained task memory already covers") {
		t.Fatalf("unexpected base InPrefixMessage: %#v", msg)
	}
	maintainedPlan := Plan{OutputBudget: 512, Maintained: &Checkpoint{Facts: []string{"f-1: ok"}}, MaintainedRevision: 3}
	if !strings.Contains(maintainedPlan.InPrefixMessage().Content, "revision 3") {
		t.Fatalf("missing maintained revision guidance: %q", maintainedPlan.InPrefixMessage().Content)
	}

	m := New(Policy{ContextWindow: 16_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}, []client.Message{
		{Role: client.RoleSystem, Content: "contract"},
		{Role: client.RoleAssistant, Content: strings.Repeat("work ", 2_000)},
		{Role: client.RoleUser, Content: "next"},
	})
	plan, err := m.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if !m.CanCompactInPrefix(plan) {
		t.Fatalf("expected eligible normal history (predicted=%d, window=16000)", m.PredictedTokens())
	}

	var nilMgr *Manager
	if nilMgr.CanCompactInPrefix(plan) {
		t.Fatal("nil manager should not be eligible")
	}

	unknown := New(Policy{ContextWindow: 0}, m.Messages())
	if unknown.CanCompactInPrefix(plan) {
		t.Fatal("zero context window should not be eligible")
	}

	pending := New(Policy{ContextWindow: 16_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}, append(m.Messages(), client.Message{
		Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "c-1", Function: client.FunctionCall{Name: "read_file"}}},
	}))
	if pending.CanCompactInPrefix(plan) {
		t.Fatal("pending tool exchange should fall back to detached compaction")
	}

	tight := New(Policy{ContextWindow: m.PredictedTokens() + 100, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}, m.Messages())
	if tight.CanCompactInPrefix(plan) {
		t.Fatal("insufficient headroom should fall back to detached compaction")
	}
}
