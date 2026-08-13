package thinker

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestBuildContextChunkTargetsThirtyFiveAndCapsFortyFivePercent(t *testing.T) {
	messages := make([]client.Message, 0, 40)
	for index := 0; index < 40; index++ {
		role := client.RoleUser
		if index%2 == 1 {
			role = client.RoleAssistant
		}
		messages = append(messages, client.Message{Role: role, Content: strings.Repeat("bounded context token ", 20)})
	}
	chunk, err := BuildContextChunk(messages, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if chunk.TargetTokens != 1400 || chunk.MaximumTokens != 1800 || chunk.Tokens > chunk.MaximumTokens || !chunk.Truncated {
		t.Fatalf("chunk = %#v", chunk)
	}
	if len(chunk.Messages) == 0 || chunk.Messages[len(chunk.Messages)-1].Role != client.RoleAssistant {
		t.Fatalf("latest messages were not retained: %#v", chunk.Messages)
	}
}

func TestBuildContextChunkKeepsToolExchangeAtomic(t *testing.T) {
	messages := []client.Message{
		{Role: client.RoleUser, Content: strings.Repeat("old ", 1200)},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
			ID: "call-1", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`},
		}}},
		{Role: client.RoleTool, Name: "read_file", ToolCallID: "call-1", Content: "tool result"},
	}
	chunk, err := BuildContextChunk(messages, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunk.Messages) != 3 || len(chunk.Messages[1].ToolCalls) != 1 || chunk.Messages[2].ToolCallID != "call-1" {
		t.Fatalf("tool exchange was split: %#v", chunk.Messages)
	}
}

func TestBuildContextChunkTrimsOversizedLatestMessageBelowHardCap(t *testing.T) {
	chunk, err := BuildContextChunk([]client.Message{{
		Role: client.RoleUser, Content: strings.Repeat("가", 20_000),
	}}, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if chunk.Tokens > 1800 || !chunk.Truncated || !strings.Contains(chunk.Messages[0].Content, "truncated") {
		t.Fatalf("chunk = %#v", chunk)
	}
}
