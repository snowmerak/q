package thinker

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestBuildContextChunkUsesClosedSegmentAndCapsFortyFivePercent(t *testing.T) {
	messages := make([]client.Message, 0, 12)
	for index := 0; index < 12; index++ {
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
	if chunk.TargetTokens != 1800 || chunk.MaximumTokens != 1800 || chunk.Tokens > chunk.MaximumTokens {
		t.Fatalf("chunk = %#v", chunk)
	}
	if len(chunk.Messages) != len(messages) || chunk.Messages[len(chunk.Messages)-1].Role != client.RoleAssistant {
		t.Fatalf("closed segment was not retained: %#v", chunk.Messages)
	}
}

func TestBuildContextChunkExcludesToolCallsAndResults(t *testing.T) {
	messages := []client.Message{
		{Role: client.RoleUser, Content: "Please inspect the repository."},
		{Role: client.RoleAssistant, Content: "I will inspect it.", ToolCalls: []client.ToolCall{{
			ID: "call-1", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`},
		}}},
		{Role: client.RoleTool, Name: "read_file", ToolCallID: "call-1", Content: "large raw tool result"},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
			ID: "call-2", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: "search", Arguments: `{"query":"internal"}`},
		}}},
		{Role: client.RoleTool, Name: "search", ToolCallID: "call-2", Content: "another tool result"},
		{Role: client.RoleAssistant, Content: "The repository uses a global Library."},
	}
	chunk, err := BuildContextChunk(messages, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunk.Messages) != 3 || chunk.SourceMessages != 3 {
		t.Fatalf("filtered context = %#v", chunk)
	}
	for _, message := range chunk.Messages {
		if message.Role == client.RoleTool || len(message.ToolCalls) != 0 || message.ToolCallID != "" {
			t.Fatalf("tool data remained in context: %#v", chunk.Messages)
		}
	}
	for _, excluded := range []string{"read_file", "README.md", "large raw tool result", "another tool result"} {
		if strings.Contains(chunk.Prompt, excluded) {
			t.Fatalf("prompt contains excluded tool data %q: %s", excluded, chunk.Prompt)
		}
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
