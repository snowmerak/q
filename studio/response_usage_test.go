package studio

import (
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

func TestSessionDetailAssociatesUsageWithAssistantMessages(t *testing.T) {
	cached := 0
	detail := detailFromSession("root", workspace.Store{}, workspace.Session{
		Transcript: []client.Message{
			{Role: client.RoleSystem, Content: "instructions"},
			{Role: client.RoleUser, Content: "question"},
			{Role: client.RoleAssistant, Content: "old answer"},
			{Role: client.RoleTool, Content: "result"},
			{Role: client.RoleAssistant, Content: "new answer"},
		},
		ResponseUsage: []workspace.ResponseUsage{{AssistantIndex: 1, TokenUsage: workspace.TokenUsage{InputTokens: 100, CachedTokens: &cached, OutputTokens: 20}}},
	})
	for index, message := range detail.Transcript {
		if (message.Usage != nil) != (index == 4) {
			t.Fatalf("message %d usage = %#v", index, message.Usage)
		}
	}
	if got := detail.Transcript[4].Usage; got.InputTokens != 100 || got.OutputTokens != 20 || got.CachedTokens == nil || *got.CachedTokens != 0 {
		t.Fatalf("response usage = %#v", got)
	}
}
