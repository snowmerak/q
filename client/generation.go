package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrEmptyResponse identifies a completion with neither text nor tool calls.
var ErrEmptyResponse = errors.New("provider returned an empty assistant response")

// IncompleteGeneration retains untrusted partial output for display/audit. Its
// tool calls must never be executed or inserted into normal model history.
type IncompleteGeneration struct {
	Reason   string
	Response *ChatResponse
	Limit    int
	Attempts int
	Cause    error
	Settings *GenerationSettings
}

func (e *IncompleteGeneration) Error() string {
	usage := Usage{}
	if e.Response != nil {
		usage = e.Response.Usage
	}
	limit := fmt.Sprint(e.Limit)
	if e.Limit <= 0 {
		limit = "provider default/unknown"
	}
	message := fmt.Sprintf("model generation incomplete: %s (limit=%s, output_tokens=%d, attempts=%d)", e.Reason, limit, usage.CompletionTokens, e.Attempts)
	if e.Response != nil {
		id := e.Response.ID
		if len(id) > 128 {
			id = id[:128]
		}
		message += fmt.Sprintf("; response_id=%s, prompt_tokens=%d", id, usage.PromptTokens)
	}
	if e.Cause != nil {
		message += "; " + e.Cause.Error()
	}
	return message
}
func (e *IncompleteGeneration) Unwrap() error { return e.Cause }

// GenerationSettings is a snapshot for one logical model generation. Injected
// clients must opt into SafeReplay before partial output can be regenerated.
type GenerationSettings struct {
	MaxTokens           int
	TokenField          string
	MaxAttempts         int
	SafeReplay          bool
	RequireFinishReason bool
}

type generationSettingsSource interface {
	GenerationSettings(context.Context, string) (GenerationSettings, error)
}

const OutputLimitSnapshotHeader = "X-Q-Output-Limit-Snapshot"

type OutputLimitSnapshot struct {
	ReasoningEffort          string `json:"reasoning_effort,omitempty"`
	AllowMissingFinishReason bool   `json:"allow_missing_finish_reason,omitempty"`
	Signature                string `json:"signature,omitempty"`
	Model                    string `json:"model"`
	Limit                    int    `json:"limit"`
	Field                    string `json:"field,omitempty"`
}
type generationSettingsKey struct{}

// WithGenerationSettings explicitly supplies policy to an embedded client.
func WithGenerationSettings(ctx context.Context, settings GenerationSettings) context.Context {
	return context.WithValue(ctx, generationSettingsKey{}, settings)
}

type GenerationAttempt struct {
	ID              string        `json:"id"`
	Index           int           `json:"index"`
	Reason          string        `json:"reason"`
	Action          string        `json:"action,omitempty"`
	Response        *ChatResponse `json:"-"`
	Model           string        `json:"model,omitempty"`
	Content         string        `json:"content,omitempty"`
	ToolCalls       []ToolCall    `json:"tool_calls,omitempty"`
	Usage           Usage         `json:"usage"`
	ReasoningTokens *int          `json:"reasoning_tokens,omitempty"`
}

type generationObserverKey struct{}

// WithGenerationObserver observes physical generation results without recording
// another usage charge. Existing transport usage recording remains authoritative.
func WithGenerationObserver(ctx context.Context, observer func(GenerationAttempt)) context.Context {
	return context.WithValue(ctx, generationObserverKey{}, observer)
}

// CheckGeneration checks termination before any caller can dispatch tools. Empty
// finish reasons remain compatible only when the owning connection allows them.
func CheckGeneration(response *ChatResponse, strict bool) error {
	if response == nil || len(response.Choices) == 0 {
		return errors.New("provider returned no choices")
	}
	choice := response.Choices[0]
	reason := choice.FinishReason
	if refusal := RefusalText(response.Raw); refusal != "" {
		if response.Choices[0].Message.TextContent() == "" {
			response.Choices[0].Message.Content = refusal
		}
		return &IncompleteGeneration{Reason: "refusal", Response: response}
	}
	switch reason {
	case "length", "max_tokens", "max_output_tokens":
		return &IncompleteGeneration{Reason: "output_limit", Response: response}
	case "content_filter", "refusal":
		return &IncompleteGeneration{Reason: reason, Response: response}
	case "cancelled", "canceled":
		return &IncompleteGeneration{Reason: "cancelled", Response: response, Cause: context.Canceled}
	case "", "stop", "end_turn", "tool_calls", "function_call":
		if strict && reason == "" {
			return &IncompleteGeneration{Reason: "missing_finish_reason", Response: response}
		}
	default:
		return &IncompleteGeneration{Reason: reason, Response: response}
	}
	if (reason == "tool_calls" || reason == "function_call") && len(choice.Message.ToolCalls) == 0 {
		return &IncompleteGeneration{Reason: "missing_tool_calls", Response: response}
	}
	for _, call := range choice.Message.ToolCalls {
		if call.Function.Name == "" || !json.Valid([]byte(call.Function.Arguments)) {
			return &IncompleteGeneration{Reason: "invalid_tool_arguments", Response: response}
		}
	}
	return nil
}

func generationSettings(ctx context.Context, configured any, model string) (GenerationSettings, error) {
	if settings, ok := ctx.Value(generationSettingsKey{}).(GenerationSettings); ok {
		return settings, nil
	}
	if source, ok := configured.(generationSettingsSource); ok {
		return source.GenerationSettings(ctx, model)
	}
	// Existing injected clients retain empty-answer recovery, but cannot replay
	// partial calls without explicitly declaring that their generation is pure.
	return GenerationSettings{MaxAttempts: 2}, nil
}

func ResolveGenerationSettings(ctx context.Context, configured any, model string) (GenerationSettings, error) {
	return generationSettings(ctx, configured, model)
}

// RecoverGeneration is the sole semantic generation recovery policy. It does not
// execute tools, change models, increase limits, or replay a remote agent turn.
func RecoverGeneration(ctx context.Context, configured any, request ChatRequest, invoke func(context.Context, ChatRequest) (*ChatResponse, error)) (*ChatResponse, error) {
	settings, err := generationSettings(ctx, configured, request.Model)
	if err != nil {
		return nil, err
	}
	_, explicitPolicy := ctx.Value(generationSettingsKey{}).(GenerationSettings)
	_, configuredPolicy := configured.(generationSettingsSource)
	legacyEmptyRecovery := !explicitPolicy && !configuredPolicy
	if settings.MaxAttempts < 0 || settings.MaxAttempts > 5 || settings.MaxTokens < 0 {
		return nil, errors.New("invalid generation settings")
	}
	if request.MaxTokens != nil && *request.MaxTokens <= 0 || request.MaxCompletionTokens != nil && *request.MaxCompletionTokens <= 0 {
		return nil, errors.New("generation output limit must be positive")
	}
	request = ApplyOutputLimit(request, settings.MaxTokens, settings.TokenField)
	base := request
	id := NewUsageEventID()
	observer, _ := ctx.Value(generationObserverKey{}).(func(GenerationAttempt))
	var total Usage
	var text strings.Builder
	var lastResponse *ChatResponse
	regenerated := false
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastResponse != nil {
				partial := *lastResponse
				partial.Usage = total
				partial.Choices = append([]Choice(nil), lastResponse.Choices...)
				if text.Len() > 0 && len(partial.Choices) > 0 {
					partial.Choices[0].Message.Content = text.String()
				}
				return nil, &IncompleteGeneration{Reason: "cancelled", Response: &partial, Limit: outputLimit(base), Attempts: attempt, Cause: err, Settings: &settings}
			}
			return nil, err
		}
		response, callErr := invoke(ctx, request)
		if response == nil && callErr != nil && lastResponse != nil {
			var supplied *IncompleteGeneration
			if !errors.As(callErr, &supplied) {
				partial := *lastResponse
				partial.Usage = Usage{}
				partial.Choices = append([]Choice(nil), lastResponse.Choices...)
				if text.Len() > 0 && len(partial.Choices) > 0 {
					partial.Choices[0].Message.Content = ""
					partial.Choices[0].Message.ToolCalls = nil
				}
				reason := "request_failed"
				if ctx.Err() != nil {
					reason = "cancelled"
				}
				callErr = &IncompleteGeneration{Reason: reason, Response: &partial, Cause: callErr}
			}
		}
		if isContextLimit(callErr) {
			callErr = &IncompleteGeneration{Reason: "context_limit", Response: response, Cause: callErr}
		}
		var incomplete *IncompleteGeneration
		if errors.As(callErr, &incomplete) && incomplete.Response != nil {
			response = incomplete.Response
		}
		if response != nil {
			total = SumUsage(total, response.Usage)
			lastResponse = response
		}
		if ctx.Err() != nil && response != nil {
			callErr = &IncompleteGeneration{Reason: "cancelled", Response: response, Cause: errors.Join(callErr, ctx.Err())}
		}
		owner, trusted := configured.(*Client)
		if response != nil && trusted && owner.forwardUsageMetadata && response.Headers.Get(OutputLimitSnapshotHeader) != "" {
			var snapshot OutputLimitSnapshot
			if json.Unmarshal([]byte(response.Headers.Get(OutputLimitSnapshotHeader)), &snapshot) == nil && snapshot.Model != "" && snapshot.Limit >= 0 {
				settings.RequireFinishReason = !snapshot.AllowMissingFinishReason
				base.Model = snapshot.Model
				base.ReasoningEffort = snapshot.ReasoningEffort
				base = ApplyOutputLimit(base, snapshot.Limit, snapshot.Field)
				settings.MaxTokens, settings.TokenField = outputLimit(base), snapshot.Field
				base.Headers = base.Headers.Clone()
				if base.Headers == nil {
					base.Headers = make(map[string][]string)
				}
				base.Headers.Set(OutputLimitSnapshotHeader, response.Headers.Get(OutputLimitSnapshotHeader))
			}
		}
		if current, ok := errors.AsType[*IncompleteGeneration](callErr); ok && current.Reason == "missing_finish_reason" && !settings.RequireFinishReason {
			callErr = nil
			incomplete = nil
		}
		if callErr == nil && text.Len() > 0 && response != nil && len(response.Choices) > 0 && len(response.Choices[0].Message.ToolCalls) > 0 {
			callErr = &IncompleteGeneration{Reason: "unexpected_continuation_tools", Response: response}
		}
		if callErr == nil {
			callErr = CheckGeneration(response, settings.RequireFinishReason)
		}
		blank := !terminalGeneration(ctx) && callErr == nil && response != nil && len(response.Choices) > 0 && strings.TrimSpace(response.Choices[0].Message.TextContent()) == "" && len(response.Choices[0].Message.ToolCalls) == 0
		if blank {
			callErr = &IncompleteGeneration{Reason: "empty_response", Response: response, Cause: ErrEmptyResponse}
		}
		reason := "completed"
		if errors.As(callErr, &incomplete) {
			reason = incomplete.Reason
		} else if callErr != nil {
			reason = "request_failed"
		}
		action := ""
		canRetry := attempt < settings.MaxAttempts && ctx.Err() == nil && incomplete != nil
		if canRetry && reason == "empty_response" && text.Len() == 0 && (settings.SafeReplay || legacyEmptyRecovery) {
			action = "retry_empty"
		}
		if canRetry && settings.SafeReplay && (reason == "output_limit" || reason == "stream_interrupted" || reason == "missing_finish_reason" || reason == "invalid_tool_arguments") && response != nil && len(response.Choices) > 0 {
			message := response.Choices[0].Message
			if len(message.ToolCalls) > 0 || request.OutputSchema != nil || generationStructured(ctx) {
				if !regenerated {
					action = "regenerate"
				}
			} else if part := message.TextContent(); strings.TrimSpace(part) != "" && !strings.HasSuffix(text.String(), part) {
				action = "continue_text"
			}
		}
		if observer != nil && (reason != "completed" || attempt > 0) {
			event := GenerationAttempt{ID: id, Index: attempt, Reason: reason, Action: action, Response: response}
			if response != nil {
				event.Model = response.Model
				event.Usage = response.Usage
				event.ReasoningTokens = reportedReasoningTokens(response)
				if len(response.Choices) > 0 {
					event.Content = response.Choices[0].Message.TextContent()
					event.ToolCalls = response.Choices[0].Message.ToolCalls
				}
			}
			observer(event)
		}
		if callErr == nil {
			if text.Len() > 0 {
				response.Choices[0].Message.Content = text.String() + response.Choices[0].Message.TextContent()
				response.Choices[0].Message.ResponseOutput = nil
			}
			lastPrompt := response.Usage.PromptTokens
			response.Headers = response.Headers.Clone()
			if response.Headers == nil {
				response.Headers = make(map[string][]string)
			}
			response.Headers.Set("X-Q-Last-Prompt-Tokens", fmt.Sprint(lastPrompt))
			response.Usage = total
			return response, nil
		}
		if action == "" {
			if incomplete != nil {
				copy := *incomplete
				copy.Settings = &settings
				copy.Limit, copy.Attempts = max(copy.Limit, outputLimit(base)), attempt+1
				if response != nil {
					partial := *response
					partial.Usage = total
					if text.Len() > 0 && len(partial.Choices) > 0 {
						partial.Choices = append([]Choice(nil), partial.Choices...)
						partial.Choices[0].Message.Content = text.String() + partial.Choices[0].Message.TextContent()
					}
					copy.Response = &partial
				}
				return nil, &copy
			}
			return response, callErr
		}
		request = base
		request.Messages = append([]Message(nil), base.Messages...)
		request.ConversationID = ""
		switch action {
		case "continue_text":
			text.WriteString(response.Choices[0].Message.TextContent())
			request.Tools, request.ToolChoice = nil, ToolChoiceNone
			request.Messages = append(request.Messages, Message{Role: RoleAssistant, Content: text.String()}, Message{Role: RoleUser, Content: "Continue the unfinished answer from its last word. Do not repeat earlier text. Complete the remaining answer concisely. Do not call tools."})
		case "regenerate":
			regenerated = true
			text.Reset()
			request.Messages = append(request.Messages, Message{Role: RoleUser, Content: "The previous generation ended before completion and none of its proposed tools were executed. Produce a complete, more concise result within the same output limit. Use small tool arguments or artifact references. Regenerate complete JSON; do not continue a partial JSON fragment."})
		}
	}
}

// RefusalText reads the native Chat refusal field, which older compatible
// message types may omit. It is never treated as an empty-answer retry.
func RefusalText(raw json.RawMessage) string {
	var body struct {
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Refusal string `json:"refusal"`
			} `json:"message"`
			Delta struct {
				Refusal string `json:"refusal"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &body) != nil || len(body.Choices) == 0 {
		return ""
	}
	for _, choice := range body.Choices {
		if choice.Index == 0 {
			return choice.Message.Refusal + choice.Delta.Refusal
		}
	}
	return ""
}

func reportedReasoningTokens(response *ChatResponse) *int {
	var raw struct {
		Usage struct {
			Chat *struct {
				Tokens *int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
			Responses *struct {
				Tokens *int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(response.Raw, &raw) != nil {
		return nil
	}
	if raw.Usage.Chat != nil {
		return raw.Usage.Chat.Tokens
	}
	if raw.Usage.Responses != nil {
		return raw.Usage.Responses.Tokens
	}
	return nil
}

type structuredGenerationKey struct{}
type terminalGenerationKey struct{}

func terminalGeneration(ctx context.Context) bool {
	value, _ := ctx.Value(terminalGenerationKey{}).(bool)
	return value
}

func WithStructuredGeneration(ctx context.Context) context.Context {
	return context.WithValue(ctx, structuredGenerationKey{}, true)
}
func generationStructured(ctx context.Context) bool {
	value, _ := ctx.Value(structuredGenerationKey{}).(bool)
	return value
}

func outputLimit(request ChatRequest) int {
	limit := 0
	if request.MaxCompletionTokens != nil {
		limit = *request.MaxCompletionTokens
	}
	if request.MaxTokens != nil {
		if limit <= 0 || *request.MaxTokens < limit {
			limit = *request.MaxTokens
		}
	}
	return limit
}

// ApplyOutputLimit selects the smaller explicit limit and sends one Chat field.
func ApplyOutputLimit(request ChatRequest, configured int, field string) ChatRequest {
	limit := outputLimit(request)
	if configured > 0 && (limit <= 0 || configured < limit) {
		limit = configured
	}
	if limit <= 0 {
		return request
	}
	request.MaxTokens, request.MaxCompletionTokens = nil, nil
	if field == "max_tokens" {
		request.MaxTokens = &limit
	} else {
		request.MaxCompletionTokens = &limit
	}
	return request
}

// SumUsage aggregates logical generation attempts; transport recorders still
// receive the individual usage records, never this aggregate.
func SumUsage(a, b Usage) Usage {
	a.PromptTokens += b.PromptTokens
	a.CompletionTokens += b.CompletionTokens
	a.TotalTokens += b.TotalTokens
	if b.PromptDetails != nil {
		next := TokenDetails{}
		if a.PromptDetails != nil {
			next = *a.PromptDetails
		}
		next.CachedTokens += b.PromptDetails.CachedTokens
		next.CacheWriteTokens += b.PromptDetails.CacheWriteTokens
		a.PromptDetails = &next
	}
	return a
}

// ContextUsage avoids calibrating memory against the sum of recovery prompts.
func ContextUsage(response *ChatResponse) Usage {
	if response == nil {
		return Usage{}
	}
	usage := response.Usage
	if n, err := strconv.Atoi(response.Headers.Get("X-Q-Last-Prompt-Tokens")); err == nil {
		usage.PromptTokens = n
	}
	return usage
}
