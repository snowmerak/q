package terminalturn

import "context"

// InstructionPrefix introduces the model-facing instruction for a finished task.
const InstructionPrefix = "The host has accepted this terminal tool result. The task is finished. "

type contextKey struct{}

// Mark identifies the completion phase on calls made by FinishToolTurn.
func Mark(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, true)
}

// Active reports whether the generation belongs to a terminal tool turn.
func Active(ctx context.Context) bool {
	active, _ := ctx.Value(contextKey{}).(bool)
	return active
}
