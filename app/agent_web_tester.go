package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

var webTesterAgentCommand = externalAgentCommand{
	toolName: subagent.ExternalWebTesterToolName, agentName: "web-tester", callPrefix: "q-agent-web-tester-",
	usage:       "Usage: /subagent builtin/web-tester <request>",
	unavailable: "External Web Tester is unavailable",
	errorText:   "external web tester is unavailable",
	starting:    "Starting Web Tester agent…",
	completed:   "web test result captured in Loom",
	failed:      "external web tester returned an error",
	acpStarted:  "Web Tester agent started.\n",
	acpReceived: "Web test result received. Main agent is preparing the answer.\n",
	acpFailure:  "ACP agent web tester failed",
	toolCall:    agentWebTesterToolCall,
}

func explicitAgentWebTesterInput(request string) subagent.ExternalWebTesterInput {
	return subagent.ExternalWebTesterInput{
		Request: strings.TrimSpace(request),
		CompletionCriteria: []string{
			"Return a structured result grounded in checks actually performed against the current workspace or application.",
		},
	}
}

func agentWebTesterToolCall(request, callID string) (client.ToolCall, error) {
	payload, err := json.Marshal(explicitAgentWebTesterInput(request))
	if err != nil {
		return client.ToolCall{}, fmt.Errorf("encode agent web tester input: %w", err)
	}
	return client.ToolCall{
		ID: callID, Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: subagent.ExternalWebTesterToolName, Arguments: string(payload)},
	}, nil
}

func (a *acpAgent) runACPAgentWebTester(ctx context.Context, request string) (acp.PromptResponse, error) {
	toolRuntime, err := configuredAgentToolRuntime(
		a.state.toolRuntime, mcpconfig.RoleDefault, a.state.activeConfig(), a.root,
	)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	if !toolAvailable(toolRuntime, subagent.ExternalWebTesterToolName) {
		return acp.PromptResponse{}, errors.New("external web tester is unavailable")
	}

	return a.runACPExternalAgent(ctx, toolRuntime, webTesterAgentCommand, request)
}
