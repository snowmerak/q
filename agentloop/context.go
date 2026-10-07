package agentloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
)

// AgentContextCompaction transfers a loop-local compaction to the embedding
// host without changing the full transcript.
type AgentContextCompaction struct {
	Plan    memory.Plan
	Summary string
}

// Context holds loop-local conversation memory and compaction policy.
type Context struct {
	manager *memory.Manager
	extra   map[string]any
}

// NewContext creates loop-local memory for a test or embedding host.
func NewContext(policy memory.Policy, initial []client.Message, tools []client.Tool) *Context {
	return newAgentLoopContext(policy, initial, tools)
}

func newAgentLoopContext(policy memory.Policy, initial []client.Message, tools []client.Tool) *Context {
	manager := memory.New(policy, initial)
	if toolTokens := memory.CountTools(tools); toolTokens > 0 {
		local := manager.LocalEstimate()
		manager.ObserveUsage(local+toolTokens, local)
	}
	return &Context{manager: manager}
}

func (c *Context) Messages() []client.Message {
	if c == nil || c.manager == nil {
		return nil
	}
	return c.manager.Messages()
}

// PredictedTokens reports the current estimate for the next request.
func (c *Context) PredictedTokens() int {
	if c == nil || c.manager == nil {
		return 0
	}
	return c.manager.PredictedTokens()
}

func (c *Context) Append(messages ...client.Message) {
	if c == nil || c.manager == nil {
		return
	}
	for _, message := range messages {
		c.manager.Append(message)
	}
}

func (c *Context) CallMemoryTool(call client.ToolCall) (client.ToolResult, bool) {
	if c == nil || c.manager == nil {
		return client.ToolResult{}, false
	}
	// The delta becomes authoritative when its result is appended. In
	// particular, pre-append compaction must not include an unpersisted update.
	return c.manager.Copy().CallMemoryTool(call)
}

func (c *Context) Observe(usage client.Usage, requestEstimate int) {
	if c == nil || c.manager == nil {
		return
	}
	c.manager.ObserveUsage(usage.PromptTokens, requestEstimate)
}

func (c *Context) ShouldCompact() bool {
	return c != nil && c.manager != nil && c.manager.ShouldCompact()
}

func loopRetention() memory.Retention {
	// Lifecycle exchanges and pending callbacks must survive a fresh thread.
	return memory.Retention{
		PreserveInstructions: true,
		PreserveToolNames:    []string{askToUserToolName, taskStartToolName},
		AllowTargetGrowth:    true, SummarizeOversizedRecent: true,
		ContinuationMessage: "keep going",
	}
}

// CompactBeforeAppend checks the combined size before mutating history. The
// returned plan contains only existing history; callers emit it before the
// incoming message so the persisted host context follows the same order.
func (c *Context) CompactBeforeAppend(ctx context.Context, configuredClient ChatClient, modelID, reasoningEffort string, messages ...client.Message) (*AgentContextCompaction, error) {
	if c == nil || c.manager == nil || !c.manager.ShouldCompactAfterAppend(messages...) {
		return nil, nil
	}
	plan, err := c.manager.PlanBeforeAppend(loopRetention(), messages...)
	if err != nil {
		return nil, fmt.Errorf("agent loop: reserve context for incoming history: %w", err)
	}
	return c.compact(ctx, configuredClient, modelID, reasoningEffort, plan)
}

// CompactIfNeeded summarizes completed tool-loop history on a fresh provider
// conversation. Applying the returned plan to the session memory keeps the
// durable request context synchronized with this loop-local owner.
func (c *Context) CompactIfNeeded(
	ctx context.Context,
	configuredClient ChatClient,
	modelID string,
	reasoningEffort string,
) (*AgentContextCompaction, error) {
	if !c.ShouldCompact() {
		return nil, nil
	}
	plan, err := c.manager.PlanWithRetention(loopRetention())
	if err != nil {
		return nil, fmt.Errorf("agent loop: plan context compaction: %w", err)
	}
	return c.compact(ctx, configuredClient, modelID, reasoningEffort, plan)
}

func (c *Context) compact(ctx context.Context, configuredClient ChatClient, modelID, reasoningEffort string, plan memory.Plan) (*AgentContextCompaction, error) {
	checkpointText, ready := plan.CheckpointWithoutModel()
	if !ready {
		if configuredClient == nil {
			return nil, errors.New("agent loop: context compaction requires a model client")
		}
		response, err := chatWithEmptyResponseRecovery(ctx, configuredClient, client.ChatRequest{
			Model: modelID, Messages: plan.RequestMessages(),
			ReasoningEffort: reasoningEffort, Extra: c.extra,
		})
		if err != nil {
			return nil, fmt.Errorf("agent loop: compact context: %w", err)
		}
		if response == nil || len(response.Choices) == 0 {
			return nil, errors.New("agent loop: compact context returned no choices")
		}
		checkpointText = response.Choices[0].Message.TextContent()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	next, checkpoint, err := c.manager.CheckpointCopy(plan, checkpointText)
	if err != nil {
		return nil, fmt.Errorf("agent loop: compact context: %w", err)
	}
	c.manager = next
	return &AgentContextCompaction{Plan: plan, Summary: checkpoint}, nil
}
