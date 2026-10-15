// Package diagnostics provides scoped diagnostic logging and request correlation.
// It never replaces the process-wide logger.
package diagnostics

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
)

type requestKey struct{}
type loggerKey struct{}

// WithRequestID sets a canonical UUID. An empty id preserves the current ID;
// a missing or invalid ID is replaced with a new random UUID.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		id = RequestID(ctx)
	}
	if parsed, err := uuid.Parse(id); err != nil || len(id) != 36 {
		id = uuid.NewString()
	} else {
		id = parsed.String()
	}
	return context.WithValue(ctx, requestKey{}, id)
}

// WithAttrs scopes diagnostic fields without copying request IDs into the logger.
// Logger adds the current context's request ID when emitting an event.
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	logger, _ := ctx.Value(loggerKey{}).(*slog.Logger)
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return WithLogger(ctx, slog.New(logger.Handler().WithAttrs(attrs)))
}

// RequestID returns the request that caused this execution, when available.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestKey{}).(string)
	return id
}

// WithLogger attaches a logger to one execution tree.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}

// Logger returns a request-scoped logger, or a disabled logger for embedders.
func Logger(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if logger, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && logger != nil {
			if id := RequestID(ctx); id != "" {
				return logger.With("request_id", id)
			}
			return logger
		}
	}
	return slog.New(slog.DiscardHandler)
}

// Inherit copies diagnostic values while preserving the parent's cancellation.
// Detached jobs use this instead of retaining a browser request's lifetime.
func Inherit(parent, source context.Context) context.Context {
	if id := RequestID(source); id != "" {
		parent = WithRequestID(parent, id)
	}
	if source != nil {
		if logger, ok := source.Value(loggerKey{}).(*slog.Logger); ok && logger != nil {
			parent = WithLogger(parent, logger)
		}
	}
	return parent
}
