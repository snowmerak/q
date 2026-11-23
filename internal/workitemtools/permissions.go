package workitemtools

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
)

type delegatedSessionKey struct{}

// WithDelegatedSession carries a host-generated identity to Q's native tools.
func WithDelegatedSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, delegatedSessionKey{}, sessionID)
}

const DelegatedSessionMetaKey = "q/delegated-session"

func DelegatedSession(ctx context.Context) string {
	id, _ := ctx.Value(delegatedSessionKey{}).(string)
	return id
}

// NativeDelegatedSession is installed only on Q's private in-memory transport.
// The stdio bridge binds the identity from its host-selected startup arguments.
func NativeDelegatedSession(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if call, ok := req.(*mcp.CallToolRequest); ok && call.Params != nil {
			if id, ok := call.Params.Meta[DelegatedSessionMetaKey].(string); ok && id != "" {
				ctx = WithDelegatedSession(ctx, id)
			}
		}
		return next(ctx, method, req)
	}
}

func (b Binding) caller(ctx context.Context) Binding {
	if sessionID := DelegatedSession(ctx); sessionID != "" {
		return Binding{WorkspaceID: b.WorkspaceID, SessionID: sessionID}
	}
	return b
}

// IsCollaborationTool identifies the common project tools exposed to workers.
// Each write still checks the caller's authority for the target at execution.
func IsCollaborationTool(name string) bool {
	switch name {
	case "search_work", "post_get", "issue_list", "issue_get", "ticket_list", "ticket_get",
		"issue_create", "issue_reply", "issue_update", "ticket_create", "ticket_reply", "ticket_update", "post_update":
		return true
	default:
		return false
	}
}

func (b Binding) actor() string {
	if b.SessionID == "" {
		return "agent"
	}
	return "agent/" + b.SessionID
}

func (b Binding) owns(post workitems.Post) bool {
	return b.SessionID != "" && post.Author == b.actor()
}

func (b Binding) canPlan() bool {
	return b.Manager || b.Coordinator
}

func (b Binding) requireItemOwner(ctx context.Context, c *library.Client, item workitems.Item) error {
	if b.canPlan() {
		return nil
	}
	body, err := c.ReadContent(ctx, item.BodyPostID, "", 1)
	if err != nil {
		return err
	}
	if b.owns(body.Post) {
		return nil
	}
	return errors.New("item changes require the issuer, Coordinator, or Integrator; use a reply to contribute to this thread")
}
