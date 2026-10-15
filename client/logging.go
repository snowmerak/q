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
	ctx      context.Context
	logger   *slog.Logger
	started  time.Time
	once     sync.Once
	mu       sync.Mutex
	model    string
	usage    Usage
	reported bool
}

func startModelCall(ctx context.Context, model, apiMode string, streaming bool) *modelCallLog {
	logger := diagnostics.Logger(ctx)
	if !logger.Enabled(ctx, slog.LevelInfo) && !logger.Enabled(ctx, slog.LevelError) {
		return nil
	}
	logger = logger.With("model_call_id", uuid.NewString(), "requested_model", model,
		"api_mode", apiMode, "streaming", streaming, "usage_role", usageRole(ctx))
	call := &modelCallLog{ctx: ctx, logger: logger, started: time.Now()}
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

func (call *modelCallLog) observeChunk(chunk *ChatChunk) {
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
		attrs := []any{"status", status, "duration_ms", time.Since(call.started).Milliseconds(),
			"response_model", call.model, "usage_reported", call.reported}
		if call.reported {
			attrs = append(attrs, "prompt_tokens", call.usage.PromptTokens,
				"completion_tokens", call.usage.CompletionTokens, "total_tokens", call.usage.TotalTokens)
		}
		call.mu.Unlock()
		if err != nil {
			attrs = append(attrs, "error_type", fmt.Sprintf("%T", err))
			if apiErr, ok := errors.AsType[*APIError](err); ok {
				attrs = append(attrs, "http_status", apiErr.StatusCode)
			}
		}
		call.logger.Log(call.ctx, level, "model.call.finished", attrs...)
	})
}

type modelLogStream struct {
	Stream
	call *modelCallLog
}

func (stream *modelLogStream) Recv() (*ChatChunk, error) {
	chunk, err := stream.Stream.Recv()
	stream.call.observeChunk(chunk)
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
