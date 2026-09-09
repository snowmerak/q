package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
	qtools "github.com/snowmerak/q/tools"
)

type compactingLoopClient struct {
	requests      []client.ChatRequest
	normalCalls   int
	compactCalls  int
	compactionErr error
}

func (c *compactingLoopClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	request.Messages = append([]client.Message(nil), request.Messages...)
	request.Tools = append([]client.Tool(nil), request.Tools...)
	c.requests = append(c.requests, request)
	if request.MaxCompletionTokens != nil && len(request.Tools) == 0 {
		c.compactCalls++
		if c.compactionErr != nil {
			return nil, c.compactionErr
		}
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant, Content: "condensed tool evidence",
		}}}}, nil
	}

	c.normalCalls++
	if c.normalCalls == 1 {
		return &client.ChatResponse{
			ConversationID: "before-compaction",
			Choices: []client.Choice{{Message: client.Message{
				Role: client.RoleAssistant,
				ToolCalls: []client.ToolCall{{
					ID: "large-read", Type: client.ToolTypeFunction,
					Function: client.FunctionCall{Name: "large_read", Arguments: `{}`},
				}},
			}}},
		}, nil
	}
	return &client.ChatResponse{
		ConversationID: "after-compaction",
		Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant, Content: "done",
		}}},
	}, nil
}

func (c *compactingLoopClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (c *compactingLoopClient) Close() error                                       { return nil }

type largeResultRuntime struct {
	content string
}

type instructionLoopClient struct {
	requests []client.ChatRequest
}

func (c *instructionLoopClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	request.Messages = append([]client.Message(nil), request.Messages...)
	c.requests = append(c.requests, request)
	if len(c.requests) <= 2 {
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant,
			ToolCalls: []client.ToolCall{{
				ID: "nested-write", Type: client.ToolTypeFunction,
				Function: client.FunctionCall{Name: "write_file", Arguments: `{"path":"app/model.go","content":"updated"}`},
			}},
		}}}}, nil
	}
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, Content: "done",
	}}}}, nil
}

func (*instructionLoopClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (*instructionLoopClient) Close() error                                       { return nil }

type instructionRuntime struct {
	calls []client.ToolCall
}

func (r *instructionRuntime) Tools() []client.Tool {
	return []client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name: "write_file", Parameters: map[string]any{"type": "object"},
	}}}
}

func (*instructionRuntime) Environment() qtools.HostEnvironment {
	return qtools.HostEnvironment{OS: "test", Architecture: "test", Shell: "test"}
}

func (r *instructionRuntime) Call(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
	r.calls = append(r.calls, call)
	return client.ToolResult{Content: `{"updated":true}`}, nil
}

func TestStreamAgentLoopLoadsNestedInstructionsBeforeToolExecution(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "AGENTS.md"), []byte("Never edit generated files."), 0o644); err != nil {
		t.Fatal(err)
	}
	configuredClient := &instructionLoopClient{}
	runtime := &instructionRuntime{}
	events := make(chan agentEvent)
	go streamAgentLoop(
		t.Context(), configuredClient, runtime, "test-model", "low",
		[]client.Message{{Role: client.RoleUser, Content: "update app/model.go"}}, "", root, nil, false, true,
		memoryPolicy(config.Default()), events,
	)
	var toolErrors int
	for event := range events {
		if event.err != nil {
			t.Fatal(event.err)
		}
		if event.toolIsError {
			toolErrors++
		}
	}
	if len(configuredClient.requests) != 3 || len(runtime.calls) != 1 || toolErrors != 1 {
		t.Fatalf("requests=%d runtime calls=%d tool errors=%d", len(configuredClient.requests), len(runtime.calls), toolErrors)
	}
	firstContent := joinedMessageContent(configuredClient.requests[0].Messages)
	secondContent := joinedMessageContent(configuredClient.requests[1].Messages)
	if strings.Contains(firstContent, "Never edit generated files.") || !strings.Contains(secondContent, "Never edit generated files.") {
		t.Fatalf("first=%q\nsecond=%q", firstContent, secondContent)
	}
	if configuredClient.requests[1].Messages[0].Role != client.RoleSystem {
		t.Fatalf("nested instruction was not moved to the leading block: %#v", configuredClient.requests[1].Messages)
	}
}

func (r largeResultRuntime) Tools() []client.Tool {
	return []client.Tool{{
		Type: client.ToolTypeFunction,
		Function: client.FunctionDefinition{
			Name: "large_read", Description: "return a large test result",
			Parameters: map[string]any{"type": "object"},
		},
	}}
}

func (largeResultRuntime) Environment() qtools.HostEnvironment {
	return qtools.HostEnvironment{OS: "test", Architecture: "test", Shell: "test"}
}

func (r largeResultRuntime) Call(context.Context, client.ToolCall) (client.ToolResult, error) {
	return client.ToolResult{Content: r.content}, nil
}

func TestStreamAgentLoopCompactsBetweenToolRounds(t *testing.T) {
	configuredClient := &compactingLoopClient{}
	hugeResult := strings.Repeat("large tool output ", 4_000)
	events := make(chan agentEvent)
	go streamAgentLoop(
		t.Context(), configuredClient, largeResultRuntime{content: hugeResult}, "test-model", "low",
		[]client.Message{
			{Role: client.RoleSystem, Content: "keep this system contract exactly"},
			{Role: client.RoleUser, Content: "read the large result"},
		},
		"initial-conversation", "", nil, false, false,
		memory.Policy{ContextWindow: 16_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07},
		events,
	)

	compactions := 0
	var final *client.ChatResponse
	for event := range events {
		if event.err != nil {
			t.Fatal(event.err)
		}
		if event.compaction != nil {
			compactions++
			if event.compaction.Summary != "condensed tool evidence" {
				t.Fatalf("summary = %q", event.compaction.Summary)
			}
		}
		if event.response != nil {
			final = event.response
		}
	}

	if configuredClient.normalCalls != 2 || configuredClient.compactCalls != 1 || compactions != 1 {
		t.Fatalf("normal calls = %d, compact calls = %d, events = %d", configuredClient.normalCalls, configuredClient.compactCalls, compactions)
	}
	if final == nil || final.Choices[0].Message.Content != "done" {
		t.Fatalf("final response = %#v", final)
	}

	var compactRequest, resumedRequest *client.ChatRequest
	for index := range configuredClient.requests {
		request := &configuredClient.requests[index]
		if request.MaxCompletionTokens != nil && len(request.Tools) == 0 {
			compactRequest = request
		}
		if len(request.Tools) > 0 && request.ConversationID == "" {
			resumedRequest = request
		}
	}
	if compactRequest == nil || compactRequest.ConversationID != "" || len(compactRequest.Tools) != 0 {
		t.Fatalf("compaction request = %#v", compactRequest)
	}
	if resumedRequest == nil {
		t.Fatal("no fresh post-compaction request")
	}
	resumedContent := joinedMessageContent(resumedRequest.Messages)
	if !strings.Contains(resumedContent, "Compressed conversation memory:\ncondensed tool evidence") {
		t.Fatalf("post-compaction messages do not contain summary: %#v", resumedRequest.Messages)
	}
	if strings.Contains(resumedContent, hugeResult) {
		t.Fatal("post-compaction request retained the oversized tool result")
	}
	if resumedRequest.Messages[0].Content != "keep this system contract exactly" {
		t.Fatalf("system contract changed: %#v", resumedRequest.Messages)
	}
}

func TestMainAgentLoopUsesConfiguredTrigger(t *testing.T) {
	history := []client.Message{{Role: client.RoleUser, Content: strings.Repeat("context ", 1_000)}}
	probe := newAgentLoopContext(memory.Policy{}, history, largeResultRuntime{}.Tools())
	predicted := probe.manager.PredictedTokens()
	contextWindowAtEightyTwoPercent := (predicted*100 + 81) / 82
	loopContext := newAgentLoopContext(memory.Policy{
		ContextWindow: contextWindowAtEightyTwoPercent,
		TriggerRatio:  .85,
		TargetRatio:   .22,
		RecentRatio:   .07,
	}, history, largeResultRuntime{}.Tools())
	if loopContext.ShouldCompact() {
		t.Fatalf("main loop compacted at about 82%% with configured 85%% trigger: predicted = %d, window = %d", predicted, contextWindowAtEightyTwoPercent)
	}
}

func TestStreamAgentLoopCompactionFailureStopsBeforeNextRound(t *testing.T) {
	compactErr := errors.New("summary backend unavailable")
	configuredClient := &compactingLoopClient{compactionErr: compactErr}
	events := make(chan agentEvent)
	go streamAgentLoop(
		t.Context(), configuredClient, largeResultRuntime{content: strings.Repeat("large result ", 5_000)},
		"test-model", "low", []client.Message{{Role: client.RoleUser, Content: "read"}},
		"initial-conversation", "", nil, false, false,
		memory.Policy{ContextWindow: 16_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07},
		events,
	)

	var gotErr error
	compactions := 0
	for event := range events {
		if event.compaction != nil {
			compactions++
		}
		if event.err != nil {
			gotErr = event.err
		}
	}
	if !errors.Is(gotErr, compactErr) {
		t.Fatalf("error = %v", gotErr)
	}
	if configuredClient.normalCalls != 1 || configuredClient.compactCalls != 1 || compactions != 0 {
		t.Fatalf("normal calls = %d, compact calls = %d, events = %d", configuredClient.normalCalls, configuredClient.compactCalls, compactions)
	}
}

func TestApplyAgentContextCompactionPreservesTranscript(t *testing.T) {
	policy := memory.Policy{ContextWindow: 12_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
	history := []client.Message{{Role: client.RoleSystem, Content: "system contract"}}
	for index := 0; index < 12; index++ {
		history = append(history,
			client.Message{Role: client.RoleUser, Content: strings.Repeat("old request ", 120)},
			client.Message{Role: client.RoleAssistant, Content: strings.Repeat("old response ", 120)},
		)
	}
	manager := memory.New(policy, history)
	plan, err := manager.PlanWithRetention(memory.Retention{
		PreserveInstructions: true, AllowTargetGrowth: true, SummarizeOversizedRecent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	transcript := append([]client.Message(nil), history...)
	archive := &collectingRecordArchive{}
	m := model{messages: transcript, memory: manager, conversationID: "old-provider-state", archive: archive}

	if err := m.applyAgentContextCompaction(agentContextCompaction{Plan: plan, Summary: "durable state"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.messages, history) {
		t.Fatal("full transcript changed during request-context compaction")
	}
	if m.conversationID != "" {
		t.Fatalf("conversation ID = %q", m.conversationID)
	}
	if !hasMessageNamed(m.memory.Messages(), memory.SummaryName) || m.memory.Stats().Compactions != 1 {
		t.Fatalf("memory was not compacted: %#v, stats = %#v", m.memory.Messages(), m.memory.Stats())
	}
	if len(archive.records) != 1 || archive.records[0].Content != "durable state" {
		t.Fatalf("archive records = %#v", archive.records)
	}
}

func hasMessageNamed(messages []client.Message, name string) bool {
	for _, message := range messages {
		if message.Name == name {
			return true
		}
	}
	return false
}

func joinedMessageContent(messages []client.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		builder.WriteString(message.Content)
	}
	return builder.String()
}
