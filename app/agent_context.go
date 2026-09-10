package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
)

// agentContextCompaction transfers a loop-local compaction to the owning TUI
// or ACP session memory without changing the full transcript.
type agentContextCompaction struct {
	Plan    memory.Plan
	Summary string
}

type agentLoopContext struct {
	manager *memory.Manager
}

func (m *model) applyAgentContextCompaction(compaction agentContextCompaction) error {
	if m.memory == nil {
		m.memory = memory.New(memoryPolicy(m.activeConfig()), nil)
	}
	checkpoint, err := m.memory.ApplyCheckpoint(compaction.Plan, compaction.Summary)
	if err != nil {
		return err
	}
	m.conversationID = ""
	m.archiveSummary(checkpoint)
	return nil
}

func newAgentLoopContext(policy memory.Policy, initial []client.Message, tools []client.Tool) *agentLoopContext {
	manager := memory.New(policy, initial)
	if toolTokens := memory.CountTools(tools); toolTokens > 0 {
		local := manager.LocalEstimate()
		manager.ObserveUsage(local+toolTokens, local)
	}
	return &agentLoopContext{manager: manager}
}

func (c *agentLoopContext) Messages() []client.Message {
	if c == nil || c.manager == nil {
		return nil
	}
	return c.manager.Messages()
}

func (c *agentLoopContext) Append(messages ...client.Message) {
	if c == nil || c.manager == nil {
		return
	}
	for _, message := range messages {
		c.manager.Append(message)
	}
}

func (c *agentLoopContext) Observe(usage client.Usage, requestEstimate int) {
	if c == nil || c.manager == nil {
		return
	}
	c.manager.ObserveUsage(usage.PromptTokens, requestEstimate)
}

func (c *agentLoopContext) ShouldCompact() bool {
	return c != nil && c.manager != nil && c.manager.ShouldCompact()
}

// CompactIfNeeded summarizes completed tool-loop history on a fresh provider
// conversation. Applying the returned plan to the session memory keeps the
// durable request context synchronized with this loop-local owner.
func (c *agentLoopContext) CompactIfNeeded(
	ctx context.Context,
	configuredClient chatClient,
	modelID string,
	reasoningEffort string,
) (*agentContextCompaction, error) {
	if !c.ShouldCompact() {
		return nil, nil
	}
	if configuredClient == nil {
		return nil, errors.New("agent loop: context compaction requires a model client")
	}
	plan, err := c.manager.PlanWithRetention(memory.Retention{
		PreserveInstructions:     true,
		PreserveToolNames:        []string{askToUserToolName},
		AllowTargetGrowth:        true,
		SummarizeOversizedRecent: true,
	})
	if err != nil {
		return nil, fmt.Errorf("agent loop: plan context compaction: %w", err)
	}
	maxTokens := plan.OutputBudget
	response, err := chatWithConversationRecovery(ctx, configuredClient, client.ChatRequest{
		Model: modelID, Messages: plan.RequestMessages(), MaxCompletionTokens: &maxTokens,
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
	return &agentContextCompaction{Plan: plan, Summary: checkpoint}, nil
}
