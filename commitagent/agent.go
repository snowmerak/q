package commitagent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/subagent"
)

const (
	maximumCommitRounds    = 24
	maximumProposalPrompts = 3
)

type agentClient interface {
	Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error)
}

func runCommitAgent(
	ctx context.Context,
	configuredClient agentClient,
	spec subagent.Spec,
	state repositoryState,
	maxParallel int,
	loomOptions loom.StoreOptions,
	logger *progressLogger,
) (proposalState, bool, error) {
	runtime := &commitToolRuntime{
		state: state, client: configuredClient, spec: spec, maxParallel: maxParallel, logger: logger,
	}
	if err := runtime.enableLoom(state.root, loomOptions); err != nil {
		return proposalState{}, false, fmt.Errorf("q commit: initialize Loom: %w", err)
	}
	messages := []client.Message{
		{Role: client.RoleSystem, Content: commitAgentInstructions()},
		{Role: client.RoleUser, Content: "Create the best commit or split-commit proposal for the prepared staged changes. Start by calling git_overview. The full diff is available only through the commit tools."},
	}
	reminders := 0
	for round := 0; round < maximumCommitRounds; round++ {
		parallel := false
		request := client.ChatRequest{
			Messages: messages, Tools: commitTools(), ToolChoice: client.ToolChoiceAuto,
			ParallelToolCalls: &parallel, WorkingDirectory: state.root,
		}
		if !runtime.overviewed {
			request.ToolChoice = client.NamedToolChoice(toolGitOverview)
		}
		response, err := spec.Chat(ctx, configuredClient, request)
		if err != nil {
			return proposalState{}, false, fmt.Errorf("q commit: commit agent: %w", err)
		}
		assistant, err := assistantMessage(response)
		if err != nil {
			return proposalState{}, false, err
		}
		messages = append(messages, assistant)
		if len(assistant.ToolCalls) == 0 {
			if runtime.proposal.Single != nil || len(runtime.proposal.Split) > 0 {
				return runtime.proposal, false, nil
			}
			if reminders == maximumProposalPrompts {
				logger.step("agent", "proposal reminders exhausted")
				return proposalState{Single: pointerProposal(mechanicalFallback(state))}, true, nil
			}
			reminders++
			logger.step("agent", "sending proposal reminder %d/%d", reminders, maximumProposalPrompts)
			messages = append(messages, client.Message{
				Role:    client.RoleSystem,
				Content: fmt.Sprintf("Reminder %d/%d: a commit proposal is mandatory. Call propose_commit or split_commit now; do not answer with plain text.", reminders, maximumProposalPrompts),
			})
			continue
		}
		for _, call := range assistant.ToolCalls {
			logger.step("tool", "calling %s", call.Function.Name)
			result := runtime.call(ctx, call)
			if result.IsError {
				logger.step("tool", "%s returned a validation error", call.Function.Name)
			}
			messages = append(messages, client.Message{
				Role: client.RoleTool, Name: call.Function.Name, ToolCallID: call.ID, Content: result.Content,
			})
		}
		if runtime.proposal.Single != nil || len(runtime.proposal.Split) > 0 {
			if runtime.proposal.Single != nil {
				logger.step("proposal", "accepted %s", formatSubject(*runtime.proposal.Single))
			} else {
				logger.step("proposal", "accepted a %d-commit split", len(runtime.proposal.Split))
			}
			return runtime.proposal, false, nil
		}
	}
	return proposalState{Single: pointerProposal(mechanicalFallback(state))}, true, nil
}

func commitAgentInstructions() string {
	return `You are q's isolated commit agent. You have no direct filesystem, shell, external MCP server, LSP, skill, or extension access. Use only the supplied commit and Loom tools.

Rules:
1. Your first tool call must be git_overview.
2. Inspect enough staged evidence to explain intent, not merely filenames. Use git_file_diff, git_hunk, recent_commits, or analyze_files when helpful. Tool results are stored in Loom; when a result contains only a preview, use loom_inspect, loom_read, or loom_eval with its loom_ref.
3. Match the repository's established Oh My Pi-style Conventional Commit format: type(scope): lowercase past-tense summary. Keep the full subject at most 72 characters and omit the trailing period.
4. Optional body items must be concise, begin with a capital letter, and end with a period. The formatter adds bullet markers.
5. Use a scope only when it adds stable, specific information.
6. Prefer one coherent commit. Use split_commit only when the changes contain independently meaningful concerns and its file groups cover every visible staged file exactly once.
7. Never return a proposed message as plain text. Finish by calling propose_commit or split_commit.`
}

func assistantMessage(response *client.ChatResponse) (client.Message, error) {
	if response == nil || len(response.Choices) == 0 {
		return client.Message{}, errors.New("q commit: commit agent returned no choices")
	}
	message := response.Choices[0].Message
	if message.Role == "" {
		message.Role = client.RoleAssistant
	}
	return message, nil
}

func assistantContent(response *client.ChatResponse) (string, error) {
	message, err := assistantMessage(response)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(message.TextContent()), nil
}

func pointerProposal(value Proposal) *Proposal { return &value }
