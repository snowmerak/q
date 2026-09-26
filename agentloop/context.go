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

func (c *Context) Observe(usage client.Usage, requestEstimate int) {
	if c == nil || c.manager == nil {
		return
	}
	c.manager.ObserveUsage(usage.PromptTokens, requestEstimate)
}

func (c *Context) ShouldCompact() bool {
	return c != nil && c.manager != nil && c.manager.ShouldCompact()
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
	if configuredClient == nil {
		return nil, errors.New("agent loop: context compaction requires a model client")
	}
	plan, err := c.manager.PlanWithRetention(memory.Retention{
		PreserveInstructions: true,
		// The lifecycle state is host-owned. Keep its opening exchange exact so
		// a compacted model context does not try to start the active task again.
		PreserveToolNames:        []string{askToUserToolName, taskStartToolName},
		AllowTargetGrowth:        true,
		SummarizeOversizedRecent: true,
		// Strict chat templates need a user turn even when the latest real one was summarized.
		ContinuationMessage: "keep going",
	})
	if err != nil {
		return nil, fmt.Errorf("agent loop: plan context compaction: %w", err)
	}
	response, err := chatWithEmptyResponseRecovery(ctx, configuredClient, client.ChatRequest{
		Model: modelID, Messages: plan.RequestMessages(),
		ReasoningEffort: reasoningEffort,
	})
	if err != nil {
		return nil, fmt.Errorf("agent loop: compact context: %w", err)
	}
	if response == nil || len(response.Choices) == 0 {
		return nil, errors.New("agent loop: compact context returned no choices")
	}
	checkpoint, err := c.manager.ApplyCheckpoint(plan, response.Choices[0].Message.TextContent())
	if err != nil {
		return nil, fmt.Errorf("agent loop: compact context: %w", err)
	}
	return &AgentContextCompaction{Plan: plan, Summary: checkpoint}, nil
}
