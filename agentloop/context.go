package agentloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
)

type loopContext struct {
	manager *memory.Manager
}

func newLoopContext(policy memory.Policy, initial []client.Message, tools []client.Tool) *loopContext {
	manager := memory.New(policy, initial)
	if toolTokens := memory.CountTools(tools); toolTokens > 0 {
		local := manager.LocalEstimate()
		manager.ObserveUsage(local+toolTokens, local)
	}
	return &loopContext{manager: manager}
}

func (c *loopContext) messages() []client.Message {
	if c == nil || c.manager == nil {
		return nil
	}
	return c.manager.Messages()
}

func (c *loopContext) append(messages ...client.Message) {
	if c == nil || c.manager == nil {
		return
	}
	for _, message := range messages {
		c.manager.Append(message)
	}
}

func (c *loopContext) observe(usage client.Usage, requestEstimate int) {
	if c == nil || c.manager == nil {
		return
	}
	c.manager.ObserveUsage(usage.PromptTokens, requestEstimate)
}

func (c *loopContext) shouldCompact() bool {
	return c != nil && c.manager != nil && c.manager.ShouldCompact()
}

func (c *loopContext) compactIfNeeded(
	ctx context.Context,
	configuredClient ModelClient,
	modelID string,
	reasoningEffort string,
) (*Compaction, error) {
	if !c.shouldCompact() {
		return nil, nil
	}
	if configuredClient == nil {
		return nil, errors.New("agentloop: context compaction requires a model client")
	}
	plan, err := c.manager.PlanWithRetention(memory.Retention{
		PreserveInstructions:     true,
		PreserveToolNames:        []string{AskToUserToolName},
		AllowTargetGrowth:        true,
		SummarizeOversizedRecent: true,
		ContinuationMessage:      "keep going",
	})
	if err != nil {
		return nil, fmt.Errorf("agentloop: plan context compaction: %w", err)
	}
	response, err := chatWithEmptyResponseRecovery(ctx, configuredClient, client.ChatRequest{
		Model: modelID, Messages: plan.RequestMessages(),
		ReasoningEffort: reasoningEffort,
	})
	if err != nil {
		return nil, fmt.Errorf("agentloop: compact context: %w", err)
	}
	if response == nil || len(response.Choices) == 0 {
		return nil, errors.New("agentloop: compact context returned no choices")
	}
	checkpoint, err := c.manager.ApplyCheckpoint(plan, response.Choices[0].Message.TextContent())
	if err != nil {
		return nil, fmt.Errorf("agentloop: compact context: %w", err)
	}
	return &Compaction{Plan: plan, Summary: checkpoint}, nil
}
