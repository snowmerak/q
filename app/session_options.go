package app

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/internal/workitemtools"
	"github.com/snowmerak/q/internal/workmedia"
	"github.com/snowmerak/q/workspace"
)

// SessionOptions configures an embedded session without replacing Q's session
// execution, persistence or recovery. RuntimeFactory owns the authorized workspace
// catalog. AllowedTools can also restrict loop-owned orchestration and memory.
type SessionOptions struct {
	Type  workspace.SessionType
	Model string
	// Agent selects a registered ACP connection through ParticipantRunner.
	Agent                string
	ReasoningEffort      string
	AnthropicPromptCache string
	SystemPrompt         string
	WorkingDirectory     string
	AuxiliaryDirectories []string
	// Deprecated: automatic learning was removed; this compatibility field is ignored.
	DisableLearning    bool
	DisableExternalMCP bool
	RuntimeKey         string
	RuntimeFactory     func(context.Context, workspace.Store) (AgentToolRuntime, io.Closer, error) `json:"-"`
	// WrapRuntime restricts or extends the standard runtime without replacing
	// its archive, Skill Library, or resource ownership.
	WrapRuntime func(context.Context, workspace.Store, AgentToolRuntime) (AgentToolRuntime, error) `json:"-"`
	// AllowedTools filters the complete catalog. Nil preserves Q's default tools;
	// an empty non-nil list disables all tools.
	AllowedTools []string
	// Control belongs to this turn only and is never retained by a live session.
	Control *SessionRunControl `json:"-"`
	// OperationID makes retries continue a saved operation without appending
	// the same user prompt again. A completed operation returns its saved result.
	OperationID string       `json:"-"`
	Images      []ImageInput `json:"-"`
	review      workitemtools.ReviewAssignment
}

type ImageInput = workmedia.Image

// configuresRuntime excludes turn controls, which must preserve the session's
// selected native model or ACP connection.
func (options SessionOptions) configuresRuntime() bool {
	return options.Type != "" || options.Model != "" || options.Agent != "" ||
		options.ReasoningEffort != "" || options.AnthropicPromptCache != "" ||
		options.SystemPrompt != "" || options.WorkingDirectory != "" || options.AuxiliaryDirectories != nil ||
		options.DisableLearning || options.DisableExternalMCP || options.RuntimeKey != "" ||
		options.RuntimeFactory != nil || options.WrapRuntime != nil || options.AllowedTools != nil
}

func (m model) startSessionOperation(prompt, id string, images []ImageInput) (tea.Model, tea.Cmd) {
	if id == "" {
		m.sessionOperation = nil
		return m.startChatTurn(prompt, !m.clientIsACPRemote(), images...)
	}
	if operation := m.sessionOperation; operation != nil && operation.ID == id {
		if operation.Prompt != prompt {
			m.turnErr = errors.New("session operation prompt changed")
			return m, nil
		}
		if operation.ContextReady {
			command := m.continueChatTurn()
			return m, command
		}
		// A cancelled deferred prompt exists only in the audit transcript;
		// startChatTurn will compact and insert it into context on this attempt.
		if len(m.messages) > 0 {
			last := m.messages[len(m.messages)-1]
			if last.Role == client.RoleUser && strings.TrimSpace(last.Content) == prompt {
				m.messages = m.messages[:len(m.messages)-1]
			}
		}
	}
	m.sessionOperation = &workspace.SessionOperation{ID: id, Prompt: prompt}
	return m.startChatTurn(prompt, !m.clientIsACPRemote())
}

// RunWithOptions runs through the same live session used by Run and Studio.
// Keep RuntimeKey stable while the injected runtime's authorization is unchanged.
func (host *SessionHost) RunWithOptions(ctx context.Context, store workspace.Store, sessionID, prompt string, options SessionOptions, emit SessionEventSink) (returnErr error) {
	if options.Control != nil {
		defer func() { options.Control.finish(returnErr) }()
	}
	if options.Agent != "" {
		return errors.New("ACP sessions require a participant runner")
	}
	if (options.RuntimeFactory != nil || options.WrapRuntime != nil) && options.RuntimeKey == "" {
		return errors.New("injected session runtime requires a runtime key")
	}
	if options.AnthropicPromptCache != "" && options.AnthropicPromptCache != "1h" && options.AnthropicPromptCache != "off" {
		return errors.New("anthropic prompt cache must be 1h or off")
	}
	options.AuxiliaryDirectories = append([]string(nil), options.AuxiliaryDirectories...)
	options.AllowedTools = slices.Clone(options.AllowedTools)
	return host.run(ctx, store, sessionID, prompt, emit, options.Control, options)
}

func (m model) executionRoots() (string, []string) {
	if m.sessionOptions != nil && (m.sessionOptions.RuntimeFactory != nil || m.sessionOptions.WorkingDirectory != "") {
		return m.sessionOptions.WorkingDirectory, append([]string(nil), m.sessionOptions.AuxiliaryDirectories...)
	}
	var root string
	var auxiliary []string
	if m.workspaceStore != nil {
		root = m.workspaceStore.Root
	}
	if m.studioWorkspaceContext != nil {
		auxiliary = append(auxiliary, m.studioWorkspaceContext.AuxiliaryRoots...)
	}
	return root, auxiliary
}
