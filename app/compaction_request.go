package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/snowmerak/q/agentinstructions"
	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/diagnostics"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/memory"
)

type compactionCallOptions struct {
	Model                string
	ReasoningEffort      string
	ConversationID       string
	Tools                []client.Tool
	CoalesceInstructions bool
	WorkingDirectory     string
	Extra                map[string]any
	AllowInPrefix        bool
	LogManualInput       bool
}

func (m model) compactionToolCatalog(workingDirectory string) ([]client.Tool, bool) {
	if m.toolRuntime == nil {
		return nil, true
	}
	var allowedTools []string
	if m.sessionOptions != nil {
		allowedTools = m.sessionOptions.AllowedTools
	}
	if m.sessionOptions != nil && (m.sessionOptions.RuntimeFactory != nil || m.sessionOptions.WrapRuntime != nil) {
		return agentloop.ToolCatalog(m.toolRuntime, allowedTools), true
	}
	runtime, err := configuredAgentToolRuntime(
		m.toolRuntime, mcpconfig.RoleDefault, m.activeConfig(), workingDirectory,
	)
	if err != nil {
		return nil, false
	}
	runtime, err = m.configuredDelegationRuntime(runtime, workingDirectory)
	if err != nil {
		return nil, false
	}
	return agentloop.ToolCatalog(runtime, allowedTools), true
}

func runCompactionChat(
	ctx context.Context,
	memorySnapshot *memory.Manager,
	plan memory.Plan,
	opts compactionCallOptions,
	invoke func(context.Context, client.ChatRequest) (*client.ChatResponse, error),
) (*client.ChatResponse, error) {
	structuredCtx := client.WithStructuredGeneration(ctx)
	logInput := func(mode string, messages []client.Message) {
		if !opts.LogManualInput {
			return
		}
		logger := diagnostics.Logger(ctx)
		if logger.Enabled(ctx, slog.LevelInfo) {
			logger.InfoContext(ctx, "studio.compact.model_input",
				"requested_model", opts.Model,
				"input_tokens_estimate", memory.CountMessages(messages),
				"mode", mode,
			)
		}
	}
	if opts.AllowInPrefix && memorySnapshot != nil && memorySnapshot.CanCompactInPrefix(plan) {
		inPrefixMessages := append(
			providerMessages(agentinstructions.Normalize(memorySnapshot.Messages()), opts.CoalesceInstructions),
			plan.InPrefixMessage(),
		)
		logInput("in_prefix", inPrefixMessages)
		req := client.ChatRequest{
			Model:            opts.Model,
			Messages:         inPrefixMessages,
			ConversationID:   opts.ConversationID,
			Tools:            opts.Tools,
			ReasoningEffort:  opts.ReasoningEffort,
			WorkingDirectory: opts.WorkingDirectory,
			Extra:            opts.Extra,
		}
		if len(opts.Tools) > 0 {
			parallel := true
			req.ParallelToolCalls = &parallel
		}
		response, err := invoke(structuredCtx, req)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err != nil {
			if incomplete, ok := errors.AsType[*client.IncompleteGeneration](err); !ok || incomplete.Reason != "context_limit" {
				return nil, err
			}
		} else if response != nil && len(response.Choices) > 0 && len(response.Choices[0].Message.ToolCalls) == 0 {
			if _, _, applyErr := memorySnapshot.CheckpointCopy(plan, response.Choices[0].Message.TextContent()); applyErr == nil {
				return response, nil
			}
		}
	}
	messages := plan.RequestMessages()
	logInput("detached", messages)
	return invoke(structuredCtx, client.ChatRequest{
		Model:           opts.Model,
		Messages:        messages,
		ReasoningEffort: opts.ReasoningEffort,
		Extra:           opts.Extra,
	})
}
