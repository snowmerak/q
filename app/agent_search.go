package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

var searchAgentCommand = externalAgentCommand{
	toolName: subagent.ExternalSearchToolName, agentName: "search", callPrefix: "q-agent-search-",
	usage:       "Usage: /subagent builtin/web-search <query>",
	unavailable: "Search agent is not configured · assign it in /subagents",
	errorText:   "search agent is not configured",
	starting:    "Starting Search agent…",
	completed:   "external evidence captured in Loom",
	failed:      "external search returned an error",
	acpStarted:  "Search agent started.\n",
	acpReceived: "Search evidence received. Main agent is preparing the answer.\n",
	acpFailure:  "ACP agent search failed",
	toolCall:    agentSearchToolCall,
}

func explicitAgentSearchInput(query string) subagent.ExternalSearchInput {
	return subagent.ExternalSearchInput{
		Query: strings.TrimSpace(query),
		CompletionCriteria: []string{
			"Return a concise evidence-backed answer with direct source URLs.",
		},
	}
}

func agentSearchToolCall(query, callID string) (client.ToolCall, error) {
	payload, err := json.Marshal(explicitAgentSearchInput(query))
	if err != nil {
		return client.ToolCall{}, fmt.Errorf("encode agent search input: %w", err)
	}
	return client.ToolCall{
		ID: callID, Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: subagent.ExternalSearchToolName, Arguments: string(payload)},
	}, nil
}

func (a *acpAgent) runACPAgentSearch(ctx context.Context, query string) (acp.PromptResponse, error) {
	toolRuntime, err := configuredAgentToolRuntime(
		a.state.toolRuntime, mcpconfig.RoleDefault, a.state.activeConfig(), a.root,
	)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	if !toolAvailable(toolRuntime, subagent.ExternalSearchToolName) {
		message := "Search agent is not configured. Assign an enabled ACP connection to builtin/web-search with /subagents."
		if err := a.updateContext(ctx, acp.UpdateAgentMessageText(message)); err != nil {
			return acp.PromptResponse{}, err
		}
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}

	return a.runACPExternalAgent(ctx, toolRuntime, searchAgentCommand, query)
}
