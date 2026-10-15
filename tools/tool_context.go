package tools

import (
	"context"
	"errors"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/diagnostics"
)

const toolContextMetaKey = "q/caller-context"

// Context values do not cross the in-process MCP transport. Keep the caller's
// model client local and attach only an opaque, call-lifetime reference to RPC.
type toolCallContexts struct {
	mu      sync.Mutex
	pending map[string]*toolCallContext
}

type toolCallContext struct {
	ctx     context.Context
	started bool
	done    chan struct{}
}

func (c *toolCallContexts) bind(ctx context.Context, params *mcp.CallToolParams) func() {
	id := client.NewUsageEventID()
	entry := &toolCallContext{ctx: ctx, done: make(chan struct{})}
	c.mu.Lock()
	if c.pending == nil {
		c.pending = make(map[string]*toolCallContext)
	}
	c.pending[id] = entry
	c.mu.Unlock()
	if params.Meta == nil {
		params.Meta = mcp.Meta{}
	}
	params.Meta[toolContextMetaKey] = id
	return func() {
		c.mu.Lock()
		delete(c.pending, id)
		started := entry.started
		c.mu.Unlock()
		// A cancelled MCP client can return before its handler. Join the
		// handler before releasing the borrowed caller model client.
		if started {
			<-entry.done
		}
	}
}

func (c *toolCallContexts) middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		params := req.GetParams().(*mcp.CallToolParamsRaw)
		id, _ := params.Meta[toolContextMetaKey].(string)
		if id == "" {
			return next(ctx, method, req)
		}
		c.mu.Lock()
		entry := c.pending[id]
		if entry != nil && !entry.started {
			entry.started = true
		} else {
			entry = nil
		}
		c.mu.Unlock()
		if entry == nil {
			return nil, errors.New("tools: caller context expired")
		}
		defer close(entry.done)
		parent := entry.ctx
		if err := parent.Err(); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(parent, cancel)
		defer stop()
		if deadline, ok := parent.Deadline(); ok {
			var cancelDeadline context.CancelFunc
			ctx, cancelDeadline = context.WithDeadline(ctx, deadline)
			defer cancelDeadline()
		}
		ctx = diagnostics.Inherit(ctx, parent)
		if model, ok := client.ToolModelFromContext(parent); ok {
			ctx = client.WithToolModel(ctx, model)
		}
		return next(ctx, method, req)
	}
}
