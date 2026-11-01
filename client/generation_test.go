package client

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func generationResponse(text, reason string, tokens int) *ChatResponse {
	return &ChatResponse{Model: "test", Choices: []Choice{{Message: Message{Role: RoleAssistant, Content: text}, FinishReason: reason}}, Usage: Usage{PromptTokens: 10, CompletionTokens: tokens, TotalTokens: 10 + tokens}}
}

func TestGenerationContinuesTextWithinLimitAndAccountsEveryAttempt(t *testing.T) {
	ctx := WithGenerationSettings(t.Context(), GenerationSettings{MaxTokens: 64, MaxAttempts: 2, SafeReplay: true, RequireFinishReason: true})
	var attempts []GenerationAttempt
	ctx = WithGenerationObserver(ctx, func(a GenerationAttempt) { attempts = append(attempts, a) })
	var requests []ChatRequest
	response, err := RecoverGeneration(ctx, nil, ChatRequest{Model: "test", ReasoningEffort: "high", Tools: []Tool{{Type: ToolTypeFunction}}}, func(ctx context.Context, r ChatRequest) (*ChatResponse, error) {
		requests = append(requests, r)
		if len(requests) == 1 {
			return generationResponse("The answer is ", "length", 64), nil
		}
		return generationResponse("42.", "stop", 3), nil
	})
	if err != nil || response.Choices[0].Message.Content != "The answer is 42." || response.Usage.CompletionTokens != 67 || response.Usage.PromptTokens != 20 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if ContextUsage(response).PromptTokens != 10 {
		t.Fatal("memory calibrated from aggregate usage")
	}
	if len(requests) != 2 || len(requests[1].Tools) != 0 || requests[1].ToolChoice != ToolChoiceNone || requests[1].ReasoningEffort != "high" || outputLimit(requests[1]) != 64 {
		t.Fatalf("recovery changed policy: %+v", requests)
	}
	if attempts[0].Action != "continue_text" || attempts[1].ID != attempts[0].ID {
		t.Fatalf("attempts=%+v", attempts)
	}
}

func TestGenerationDiscardsAllCutoffToolsBeforeRegeneration(t *testing.T) {
	ctx := WithGenerationSettings(t.Context(), GenerationSettings{MaxAttempts: 2, SafeReplay: true})
	requests := 0
	partial := generationResponse("", "length", 50)
	partial.Choices[0].Message.ToolCalls = []ToolCall{{ID: "valid-but-rejected", Function: FunctionCall{Name: "write", Arguments: `{"path":"first"}`}}, {ID: "cutoff", Function: FunctionCall{Name: "write", Arguments: `{"path":`}}}
	response, err := RecoverGeneration(ctx, nil, ChatRequest{Model: "test", Messages: []Message{{Role: RoleUser, Content: "do work"}}}, func(ctx context.Context, r ChatRequest) (*ChatResponse, error) {
		requests++
		if requests == 1 {
			return partial, nil
		}
		for _, message := range r.Messages {
			if len(message.ToolCalls) > 0 {
				t.Fatal("partial calls entered replay history")
			}
		}
		result := generationResponse("", "tool_calls", 8)
		result.Choices[0].Message.ToolCalls = []ToolCall{{ID: "new", Function: FunctionCall{Name: "write", Arguments: `{"path":"complete"}`}}}
		return result, nil
	})
	if err != nil || requests != 2 || response.Choices[0].Message.ToolCalls[0].ID != "new" {
		t.Fatalf("response=%+v calls=%d err=%v", response, requests, err)
	}
}

func TestGenerationRecoveryStopsOnReasoningOnlyUnsafeOrDisabled(t *testing.T) {
	for _, test := range []struct {
		name, text string
		settings   GenerationSettings
	}{
		{"reasoning-only", "", GenerationSettings{MaxAttempts: 2, SafeReplay: true}},
		{"remote-agent", "partial", GenerationSettings{MaxAttempts: 2}},
		{"disabled", "partial", GenerationSettings{MaxAttempts: 0, SafeReplay: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			ctx := WithGenerationSettings(t.Context(), test.settings)
			_, err := RecoverGeneration(ctx, nil, ChatRequest{}, func(context.Context, ChatRequest) (*ChatResponse, error) {
				calls++
				return generationResponse(test.text, "length", 20), nil
			})
			incomplete, ok := errors.AsType[*IncompleteGeneration](err)
			if !ok || incomplete.Response.Usage.CompletionTokens != 20 || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestStructuredGenerationIsRegeneratedOnceAndNeverConcatenated(t *testing.T) {
	ctx := WithStructuredGeneration(WithGenerationSettings(t.Context(), GenerationSettings{MaxAttempts: 2, SafeReplay: true}))
	calls := 0
	response, err := RecoverGeneration(ctx, nil, ChatRequest{}, func(context.Context, ChatRequest) (*ChatResponse, error) {
		calls++
		if calls == 1 {
			return generationResponse(`{"partial":`, "length", 20), nil
		}
		return generationResponse(`{"complete":true}`, "stop", 8), nil
	})
	if err != nil || calls != 2 || response.Choices[0].Message.Content != `{"complete":true}` {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	calls = 0
	_, err = RecoverGeneration(ctx, nil, ChatRequest{}, func(context.Context, ChatRequest) (*ChatResponse, error) {
		calls++
		return generationResponse(`{"partial":`, "length", 20), nil
	})
	if err == nil || calls != 2 {
		t.Fatalf("unbounded structured replay: calls=%d err=%v", calls, err)
	}
}

func TestGenerationNeverExecutesContinuationToolSuggestions(t *testing.T) {
	ctx := WithGenerationSettings(t.Context(), GenerationSettings{MaxAttempts: 2, SafeReplay: true})
	calls := 0
	_, err := RecoverGeneration(ctx, nil, ChatRequest{}, func(context.Context, ChatRequest) (*ChatResponse, error) {
		calls++
		if calls == 1 {
			return generationResponse("partial", "length", 20), nil
		}
		r := generationResponse("", "tool_calls", 2)
		r.Choices[0].Message.ToolCalls = []ToolCall{{Function: FunctionCall{Name: "write", Arguments: `{}`}}}
		return r, nil
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected_continuation_tools") || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestGenerationClassifiesContextFailureAndSharesEmptyRetryBudget(t *testing.T) {
	ctx := WithGenerationSettings(t.Context(), GenerationSettings{MaxAttempts: 2, SafeReplay: true})
	calls := 0
	_, err := RecoverGeneration(ctx, nil, ChatRequest{}, func(context.Context, ChatRequest) (*ChatResponse, error) {
		calls++
		if calls == 1 {
			return generationResponse("", "stop", 1), nil
		}
		return generationResponse("more", "length", 20), nil
	})
	if calls != 3 || err == nil {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	calls = 0
	_, err = RecoverGeneration(ctx, nil, ChatRequest{}, func(context.Context, ChatRequest) (*ChatResponse, error) {
		calls++
		return nil, &APIError{StatusCode: 400, Message: "context_length_exceeded"}
	})
	incomplete, ok := errors.AsType[*IncompleteGeneration](err)
	if calls != 1 || !ok || incomplete.Reason != "context_limit" {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestCancelledRecoveryRetainsAlreadyDisplayedTextAndUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = WithGenerationSettings(ctx, GenerationSettings{MaxAttempts: 2, SafeReplay: true})
	calls := 0
	_, err := RecoverGeneration(ctx, nil, ChatRequest{}, func(context.Context, ChatRequest) (*ChatResponse, error) {
		calls++
		cancel()
		return generationResponse("keep this partial answer", "length", 12), nil
	})
	partial, ok := errors.AsType[*IncompleteGeneration](err)
	if !ok || !errors.Is(err, context.Canceled) || partial.Response.Choices[0].Message.Content != "keep this partial answer" || partial.Response.Usage.CompletionTokens != 12 || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestGenerationNeverRetriesARefusalWithEmptyContent(t *testing.T) {
	ctx := WithGenerationSettings(t.Context(), GenerationSettings{MaxAttempts: 2, SafeReplay: true})
	calls := 0
	_, err := RecoverGeneration(ctx, nil, ChatRequest{}, func(context.Context, ChatRequest) (*ChatResponse, error) {
		calls++
		r := generationResponse("", "stop", 8)
		r.Raw = []byte(`{"choices":[{"message":{"refusal":"Cannot fulfill this request."}}]}`)
		return r, nil
	})
	partial, ok := errors.AsType[*IncompleteGeneration](err)
	if !ok || partial.Reason != "refusal" || partial.Response.Choices[0].Message.Content != "Cannot fulfill this request." || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestOutputLimitPreservesTheSmallestExplicitAlias(t *testing.T) {
	legacy, standard := 32, 64
	r := ApplyOutputLimit(ChatRequest{MaxTokens: &legacy, MaxCompletionTokens: &standard}, 128, "")
	if r.MaxTokens != nil || r.MaxCompletionTokens == nil || *r.MaxCompletionTokens != 32 || legacy != 32 || standard != 64 {
		t.Fatalf("normalized request=%+v", r)
	}
}
