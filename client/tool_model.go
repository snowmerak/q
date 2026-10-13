package client

import (
	"context"
	"maps"
)

// ToolModel identifies the caller's model for isolated completions inside tools.
// The host owns Client and must keep it alive until tool calls return. Client
// must support concurrent calls; tool completions do not reuse conversation IDs.
type ToolModel struct {
	Client           ModelChatClient
	Model            string
	ReasoningEffort  string
	WorkingDirectory string
	Extra            map[string]any
	Role             string
}

type toolModelKey struct{}

// WithToolModel binds model settings to one execution context, not a shared
// runtime. Parallel agents can therefore use different models in the same tools.
func WithToolModel(ctx context.Context, model ToolModel) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	model.Extra = maps.Clone(model.Extra)
	return context.WithValue(ctx, toolModelKey{}, model)
}

// ToolModelFromContext returns the host-provided model for a tool call.
func ToolModelFromContext(ctx context.Context) (ToolModel, bool) {
	if ctx == nil {
		return ToolModel{}, false
	}
	model, ok := ctx.Value(toolModelKey{}).(ToolModel)
	model.Extra = maps.Clone(model.Extra)
	return model, ok && model.Client != nil
}
