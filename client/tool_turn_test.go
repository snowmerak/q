package client

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func terminalRequest() ChatRequest {
	call := ToolCall{ID: "complete-1", Type: ToolTypeFunction, Function: FunctionCall{Name: "task_complete", Arguments: `{}`}}
	return ChatRequest{
		Model: "provider/model", ConversationID: "existing-conversation", ReasoningEffort: "high", WorkingDirectory: "workspace",
		Messages: []Message{
			{Role: RoleUser, Content: "finish the task"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{call}},
			ToolResultMessage(call, ToolResult{Content: `{"accepted":true}`}),
		},
		Tools:      []Tool{{Type: ToolTypeFunction, Function: FunctionDefinition{Name: "task_complete"}}},
		ToolChoice: ToolChoiceRequired,
	}
}

func terminalResponse(message Message) *ChatResponse {
	return &ChatResponse{ConversationID: "existing-conversation", Choices: []Choice{{Message: message, FinishReason: "stop"}}}
}

func TestFinishToolTurnReturnsResultUpstreamBeforeEnding(t *testing.T) {
	request := terminalRequest()
	original := terminalRequest()
	var observed []Message
	calls := 0
	finished, err := FinishToolTurn(t.Context(), request, func(_ context.Context, sent ChatRequest) (*ChatResponse, error) {
		calls++
		if sent.Model != request.Model || sent.ConversationID != request.ConversationID || sent.ReasoningEffort != request.ReasoningEffort || sent.WorkingDirectory != request.WorkingDirectory {
			t.Fatalf("terminal result changed provider/session: %#v", sent)
		}
		if sent.ToolChoice != ToolChoiceAuto || sent.ParallelToolCalls != request.ParallelToolCalls || !reflect.DeepEqual(sent.Tools, request.Tools) {
			t.Fatalf("terminal request changed tool settings or still forces a tool call: %#v", sent)
		}
		if len(sent.Messages) != len(request.Messages) {
			t.Fatalf("terminal delivery must not append a user message: %#v", sent.Messages)
		}
		last := sent.Messages[len(sent.Messages)-1]
		if last.Role != RoleTool || last.ToolCallID != "complete-1" || last.Name != "task_complete" || last.Content != original.Messages[2].Content+"\n\n"+terminalResultInstruction {
			t.Fatalf("terminal result lost its pairing or content: %#v", last)
		}
		response := terminalResponse(Message{Content: "Acknowledged."})
		response.Usage = Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}
		return response, nil
	}, func(message Message) error {
		observed = append(observed, message)
		return nil
	})
	if err != nil || calls != 1 || finished.Response.Choices[0].Message.Content != "Acknowledged." || finished.Usage.TotalTokens != 12 {
		t.Fatalf("calls=%d finished=%#v err=%v", calls, finished, err)
	}
	if len(observed) != 1 || observed[0].Role != RoleAssistant || !reflect.DeepEqual(request, original) {
		t.Fatalf("original history was mutated or acknowledgment lost: observed=%#v request=%#v", observed, request)
	}
}

func TestFinishToolTurnPreservesToolChoiceAndParallelSettings(t *testing.T) {
	for _, test := range []struct {
		name             string
		choice, expected any
	}{
		{"default", nil, nil},
		{"auto", ToolChoiceAuto, ToolChoiceAuto},
		{"string auto", "auto", "auto"},
		{"explicit none", ToolChoiceNone, ToolChoiceNone},
		{"required", ToolChoiceRequired, ToolChoiceAuto},
		{"string required", "required", ToolChoiceAuto},
		{"named", NamedToolChoice("task_complete"), ToolChoiceAuto},
	} {
		for _, setting := range []struct {
			name  string
			value *bool
		}{{"default", nil}, {"enabled", new(true)}, {"disabled", new(false)}} {
			t.Run(test.name+"/parallel="+setting.name, func(t *testing.T) {
				parallel := setting.value
				request := terminalRequest()
				request.ToolChoice, request.ParallelToolCalls = test.choice, parallel
				_, err := FinishToolTurn(t.Context(), request, func(_ context.Context, sent ChatRequest) (*ChatResponse, error) {
					if !reflect.DeepEqual(sent.ToolChoice, test.expected) || sent.ParallelToolCalls != parallel {
						t.Fatalf("choice=%#v parallel=%v", sent.ToolChoice, sent.ParallelToolCalls)
					}
					return terminalResponse(Message{Role: RoleAssistant, Content: "done"}), nil
				}, nil)
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestFinishToolTurnRetainsCustomReplyInstructionAfterRejectedCalls(t *testing.T) {
	const instruction = "Provide the requested findings, not a completion acknowledgment. No more tools."
	request := terminalRequest()
	rounds := 0
	finished, err := FinishToolTurn(t.Context(), request, func(_ context.Context, sent ChatRequest) (*ChatResponse, error) {
		rounds++
		last := sent.Messages[len(sent.Messages)-1]
		if !strings.Contains(last.Content, instruction) || strings.Contains(last.Content, "short acknowledgment") {
			t.Fatalf("reply policy changed: %s", last.Content)
		}
		if rounds == 1 {
			return terminalResponse(Message{ToolCalls: []ToolCall{{ID: "late", Function: FunctionCall{Name: "write_file", Arguments: `{}`}}}}), nil
		}
		if last.ToolCallID != "late" || !strings.Contains(last.Content, "Tool error:") {
			t.Fatalf("late call was not rejected: %+v", last)
		}
		return terminalResponse(Message{Content: "Here are the findings."}), nil
	}, nil, FinishToolTurnOptions{Instruction: instruction})
	if err != nil || rounds != 2 || finished.Response.Choices[0].Message.Content != "Here are the findings." {
		t.Fatalf("finished=%+v rounds=%d err=%v", finished, rounds, err)
	}
}

func TestFinishToolTurnRejectsEveryExtraCallAndDrainsItsResults(t *testing.T) {
	extra := Message{Role: RoleAssistant, ToolCalls: []ToolCall{
		{ID: "late-write", Type: ToolTypeFunction, Function: FunctionCall{Name: "write_file", Arguments: `{}`}},
		{Type: ToolTypeFunction, Function: FunctionCall{Name: "task_complete", Arguments: `{}`}},
	}}
	calls := 0
	var observed []Message
	request := terminalRequest()
	request.ToolChoice, request.ParallelToolCalls = nil, new(true)
	finished, err := FinishToolTurn(t.Context(), request, func(_ context.Context, request ChatRequest) (*ChatResponse, error) {
		calls++
		if request.ToolChoice != nil || request.ParallelToolCalls == nil || !*request.ParallelToolCalls {
			t.Fatal("rejection round changed cache-relevant tool settings")
		}
		response := terminalResponse(Message{Role: RoleAssistant, Content: "Finished."})
		response.Usage = Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}
		if calls == 1 {
			response.ConversationID = "continued-conversation"
			response.Choices[0] = Choice{Message: extra, FinishReason: "tool_calls"}
			return response, nil
		}
		if request.ConversationID != "continued-conversation" || len(request.Messages) != 6 {
			t.Fatalf("missing rejection continuation: %#v", request)
		}
		assistant := request.Messages[3]
		for index, call := range assistant.ToolCalls {
			result := request.Messages[4+index]
			if call.ID == "" || result.ToolCallID != call.ID || result.Role != RoleTool || result.Name != call.Function.Name || !strings.HasPrefix(result.Content, "Tool error:") {
				t.Fatalf("extra call did not receive an error result: call=%#v result=%#v", call, result)
			}
		}
		return response, nil
	}, func(message Message) error {
		observed = append(observed, message)
		return nil
	})
	if err != nil || calls != 2 || len(observed) != 4 || finished.Usage.TotalTokens != 24 || finished.Response.Usage.TotalTokens != 12 {
		t.Fatalf("calls=%d observed=%#v finished=%#v err=%v", calls, observed, finished, err)
	}
	if extra.ToolCalls[1].ID != "" {
		t.Fatal("provider-owned tool call slice was mutated")
	}
}

func TestFinishToolTurnFailsInsteadOfReportingUnfinishedTurnAsComplete(t *testing.T) {
	transportErr := errors.New("transport failed")
	for _, test := range []struct {
		name     string
		response *ChatResponse
		err      error
	}{
		{name: "transport", err: transportErr},
		{name: "nil response"},
		{name: "no choices", response: &ChatResponse{}},
		{name: "token limit", response: &ChatResponse{Choices: []Choice{{Message: Message{Content: "partial"}, FinishReason: "length"}}}},
		{name: "missing calls", response: &ChatResponse{Choices: []Choice{{FinishReason: "tool_calls"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := FinishToolTurn(t.Context(), terminalRequest(), func(context.Context, ChatRequest) (*ChatResponse, error) {
				return test.response, test.err
			}, nil)
			if err == nil || (test.err != nil && !errors.Is(err, test.err)) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestFinishToolTurnHonorsCancellationAndRequiresToolResult(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	chat := func(context.Context, ChatRequest) (*ChatResponse, error) {
		t.Fatal("unexpected provider call")
		return nil, nil
	}
	if _, err := FinishToolTurn(ctx, terminalRequest(), chat, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	if _, err := FinishToolTurn(t.Context(), ChatRequest{}, chat, nil); err == nil {
		t.Fatal("accepted a request without a tool result")
	}
}

func TestFinishToolTurnBoundsRepeatedProviderToolCalls(t *testing.T) {
	calls := 0
	_, err := FinishToolTurn(t.Context(), terminalRequest(), func(context.Context, ChatRequest) (*ChatResponse, error) {
		calls++
		return terminalResponse(Message{ToolCalls: []ToolCall{{ID: "repeated", Function: FunctionCall{Name: "task_complete"}}}}), nil
	}, nil)
	if err == nil || calls != maximumTerminalRounds || !strings.Contains(err.Error(), "kept requesting tools") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestToolResultMessageIncludesErrorAndOriginalCallID(t *testing.T) {
	call := ToolCall{ID: "call-1", Function: FunctionCall{Name: "tool"}}
	message := ToolResultMessage(call, ToolResult{Content: "invalid input", IsError: true})
	if message.Role != RoleTool || message.Name != "tool" || message.ToolCallID != call.ID || message.Content != "Tool error: invalid input" {
		t.Fatalf("result = %#v", message)
	}
}
