package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/snowmerak/q/diagnostics"
)

// modelCallLog describes one Q client call, including the full stream
// lifetime. It stores metadata only; prompts, chunks and headers stay private.
type modelCallLog struct {
	ctx             context.Context
	logger          *slog.Logger
	started         time.Time
	completedAt     time.Time
	once            sync.Once
	mu              sync.Mutex
	model           string
	usage           Usage
	reported        bool
	timing          ModelCallTiming
	output          modelOutputTiming
	observe         func(ModelCallTiming)
	nativeResponses bool
}

func startModelCall(ctx context.Context, model, apiMode string, streaming bool) *modelCallLog {
	logger := diagnostics.Logger(ctx)
	observe, _ := ctx.Value(modelCallObserverKey{}).(func(ModelCallTiming))
	if !streaming {
		observe = nil
	}
	if observe == nil && !logger.Enabled(ctx, slog.LevelInfo) && !logger.Enabled(ctx, slog.LevelError) {
		return nil
	}
	timing := ModelCallTiming{ID: uuid.NewString(), Model: model, Role: usageRole(ctx)}
	logger = logger.With("model_call_id", timing.ID, "requested_model", model,
		"api_mode", apiMode, "streaming", streaming, "usage_role", timing.Role)
	call := &modelCallLog{ctx: ctx, logger: logger, started: time.Now(), timing: timing, observe: observe, nativeResponses: apiMode == "responses"}
	logger.Info("model.call.started")
	return call
}

func (call *modelCallLog) observeResponse(response *ChatResponse) {
	if call == nil || response == nil {
		return
	}
	call.observeUsage(response.Model, response.Usage)
}

func (call *modelCallLog) observeUsage(model string, usage Usage) {
	if call == nil {
		return
	}
	call.mu.Lock()
	defer call.mu.Unlock()
	call.model, call.usage = model, usage
	call.reported = usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0
}

func (call *modelCallLog) observeChunk(chunk *ChatChunk, received time.Time) {
	if call == nil || chunk == nil {
		return
	}
	call.mu.Lock()
	defer call.mu.Unlock()
	if chunk.Model != "" {
		call.model = chunk.Model
	}
	if chunk.Usage != nil {
		call.usage, call.reported = *chunk.Usage, true
	}
	if !call.nativeResponses && chatChunkHasOutput(chunk) {
		call.output.observe(received)
	}
}

func (call *modelCallLog) observeOutput(received time.Time) {
	if call != nil {
		call.mu.Lock()
		call.output.observe(received)
		call.mu.Unlock()
	}
}

func (call *modelCallLog) observeCompletion(received time.Time) {
	if call != nil {
		call.mu.Lock()
		if call.completedAt.IsZero() {
			call.completedAt = received
		}
		call.mu.Unlock()
	}
}

func (call *modelCallLog) finish(err error, status string) {
	if call == nil {
		return
	}
	call.once.Do(func() {
		level := slog.LevelInfo
		if errors.Is(err, context.Canceled) || errors.Is(call.ctx.Err(), context.Canceled) {
			status = "cancelled"
		} else if err != nil {
			level, status = slog.LevelError, "failed"
		}
		call.mu.Lock()
		ended := time.Now()
		if status == "completed" && !call.completedAt.IsZero() {
			ended = call.completedAt
		}
		timing := call.timing
		timing.Status, timing.DurationMS = status, ended.Sub(call.started).Milliseconds()
		if call.model != "" {
			timing.Model = call.model
		}
		attrs := []any{"status", status, "duration_ms", timing.DurationMS,
			"response_model", call.model, "usage_reported", call.reported}
		if call.reported {
			outputTokens := call.usage.CompletionTokens
			timing.OutputTokens = &outputTokens
			attrs = append(attrs, "prompt_tokens", call.usage.PromptTokens,
				"completion_tokens", call.usage.CompletionTokens, "total_tokens", call.usage.TotalTokens)
		}
		call.output.apply(&timing, call.started, ended)
		call.mu.Unlock()
		if timing.TTFTMS != nil {
			attrs = append(attrs, "ttft_ms", *timing.TTFTMS)
		}
		if timing.AfterFirstMS != nil {
			attrs = append(attrs, "after_first_ms", *timing.AfterFirstMS)
		}
		if timing.MaxOutputGapMS != nil {
			attrs = append(attrs, "max_output_gap_ms", *timing.MaxOutputGapMS)
		}
		if timing.OutputTokensPerSec != nil {
			attrs = append(attrs, "output_tokens_per_second", *timing.OutputTokensPerSec)
		}
		if err != nil {
			attrs = append(attrs, "error_type", fmt.Sprintf("%T", err))
			if apiErr, ok := errors.AsType[*APIError](err); ok {
				attrs = append(attrs, "http_status", apiErr.StatusCode)
			}
		}
		call.logger.Log(call.ctx, level, "model.call.finished", attrs...)
		if call.observe != nil {
			call.observe(timing)
		}
	})
}

type modelLogStream struct {
	Stream
	call *modelCallLog
}

func (stream *modelLogStream) Recv() (*ChatChunk, error) {
	chunk, err := stream.Stream.Recv()
	stream.call.observeChunk(chunk, time.Now())
	if errors.Is(err, io.EOF) {
		stream.call.finish(nil, "completed")
	} else if err != nil {
		stream.call.finish(err, "failed")
	}
	return chunk, err
}

func (stream *modelLogStream) Close() error {
	err := stream.Stream.Close()
	// Closing before EOF is not evidence that the provider completed the call.
	stream.call.finish(err, "closed")
	return err
}

type modelLogHeaderStream struct {
	*modelLogStream
	headers ResponseHeaderer
}

func (stream *modelLogHeaderStream) ResponseHeaders() http.Header {
	return stream.headers.ResponseHeaders()
}
