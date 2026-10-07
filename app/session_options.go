package app

import (
	"context"
	"errors"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

// SessionOptions configures an embedded session without replacing Q's session
// execution, persistence or recovery. RuntimeFactory owns the entire authorized
// catalog; Q does not add delegation or external tools to an injected runtime.
type SessionOptions struct {
	Model                string
	ReasoningEffort      string
	AnthropicPromptCache string
	SystemPrompt         string
	WorkingDirectory     string
	AuxiliaryDirectories []string
	DisableLearning      bool
	RuntimeKey           string
	RuntimeFactory       func(context.Context, workspace.Store) (AgentToolRuntime, io.Closer, error) `json:"-"`
	// OperationID makes retries continue a saved operation without appending
	// the same user prompt again. A completed operation returns its saved result.
	OperationID string `json:"-"`
}

func (m model) startSessionOperation(prompt, id string) (tea.Model, tea.Cmd) {
	if id == "" {
		m.sessionOperation = nil
		return m.startChatTurn(prompt, true)
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
	return m.startChatTurn(prompt, true)
}

// RunWithOptions runs through the same live session used by Run and Studio.
// Keep RuntimeKey stable while the injected runtime's authorization is unchanged.
func (host *SessionHost) RunWithOptions(ctx context.Context, store workspace.Store, sessionID, prompt string, options SessionOptions, emit SessionEventSink) error {
	if options.RuntimeFactory != nil && options.RuntimeKey == "" {
		return errors.New("injected session runtime requires a runtime key")
	}
	if options.AnthropicPromptCache != "" && options.AnthropicPromptCache != "1h" && options.AnthropicPromptCache != "off" {
		return errors.New("anthropic prompt cache must be 1h or off")
	}
	options.AuxiliaryDirectories = append([]string(nil), options.AuxiliaryDirectories...)
	return host.run(ctx, store, sessionID, prompt, emit, nil, options)
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
