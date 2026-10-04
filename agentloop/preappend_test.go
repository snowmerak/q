package agentloop_test

import (
	"context"
	"strings"
	"testing"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
)

func TestToolResultsCompactBeforeAppendAndPreservePendingBatch(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "loom_read", true: "two_results"}[batch], func(t *testing.T) {
			policy := memory.Policy{ContextWindow: 256000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
			oldBytes, resultBytes, calls := 520000, 128<<10, 1
			if batch {
				oldBytes, resultBytes, calls = 460000, 80<<10, 2
			}
			history := []client.Message{{Role: client.RoleSystem, Content: "Keep the contract."},
				{Role: client.RoleAssistant, Content: strings.Repeat("x", oldBytes)}, {Role: client.RoleUser, Content: "Read the ranges."}}
			host := memory.New(policy, history)
			if host.ShouldCompact() {
				t.Fatal("existing history must fit before receiving the result")
			}
			runtime := &scriptedTools{name: "loom_read", content: "EXACT_RANGE:" + strings.Repeat("y", resultBytes)}
			configured := &scriptedClient{}
			ordinary := 0
			configured.chat = func(request client.ChatRequest) *client.ChatResponse {
				if isPreAppendCheckpoint(request) {
					if strings.Contains(joined(request.Messages), "EXACT_RANGE:") {
						t.Error("the new range was included in the summary request")
					}
					return finalResponse(`{"active_work":["Earlier work condensed."]}`)
				}
				ordinary++
				if ordinary == 1 {
					response := toolResponse("loom_read")
					response.ConversationID = "native-original"
					if batch {
						second := response.Choices[0].Message.ToolCalls[0]
						second.ID = "call-2"
						response.Choices[0].Message.ToolCalls = append(response.Choices[0].Message.ToolCalls, second)
					}
					return response
				}
				if request.ConversationID != "" {
					t.Error("compacted history reused the old native conversation")
				}
				found := 0
				for _, message := range request.Messages {
					if message.Role == client.RoleTool && (message.ToolCallID == "call-1" || message.ToolCallID == "call-2") {
						found++
						if message.Content != runtime.content {
							t.Error("tool result changed across pre-append compaction")
						}
					}
				}
				if found != calls {
					t.Errorf("kept %d tool results, want %d", found, calls)
				}
				return finalResponse("done")
			}
			events := make(chan agentloop.Event)
			go agentloop.RunAgentLoop(t.Context(), agentloop.Request{Client: configured, Tools: runtime, Model: "test", Messages: history, ContextPolicy: policy}, events)
			compactions, toolResults := 0, 0
			for event := range events {
				if err := event.Err(); err != nil {
					t.Fatal(err)
				}
				if compaction, ok := event.Compaction(); ok {
					compactions++
					if toolResults != calls-1 || host.ShouldCompact() {
						t.Errorf("compaction arrived after the overflowing result: results=%d predicted=%d", toolResults, host.PredictedTokens())
					}
					if err := host.Apply(compaction.Plan, compaction.Summary); err != nil {
						t.Fatal(err)
					}
				}
				if message, ok := event.Message(); ok {
					host.Append(message)
					if message.Role == client.RoleTool && message.Name == "loom_read" {
						toolResults++
					}
					if host.ShouldCompact() {
						t.Errorf("host history crossed 85%% after adding %s", message.Role)
					}
				}
			}
			if compactions != 1 || toolResults != calls || len(runtime.calls) != calls || ordinary != 2 {
				t.Fatalf("compactions=%d results=%d calls=%d model requests=%d", compactions, toolResults, len(runtime.calls), ordinary)
			}
		})
	}
}

func isPreAppendCheckpoint(request client.ChatRequest) bool {
	return len(request.Tools) == 0 && strings.Contains(joined(request.Messages), "session continuation checkpoint")
}

func TestOversizedToolResultFailsBeforeEnteringHistory(t *testing.T) {
	policy := memory.Policy{ContextWindow: 16000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
	history := []client.Message{{Role: client.RoleAssistant, Content: strings.Repeat("x", 24000)}, {Role: client.RoleUser, Content: "read"}}
	runtime := &scriptedTools{name: "loom_read", content: strings.Repeat("y", 50000)}
	configured := &scriptedClient{chat: func(request client.ChatRequest) *client.ChatResponse {
		if isPreAppendCheckpoint(request) {
			t.Error("a result larger than the limit cannot be fixed by summarizing history")
		}
		return toolResponse("loom_read")
	}}
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(t.Context(), agentloop.Request{Client: configured, Tools: runtime, Model: "test", Messages: history, ContextPolicy: policy}, events)
	errors := 0
	for event := range events {
		if message, ok := event.Message(); ok && message.Role == client.RoleTool {
			t.Fatal("oversized result was committed to history")
		}
		if err := event.Err(); err != nil {
			errors++
			if !strings.Contains(err.Error(), "incoming history exceeds") {
				t.Fatalf("unclear overflow error: %v", err)
			}
		}
	}
	if errors != 1 || len(runtime.calls) != 1 || len(configured.requests) != 1 {
		t.Fatalf("errors=%d tools=%d requests=%d", errors, len(runtime.calls), len(configured.requests))
	}
}

type cancelledCheckpointClient struct{}

func (cancelledCheckpointClient) Chat(ctx context.Context, _ client.ChatRequest) (*client.ChatResponse, error) {
	return nil, ctx.Err()
}

func TestCancelledPreAppendCompactionKeepsOriginalContext(t *testing.T) {
	policy := memory.Policy{ContextWindow: 16000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
	history := []client.Message{{Role: client.RoleAssistant, Content: strings.Repeat("x", 24000)}, {Role: client.RoleUser, Content: "continue"}}
	loop := agentloop.NewContext(policy, history, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	compaction, err := loop.CompactBeforeAppend(ctx, cancelledCheckpointClient{}, "test", "", client.Message{Role: client.RoleUser, Content: strings.Repeat("y", 18000)})
	if err == nil || compaction != nil || joined(loop.Messages()) != joined(history) {
		t.Fatalf("cancelled reservation changed history: compaction=%v err=%v", compaction, err)
	}
}
