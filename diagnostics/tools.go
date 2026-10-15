package diagnostics

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ToolIdentity distinguishes ordinary and nested calls, including repeated call
// IDs from different agents. Names and IDs are metadata, never tool payloads.
type ToolIdentity struct {
	Name, CallID, Agent, TaskID, ParentID string
}

// ToolCalls projects execution events into INFO diagnostics. Timing starts when
// the host reports the call, so durations can include queue and event delivery.
// At most 1024 outstanding calls retain timestamps; others still emit events.
type ToolCalls struct {
	prefix string
	mu     sync.Mutex
	active map[ToolIdentity]time.Time
}

func NewToolCalls(prefix string) *ToolCalls {
	return &ToolCalls{prefix: prefix, active: make(map[ToolIdentity]time.Time)}
}

func (calls *ToolCalls) Started(ctx context.Context, identity ToolIdentity) {
	calls.mu.Lock()
	if _, found := calls.active[identity]; found || len(calls.active) < 1024 {
		calls.active[identity] = time.Now()
	}
	calls.mu.Unlock()
	toolLogger(ctx, identity).Info(calls.prefix + ".started")
}

func (calls *ToolCalls) Finished(ctx context.Context, identity ToolIdentity, isError bool) {
	calls.mu.Lock()
	started, found := calls.active[identity]
	delete(calls.active, identity)
	calls.mu.Unlock()
	level, status := slog.LevelInfo, "completed"
	if isError {
		level, status = slog.LevelWarn, "failed"
	}
	attrs := []any{"status", status, "is_error", isError}
	if found {
		attrs = append(attrs, "duration_ms", time.Since(started).Milliseconds())
	}
	toolLogger(ctx, identity).Log(ctx, level, calls.prefix+".finished", attrs...)
}

func toolLogger(ctx context.Context, identity ToolIdentity) *slog.Logger {
	logger := Logger(ctx).With("tool", identity.Name, "call_id", identity.CallID)
	if identity.Agent != "" {
		logger = logger.With("agent", identity.Agent)
	}
	if identity.TaskID != "" {
		logger = logger.With("task_id", identity.TaskID)
	}
	if identity.ParentID != "" {
		logger = logger.With("parent_id", identity.ParentID)
	}
	return logger
}
