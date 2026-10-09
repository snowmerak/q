// Package toolbatch executes ordinary tool calls concurrently between loop-owned
// barriers. The loop remains the sole owner of history, events and persistence.
package toolbatch

import (
	"context"
	"sync"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
)

const maximumParallelCalls = 4

// IsLoopTool identifies calls whose effects must be committed before later
// calls start. Role-specific loops can add their own barriers.
func IsLoopTool(call client.ToolCall) bool {
	if memory.IsMemoryTool(call.Function.Name) {
		return true
	}
	switch call.Function.Name {
	case "task_start", "task_complete", "ask_to_user", "submit_brief", "submit_plan", "review_task":
		return true
	default:
		return false
	}
}

// End returns the end of the ordinary segment starting at index. A barrier
// belongs to a segment of its own and must be handled by the caller.
func End(calls []client.ToolCall, index int, barrier func(client.ToolCall) bool) int {
	if barrier(calls[index]) {
		return index + 1
	}
	end := index + 1
	for end < len(calls) && !barrier(calls[end]) {
		end++
	}
	return end
}

type outcome struct {
	result client.ToolResult
	err    error
}

// Batch is driven sequentially by one loop. Execute may be called concurrently;
// Before runs on the loop goroutine before any calls in a segment start.
// Call waits for the whole segment, so later barriers cannot overtake workers.
type Batch struct {
	calls   []client.ToolCall
	barrier func(client.ToolCall) bool
	execute func(context.Context, int, client.ToolCall) (client.ToolResult, error)
	results []outcome
	ready   []bool
	Before  func(int, client.ToolCall) error
}

// New prepares lazy segments. It starts no calls until Call is invoked, after
// the owning loop has persisted the assistant turn and any preceding barriers.
func New(calls []client.ToolCall, barrier func(client.ToolCall) bool, execute func(context.Context, int, client.ToolCall) (client.ToolResult, error)) *Batch {
	return &Batch{calls: calls, barrier: barrier, execute: execute,
		results: make([]outcome, len(calls)), ready: make([]bool, len(calls))}
}

// Call executes the segment once and returns its indexed result. Results are
// retained by position rather than call ID, which may repeat across turns.
// Tool errors do not cancel sibling calls. Cancellation prevents queued calls
// from starting and joins active workers before the loop can release its runtime.
func (b *Batch) Call(ctx context.Context, index int) (client.ToolResult, error) {
	if !b.ready[index] {
		end := End(b.calls, index, b.barrier)
		for next := index; next < end; next++ {
			if err := ctx.Err(); err != nil {
				return client.ToolResult{}, err
			}
			if b.Before != nil {
				if err := b.Before(next, b.calls[next]); err != nil {
					return client.ToolResult{}, err
				}
			}
		}
		var workers sync.WaitGroup
		jobs := make(chan int)
		for range min(maximumParallelCalls, end-index) {
			workers.Go(func() {
				for next := range jobs {
					if err := ctx.Err(); err != nil {
						b.results[next].err = err
						continue
					}
					b.results[next].result, b.results[next].err = b.execute(ctx, next, b.calls[next])
				}
			})
		}
		for next := index; next < end; next++ {
			jobs <- next
		}
		close(jobs)
		workers.Wait()
		for next := index; next < end; next++ {
			b.ready[next] = true
		}
	}
	return b.results[index].result, b.results[index].err
}
