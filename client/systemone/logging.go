package systemone

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/snowmerak/q/diagnostics"
)

// Decision metadata follows the same model-call event family as chat, while
// native states, questions, answers and idempotency keys remain private.
func logDecision(ctx context.Context, model string) func(*Result, error) {
	logger := diagnostics.Logger(ctx)
	if !logger.Enabled(ctx, slog.LevelInfo) && !logger.Enabled(ctx, slog.LevelError) {
		return func(*Result, error) {}
	}
	logger = logger.With("model_call_id", uuid.NewString(), "requested_model", model,
		"api_mode", "system_one", "streaming", false, "usage_role", "system_one")
	started := time.Now()
	logger.Info("model.call.started")
	return func(result *Result, err error) {
		level, status := slog.LevelInfo, "completed"
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			status = "cancelled"
		} else if err != nil {
			level, status = slog.LevelError, "failed"
		}
		attrs := []any{"status", status, "duration_ms", time.Since(started).Milliseconds(), "usage_reported", result != nil && result.Usage != nil}
		if result != nil {
			attrs = append(attrs, "response_model", result.Model)
			if result.Usage != nil {
				if result.Usage.InputTokens != nil {
					attrs = append(attrs, "prompt_tokens", *result.Usage.InputTokens)
				}
				if result.Usage.OutputTokens != nil {
					attrs = append(attrs, "completion_tokens", *result.Usage.OutputTokens)
				}
			}
		}
		if err != nil {
			attrs = append(attrs, "error_type", fmt.Sprintf("%T", err))
			if apiErr, ok := errors.AsType[*APIError](err); ok {
				attrs = append(attrs, "http_status", apiErr.StatusCode)
			}
		}
		logger.Log(ctx, level, "model.call.finished", attrs...)
	}
}
