package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/subagent"
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
