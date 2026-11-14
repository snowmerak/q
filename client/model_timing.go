package client

import (
	"context"
	"encoding/json"
	"time"
)

// ModelCallTiming measures a Q client call, including any transport retries.
// TTFT is the arrival of the first output delta at Q's client boundary.
type ModelCallTiming struct {
	ID                 string   `json:"model_call_id"`
	Model              string   `json:"model"`
	Role               string   `json:"usage_role"`
	Status             string   `json:"status"`
	DurationMS         int64    `json:"duration_ms"`
	TTFTMS             *int64   `json:"ttft_ms,omitempty"`
	AfterFirstMS       *int64   `json:"after_first_ms,omitempty"`
	MaxOutputGapMS     *int64   `json:"max_output_gap_ms,omitempty"`
	InputTokens        *int     `json:"input_tokens,omitempty"`
	OutputTokens       *int     `json:"output_tokens,omitempty"`
	TotalTokens        *int     `json:"total_tokens,omitempty"`
	CachedTokens       *int     `json:"cached_tokens,omitempty"`
	CacheHitRate       *float64 `json:"cache_hit_rate,omitempty"`
	OutputTokensPerSec *float64 `json:"output_tokens_per_second,omitempty"`
}

type modelCallObserverKey struct{}

// WithModelCallObserver observes completed or stopped model streams once per call.
// The observer runs synchronously and may be called by concurrent streams.
func WithModelCallObserver(ctx context.Context, observe func(ModelCallTiming)) context.Context {
	return context.WithValue(ctx, modelCallObserverKey{}, observe)
}

type modelOutputTiming struct {
	first time.Time
	last  time.Time
	count int
	gap   time.Duration
}

func (output *modelOutputTiming) observe(at time.Time) {
	if output.count == 0 {
		output.first = at
	} else {
		output.gap = max(output.gap, at.Sub(output.last))
	}
	output.last = at
	output.count++
}

func (output modelOutputTiming) apply(timing *ModelCallTiming, started, ended time.Time) {
	if output.count == 0 {
		return
	}
	ttft := output.first.Sub(started).Milliseconds()
	timing.TTFTMS = &ttft
	if output.count > 1 {
		gap := output.gap.Milliseconds()
		timing.MaxOutputGapMS = &gap
	}
	if timing.Status != "completed" {
		return
	}
	afterFirst := ended.Sub(output.first)
	ms := afterFirst.Milliseconds()
	timing.AfterFirstMS = &ms
	if output.count > 1 && timing.OutputTokens != nil && *timing.OutputTokens > 0 && afterFirst > 0 {
		rate := float64(*timing.OutputTokens) / afterFirst.Seconds()
		timing.OutputTokensPerSec = &rate
	}
}

func chatChunkHasOutput(chunk *ChatChunk) bool {
	for _, choice := range chunk.Choices {
		if choice.Delta == nil {
			continue
		}
		if choice.Delta.TextContent() != "" {
			return true
		}
		for _, call := range choice.Delta.ToolCalls {
			if call.Function.Name != "" || call.Function.Arguments != "" {
				return true
			}
		}
	}
	if len(chunk.Choices) == 0 || len(chunk.Raw) == 0 {
		return false
	}
	var envelope struct {
		Choices []struct {
			Delta struct {
				ReasoningContent string       `json:"reasoning_content"`
				Reasoning        string       `json:"reasoning"`
				Thinking         string       `json:"thinking"`
				Refusal          string       `json:"refusal"`
				FunctionCall     FunctionCall `json:"function_call"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(chunk.Raw, &envelope) != nil {
		return false
	}
	for _, choice := range envelope.Choices {
		delta := choice.Delta
		if delta.ReasoningContent != "" || delta.Reasoning != "" || delta.Thinking != "" || delta.Refusal != "" ||
			delta.FunctionCall.Name != "" || delta.FunctionCall.Arguments != "" {
			return true
		}
	}
	return false
}
