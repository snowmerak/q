package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
)

type externalSubagentInput struct {
	Request string `json:"request"`
}

func builtinExternalSystemPrompt(name string) string {
	for _, definition := range subagent.ExternalAgentDefinitions() {
		if definition.Info.Name == name {
			return definition.SystemPrompt
		}
	}
	return ""
}

// ACP currently has no system-prompt field in NewSession. External subagent
// instructions are therefore sent at the start of the first ordinary prompt.
func externalSubagentPrompt(systemPrompt, request string) string {
	return strings.TrimSpace(systemPrompt) + "\n\nRequest:\n" + strings.TrimSpace(request)
}

func configuredExternalSubagentInvocation(
	root, connectionID string,
	connection config.AgentConnectionConfig,
	definition subagent.AgentDefinition,
) subagent.Invocation {
	strict := true
	return subagent.Invocation{
		Tool: client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
			Name: "external_subagent", Description: "Run the configured ACP-backed subagent.", Strict: &strict,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"request": map[string]any{"type": "string", "maxLength": subagent.MaximumDelegatePromptBytes},
			}, "required": []string{"request"}, "additionalProperties": false},
		}},
		Source: subagent.InvocationSource{
			Protocol: "acp", Name: connectionID, Kind: "agent-result",
			MediaType: "text/plain",
		},
		Handler: func(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
			var input externalSubagentInput
			if err := decodeDelegationArguments(call.Function.Arguments, &input); err != nil {
				return client.ToolResult{}, err
			}
			report, err := runACPExternalSubagent(ctx, root, connectionID, connection, definition, input.Request)
			if err != nil {
				return client.ToolResult{}, err
			}
			return client.ToolResult{Content: report}, nil
		},
	}
}

func runACPExternalSubagent(
	ctx context.Context,
	root, connectionID string,
	connection config.AgentConnectionConfig,
	definition subagent.AgentDefinition,
	request string,
) (report string, runErr error) {
	if definition.Info.Kind != subagent.AgentKindExternal {
		return "", fmt.Errorf("subagent %q is not external", definition.Info.Name)
	}
	if strings.TrimSpace(request) == "" {
		return "", errors.New("external subagent request is required")
	}
	command, err := resolveConfiguredACPAgentCommand(connection, exec.LookPath)
	if err != nil {
		return "", fmt.Errorf("external subagent %q: %w", definition.Info.Name, err)
	}
	remote, err := startACPRemoteClient(ctx, command, root, "", connection.AuthMethod, io.Discard)
	if err != nil {
		return "", fmt.Errorf("external subagent %q: %w", definition.Info.Name, err)
	}
	remote.mu.Lock()
	remote.permissions = acpPermissionReadOnly
	if definition.Info.MutatesWorkspace {
		remote.permissions = acpPermissionAutomatic
	}
	remote.mu.Unlock()
	defer func() {
		runErr = errors.Join(runErr, disposeACPRemote(remote))
	}()
	report, err = remote.promptText(ctx, externalSubagentPrompt(definition.SystemPrompt, request))
	if err != nil {
		return "", fmt.Errorf("external subagent %q via ACP agent %q: %w", definition.Info.Name, connectionID, err)
	}
	return strings.TrimSpace(report), nil
}
