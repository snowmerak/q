package memory

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestSkillResourceReReadIsAppendOnlyUntilCompactionAndKeepsLatest(t *testing.T) {
	policy := Policy{ContextWindow: 24_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
	manager := New(policy, []client.Message{{Role: client.RoleSystem, Content: "exact contract"}})
	for _, message := range skillReadMessages("old-read", "skill-one", "SKILL.md", "old skill body") {
		manager.Append(message)
	}
	prefix := manager.Messages()
	manager.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("ordinary history ", 3_000)})
	for _, message := range skillReadMessages("new-read", "skill-one", "SKILL.md", "new skill body") {
		manager.Append(message)
	}
	if got := manager.Messages(); !reflect.DeepEqual(got[:len(prefix)], prefix) {
		t.Fatal("re-reading a skill changed the existing prompt prefix")
	}
	manager.Append(client.Message{Role: client.RoleUser, Content: "latest request"})
	beforePlan := manager.Messages()

	plan, err := manager.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manager.Messages(), beforePlan) {
		t.Fatal("planning compaction mutated the append-only prompt")
	}
	retained := messagesJSON(t, plan.RetainedSkillResources)
	if strings.Contains(retained, "old skill body") || !strings.Contains(retained, "new skill body") {
		t.Fatalf("retained skill resources = %s", retained)
	}
	source := messagesJSON(t, plan.Source)
	if strings.Contains(source, "old skill body") || strings.Contains(source, "new skill body") {
		t.Fatalf("skill bodies leaked into checkpoint source: %s", source)
	}
	if err := manager.Apply(plan, checkpointResponse("continue with the latest skill")); err != nil {
		t.Fatal(err)
	}
	compacted := messagesJSON(t, manager.Messages())
	if strings.Contains(compacted, "old skill body") || strings.Count(compacted, "new skill body") != 1 {
		t.Fatalf("compacted context = %s", compacted)
	}
}

func TestSkillResourceRetentionTreatsReferencesIndependently(t *testing.T) {
	reads := []skillResourceRead{
		skillRead("main", "skill-one", "SKILL.md", strings.Repeat("x", 600)),
		skillRead("guide", "skill-one", "references/guide.md", strings.Repeat("y", 600)),
		skillRead("check", "skill-two", "references/check.md", strings.Repeat("z", 600)),
	}
	newerCost := CountMessages(reads[1].messages()) + CountMessages(reads[2].messages())
	retained := retainRecentSkillResources(reads, newerCost*10)
	content := messagesJSON(t, retained)
	if strings.Contains(content, strings.Repeat("x", 600)) ||
		!strings.Contains(content, strings.Repeat("y", 600)) ||
		!strings.Contains(content, strings.Repeat("z", 600)) {
		t.Fatalf("retained resources = %s", content)
	}
}

func TestNewestSkillResourceMayExceedTenPercentBudget(t *testing.T) {
	read := skillRead("large", "skill-one", "references/large.md", strings.Repeat("large resource ", 1_000))
	cost := CountMessages(read.messages())
	contextWindow := max(1, (cost-1)*10)
	if budget := int(float64(contextWindow) * skillRetentionRatio); cost <= budget {
		t.Fatalf("test resource cost %d does not exceed budget %d", cost, budget)
	}
	retained := retainRecentSkillResources([]skillResourceRead{read}, contextWindow)
	if !reflect.DeepEqual(retained, read.messages()) {
		t.Fatal("newest oversized skill resource was truncated or discarded")
	}
}

func TestSkillRetentionKeepsAContiguousRecentResourceSet(t *testing.T) {
	reads := []skillResourceRead{
		skillRead("old-small", "skill-one", "references/old.md", "old small"),
		skillRead("middle-large", "skill-two", "SKILL.md", strings.Repeat("middle large ", 600)),
		skillRead("new-small", "skill-three", "SKILL.md", "new small"),
	}
	newestCost := CountMessages(reads[2].messages())
	contextWindow := (newestCost + 20) * 10
	retained := messagesJSON(t, retainRecentSkillResources(reads, contextWindow))
	if !strings.Contains(retained, "new small") || strings.Contains(retained, "middle large") || strings.Contains(retained, "old small") {
		t.Fatalf("retention backfilled past a newer resource that did not fit: %s", retained)
	}
}

func TestSkillRetentionSplitsParallelToolExchangeWithoutChangingResourceContent(t *testing.T) {
	skillMessages := skillReadMessages("skill", "skill-one", "SKILL.md", "exact skill body")
	readCall := client.ToolCall{
		ID: "read", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`},
	}
	assistant := client.Message{
		Role: client.RoleAssistant, Content: "inspect both resources",
		ToolCalls: []client.ToolCall{skillMessages[0].ToolCalls[0], readCall},
	}
	readResult := client.Message{Role: client.RoleTool, Name: "read_file", ToolCallID: "read", Content: "ordinary file"}
	original := []client.Message{assistant, skillMessages[1], readResult}
	ordinary, retained := splitSkillResourceReads(original, 0, 16_000)
	if len(ordinary) != 2 || len(ordinary[0].ToolCalls) != 1 || ordinary[0].ToolCalls[0].ID != "read" ||
		ordinary[1].ToolCallID != "read" {
		t.Fatalf("ordinary parallel exchange = %#v", ordinary)
	}
	if len(retained) != 2 || len(retained[0].ToolCalls) != 1 || retained[0].ToolCalls[0].ID != "skill" ||
		retained[1].Content != skillMessages[1].Content {
		t.Fatalf("retained skill exchange = %#v", retained)
	}
	if !reflect.DeepEqual(original[0], assistant) {
		t.Fatal("splitting a parallel exchange mutated the original prompt")
	}
}

func TestSkillRetentionBudgetDoesNotReduceOrdinaryCompactionTarget(t *testing.T) {
	policy := Policy{ContextWindow: 24_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
	ordinary := []client.Message{
		{Role: client.RoleSystem, Content: "contract"},
		{Role: client.RoleAssistant, Content: strings.Repeat("ordinary history ", 3_000)},
		{Role: client.RoleUser, Content: "latest request"},
	}
	withoutSkill := New(policy, ordinary)
	base, err := withoutSkill.Plan()
	if err != nil {
		t.Fatal(err)
	}
	withMessages := append([]client.Message(nil), ordinary[:1]...)
	withMessages = append(withMessages, skillReadMessages("skill", "skill-one", "SKILL.md", "direct body")...)
	withMessages = append(withMessages, ordinary[1:]...)
	withSkill := New(policy, withMessages)
	plan, err := withSkill.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if plan.TargetTokens != base.TargetTokens || plan.OutputBudget != base.OutputBudget {
		t.Fatalf("skill budget changed ordinary target: base target/output %d/%d, skill %d/%d",
			base.TargetTokens, base.OutputBudget, plan.TargetTokens, plan.OutputBudget)
	}
	if len(plan.RetainedSkillResources) != 2 {
		t.Fatalf("retained skill messages = %#v", plan.RetainedSkillResources)
	}
}

func BenchmarkPlanWithSkillResourceRetention160K(b *testing.B) {
	policy := Policy{ContextWindow: 258_400, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
	messages := []client.Message{{Role: client.RoleSystem, Content: "role contract"}}
	for index := 0; index < 120; index++ {
		messages = append(messages, client.Message{Role: client.RoleAssistant, Content: strings.Repeat("history ", 450)})
		if index%12 == 0 {
			messages = append(messages, skillReadMessages(
				"skill-"+string(rune('a'+index/12)), "skill-one", "references/guide.md", strings.Repeat("guidance ", 320),
			)...)
		}
	}
	messages = append(messages, client.Message{Role: client.RoleUser, Content: "latest request"})
	manager := New(policy, messages)
	inputTokens := manager.LocalEstimate()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		plan, err := manager.Plan()
		if err != nil {
			b.Fatal(err)
		}
		if len(plan.RetainedSkillResources) == 0 {
			b.Fatal("benchmark retained no skill resources")
		}
	}
	b.ReportMetric(float64(inputTokens), "input_tokens")
}

func skillRead(callID, skillID, resource, content string) skillResourceRead {
	messages := skillReadMessages(callID, skillID, resource, content)
	key, _ := skillResourceKey(messages[1].Content)
	return skillResourceRead{key: key, call: messages[0].ToolCalls[0], result: messages[1]}
}

func skillReadMessages(callID, skillID, resource, content string) []client.Message {
	arguments, _ := json.Marshal(map[string]string{"id": skillID, "path": resource})
	result, _ := json.Marshal(map[string]any{
		"skill": map[string]string{"id": skillID, "name": skillID},
		"path":  resource, "content": content,
	})
	call := client.ToolCall{
		ID: callID, Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: skillToolName, Arguments: string(arguments)},
	}
	return []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}},
		{Role: client.RoleTool, Name: skillToolName, ToolCallID: callID, Content: string(result)},
	}
}

func messagesJSON(t testing.TB, messages []client.Message) string {
	t.Helper()
	body, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
