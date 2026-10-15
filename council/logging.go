package council

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/snowmerak/q/diagnostics"
)

func beginStage(ctx context.Context, stage string, round int) (context.Context, func(error)) {
	ctx = diagnostics.WithAttrs(ctx, slog.String("stage", stage), slog.Int("round", round))
	logger := diagnostics.Logger(ctx)
	started := time.Now()
	logger.Info("council.stage.started")
	return ctx, func(err error) {
		level, status := slog.LevelInfo, "completed"
		if errors.Is(err, context.Canceled) {
			status = "cancelled"
		} else if err != nil {
			level, status = slog.LevelError, "failed"
		}
		logger.Log(ctx, level, "council.stage.finished", "status", status, "duration_ms", time.Since(started).Milliseconds(), "error", err)
	}
}

func memberContext(ctx context.Context, index int) context.Context {
	return diagnostics.WithAttrs(ctx, slog.String("seat", label(index)), slog.String("role", "member"))
}
