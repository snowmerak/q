package memory

import (
	"reflect"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestPreAppendCompactionReservesResultAndKeepsPendingCall(t *testing.T) {
	policy := Policy{ContextWindow: 16000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
	call := client.ToolCall{ID: "pending-read", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "loom_read", Arguments: `{}`}}
	history := []client.Message{
		{Role: client.RoleSystem, Content: "role"},
		{Role: client.RoleAssistant, Content: strings.Repeat("x", 24000)},
		{Role: client.RoleUser, Content: "read the range"},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}},
	}
	manager := New(policy, history)
	incoming := client.ToolResultMessage(call, client.ToolResult{Content: strings.Repeat("y", 18000)})
	if manager.ShouldCompact() || !manager.ShouldCompactAfterAppend(incoming) {
		t.Fatal("prospective append must cross the threshold while existing history fits")
	}
	plan, err := manager.PlanBeforeAppend(Retention{PreserveInstructions: true, AllowTargetGrowth: true, SummarizeOversizedRecent: true}, incoming)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manager.Messages(), history) || strings.Contains(joinMessagesForPreAppend(plan.RequestMessages()), incoming.Content) {
		t.Fatal("incoming result entered memory or the checkpoint request before compaction")
	}
	if err := manager.Apply(plan, checkpointResponse("old work condensed")); err != nil {
		t.Fatal(err)
	}
	manager.Append(incoming)
	if manager.ShouldCompact() {
		t.Fatalf("reserved append still exceeds the threshold: %d", manager.PredictedTokens())
	}
	var paired bool
	for _, message := range manager.Messages() {
		for _, retained := range message.ToolCalls {
			paired = paired || retained.ID == call.ID
		}
	}
	if !paired || manager.Messages()[len(manager.Messages())-1].Content != incoming.Content {
		t.Fatal("pending call or exact result was lost")
	}
}

func joinMessagesForPreAppend(messages []client.Message) string {
	var content string
	for _, message := range messages {
		content += message.Content
	}
	return content
}

func TestPreAppendRejectsOversizedIncomingAndCheckpointAtomically(t *testing.T) {
	policy := Policy{ContextWindow: 16000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
	history := []client.Message{{Role: client.RoleAssistant, Content: strings.Repeat("x", 24000)}, {Role: client.RoleUser, Content: "continue"}}
	manager := New(policy, history)
	if _, err := manager.PlanBeforeAppend(Retention{}, client.Message{Role: client.RoleUser, Content: strings.Repeat("x", 50000)}); err == nil {
		t.Fatal("accepted an incoming message larger than the threshold")
	}
	plan, err := manager.PlanBeforeAppend(Retention{AllowTargetGrowth: true, SummarizeOversizedRecent: true}, client.Message{Role: client.RoleUser, Content: strings.Repeat("y", 18000)})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.CheckpointCopy(plan, checkpointResponse(strings.Repeat("z", 30000))); err == nil {
		t.Fatal("oversized checkpoint consumed the space reserved for incoming history")
	}
	if !reflect.DeepEqual(manager.Messages(), history) {
		t.Fatal("failed reservation changed existing history")
	}
}
