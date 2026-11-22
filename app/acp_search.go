package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

var externalSearchURL = regexp.MustCompile(`https?://[^\s\]\[()<>{}"']+`)

func configuredExternalSearchInvocation(value config.Config, root string) (subagent.Invocation, bool) {
	connectionID, connection, configured := value.ExternalAgentConnection(config.AgentRoleSearch)
	if !configured {
		return subagent.Invocation{}, false
	}
	selection := value.Agents.Roles[config.AgentRoleSearch].ACPSelection
	return subagent.Invocation{
		Tool: subagent.ExternalSearchTool(),
		Source: subagent.InvocationSource{
			Protocol: "acp", Name: connectionID, Kind: "agent-result",
			MediaType: "application/vnd.q.agent-result+json",
		},
		Handler: func(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
			input, err := subagent.ParseExternalSearchInput(call.Function.Arguments)
			if err != nil {
				return client.ToolResult{}, err
			}
			result, err := runACPExternalSearch(ctx, root, connectionID, connection, selection, input)
			if err != nil {
				return client.ToolResult{}, err
			}
			body, err := json.Marshal(result)
			if err != nil {
				return client.ToolResult{}, fmt.Errorf("encode external search result: %w", err)
			}
			return client.ToolResult{Content: string(body)}, nil
		},
	}, true
}

func runACPExternalSearch(
	ctx context.Context,
	root, connectionID string,
	connection config.AgentConnectionConfig,
	selection config.ACPSelection,
	input subagent.ExternalSearchInput,
) (result subagent.ExternalSearchResult, runErr error) {
	remote, err := startACPSubagent(ctx, root, connection, selection)
	if err != nil {
		return subagent.ExternalSearchResult{}, fmt.Errorf("search agent %q: %w", connectionID, err)
	}
	return executeACPExternalSearch(ctx, remote, connectionID, input)
}

func probeACPAgentConnection(
	ctx context.Context,
	root, connectionID string,
	connection config.AgentConnectionConfig,
) error {
	_, err := ProbeACPModelSettings(ctx, root, connectionID, connection, "")
	return err
}

// ProbeACPAgentConnection validates an ACP connection by completing the same
// initialize, session, and disposal lifecycle used by the TUI connection
// tester. Studio uses this entry point so both interfaces exercise identical
// process resolution and authentication behavior.
func ProbeACPAgentConnection(
	ctx context.Context,
	root, connectionID string,
	connection config.AgentConnectionConfig,
) error {
	return probeACPAgentConnection(ctx, root, connectionID, connection)
}

// ProbeACPModelSettings reads the options from a temporary session. Preview a
// model only when the agent offers it, so stale saved IDs can be replaced.
func ProbeACPModelSettings(ctx context.Context, root, connectionID string, connection config.AgentConnectionConfig, model string) (_ ACPModelSettings, returnErr error) {
	remote, err := startACPSubagent(ctx, root, connection, config.ACPSelection{})
	if err != nil {
		return ACPModelSettings{}, fmt.Errorf("agent connection %q: %w", connectionID, err)
	}
	defer func() { returnErr = errors.Join(returnErr, disposeACPRemote(remote)) }()
	settings := remote.modelSettings()
	if model != "" && model != settings.CurrentModel && slices.ContainsFunc(settings.Models, func(option ACPModelOption) bool {
		return option.ID == model
	}) {
		if err := remote.setModel(ctx, model); err != nil {
			return ACPModelSettings{}, fmt.Errorf("preview ACP model %q: %w", model, err)
		}
		settings = remote.modelSettings()
	}
	return settings, nil
}

func executeACPExternalSearch(
	ctx context.Context,
	remote *acpRemoteClient,
	connectionID string,
	input subagent.ExternalSearchInput,
) (result subagent.ExternalSearchResult, runErr error) {
	remote.mu.Lock()
	remote.permissions = acpPermissionReadOnly
	remote.mu.Unlock()
	defer func() {
		runErr = errors.Join(runErr, disposeACPRemote(remote))
	}()

	prompt, err := externalSearchPrompt(input)
	if err != nil {
		return subagent.ExternalSearchResult{}, err
	}
	report, err := remote.promptText(ctx, prompt)
	if err != nil {
		return subagent.ExternalSearchResult{}, fmt.Errorf("search agent %q: %w", connectionID, err)
	}
	report = strings.TrimSpace(report)
	return subagent.ExternalSearchResult{
		Agent: connectionID, Summary: report, Sources: externalSearchSources(report),
	}, nil
}

func disposeACPRemote(remote *acpRemoteClient) error {
	cleanupContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return errors.Join(remote.disposeSession(cleanupContext), remote.close(false))
}

func (r *acpRemoteClient) disposeSession(ctx context.Context) error {
	r.mu.Lock()
	connection := r.connection
	sessionID := r.sessionID
	canDelete := r.capabilities.SessionCapabilities.Delete != nil
	canClose := r.capabilities.SessionCapabilities.Close != nil
	r.mu.Unlock()
	if connection == nil || sessionID == "" || r.processInterrupted() {
		return nil
	}
	var disposeErr error
	if canDelete {
		_, disposeErr = connection.UnstableDeleteSession(ctx, acp.UnstableDeleteSessionRequest{SessionId: sessionID})
		if r.processInterrupted() {
			return nil
		}
		if disposeErr == nil {
			r.clearDisposedSession(sessionID)
			return nil
		}
	}
	if canClose {
		_, closeErr := connection.CloseSession(ctx, acp.CloseSessionRequest{SessionId: sessionID})
		if r.processInterrupted() {
			return nil
		}
		if closeErr == nil {
			r.clearDisposedSession(sessionID)
			return nil
		}
		return errors.Join(disposeErr, closeErr)
	}
	return disposeErr
}

func (r *acpRemoteClient) clearDisposedSession(sessionID acp.SessionId) {
	r.mu.Lock()
	if r.sessionID == sessionID {
		r.sessionID = ""
	}
	r.mu.Unlock()
}

func externalSearchPrompt(input subagent.ExternalSearchInput) (string, error) {
	body, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode external search request: %w", err)
	}
	return externalSubagentPrompt(builtinExternalSystemPrompt(subagent.BuiltinWebSearchID), string(body)), nil
}

func externalSearchSources(report string) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, match := range externalSearchURL.FindAllString(report, -1) {
		match = strings.TrimRight(match, ".,;:!?")
		if _, found := seen[match]; found {
			continue
		}
		seen[match] = struct{}{}
		result = append(result, match)
	}
	sort.Strings(result)
	return result
}
