package builtin

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// LearnInput is intentionally empty. The active q session owns the durable
// conversation cursor and treats the tool call itself as an explicit segment
// boundary.
type LearnInput struct{}

type LearnOutput struct {
	Enqueued bool `json:"enqueued"`
}

// RegisterLearning adds tools that require an active q chat session. They are
// deliberately separate from Register so the standalone q-mcp server cannot
// advertise acknowledgements for session state it does not own.
func RegisterLearning(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "learn",
		Description: "Close the current learning segment and enqueue it for asynchronous Thinker extraction. Returns immediately; an empty segment is a no-op.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ LearnInput) (*mcp.CallToolResult, LearnOutput, error) {
		return structuredTextResult(LearnOutput{Enqueued: true})
	})
}
