package app

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

type delegationRecoveryMsg struct {
	events         <-chan delegationRecoveryMsg
	activity       *agentActivity
	trace          *agentTrace
	done           bool
	sessionID      string
	runID          string
	generation     uint64
	turnID         uint64
	previousLength int
	session        workspace.Session
	recovered      int
	err            error
}

func (m *model) continueRecoveredSession() tea.Cmd {
	if m.delegationRecoveryPending {
		return m.startDelegationRecovery()
	}
	return m.resumeRecoveredTurn()
}

func (m *model) startDelegationRecovery() tea.Cmd {
	if !m.delegationRecoveryPending || m.workspaceStore == nil || m.waiting {
		return nil
	}
	m.beginTurn()
	m.turnMessageStart = len(m.messages)
	m.waiting = true
	m.input.Blur()
	m.status = "Recovering delegated task…"
	copy := *m
	store := *m.workspaceStore
	generation := m.sessionGeneration
	turnID := m.turnID
	runID := m.runID
	events := make(chan delegationRecoveryMsg)
	return func() tea.Msg {
		go func() {
			defer close(events)
			send := func(message delegationRecoveryMsg) {
				message.sessionID, message.runID, message.generation, message.turnID = store.SessionID, runID, generation, turnID
				select {
				case events <- message:
				case <-copy.activeTurnContext().Done():
				}
			}
			session, err := store.Load()
			if err != nil {
				send(delegationRecoveryMsg{done: true, err: err})
				return
			}
			previous := len(workspaceSessionMessages(copy.messages))
			recovered, err := copy.recoverPendingWorkspaceCalls(copy.activeTurnContext(), &session, func(event agentEvent) {
				send(delegationRecoveryMsg{activity: event.activity, trace: event.trace})
			})
			send(delegationRecoveryMsg{done: true, previousLength: previous, session: session, recovered: recovered, err: err})
		}()
		return waitDelegationRecoveryEvent(events)()
	}
}

func waitDelegationRecoveryEvent(events <-chan delegationRecoveryMsg) tea.Cmd {
	return func() tea.Msg {
		message, ok := <-events
		if !ok {
			return delegationRecoveryMsg{done: true, err: errors.New("delegation recovery event stream closed unexpectedly")}
		}
		message.events = events
		return message
	}
}

func (m *model) acceptDelegationRecovery(message delegationRecoveryMsg) tea.Cmd {
	if m.workspaceStore == nil || m.workspaceStore.SessionID != message.sessionID || m.runID != message.runID || m.sessionGeneration != message.generation || m.turnID != message.turnID {
		return nil
	}
	if !message.done {
		if message.activity != nil {
			m.appendAgentActivity(*message.activity)
			m.status = activityStatus(*message.activity)
		}
		if message.trace != nil {
			m.appendAgentTrace(*message.trace)
		}
		m.resize(m.width, m.height)
		return waitDelegationRecoveryEvent(message.events)
	}
	m.finishTurn()
	m.waiting = false
	m.appendRecoveredTranscript(message.session, message.previousLength)
	m.refreshTranscript()
	calls, completed := pendingSessionCalls(message.session.Transcript)
	m.delegationRecoveryPending = len(calls) > completed
	if message.err != nil {
		if len(message.session.Transcript) == 0 {
			m.delegationRecoveryPending = true
		}
		m.status = "Delegated task recovery: " + message.err.Error()
		return m.input.Focus()
	}
	m.status = fmt.Sprintf("Recovered %d interrupted tool result(s)", message.recovered)
	m.recoverDelegationTurn = message.recovered > 0 || len(message.session.Transcript) > message.previousLength
	return m.resumeRecoveredTurn()
}

func (m *model) appendRecoveredTranscript(session workspace.Session, previousLength int) {
	for _, recovered := range session.Transcript[min(previousLength, len(session.Transcript)):] {
		m.messages = append(m.messages, recovered)
		if m.memory != nil {
			m.memory.Append(recovered)
		}
	}
}

// pendingSessionCalls finds the only legal open tool turn: the last assistant
// message followed by zero or more tool results in call order.
func pendingSessionCalls(messages []client.Message) ([]client.ToolCall, int) {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != client.RoleAssistant || len(message.ToolCalls) == 0 {
			continue
		}
		completed := 0
		for _, following := range messages[index+1:] {
			if following.Role != client.RoleTool {
				return nil, 0
			}
			completed++
		}
		return message.ToolCalls, completed
	}
	return nil, 0
}

func (m *model) recoverPendingWorkspaceCalls(recoveryContext context.Context, session *workspace.Session, observe func(agentEvent)) (int, error) {
	if m.workspaceStore == nil || session == nil {
		return 0, nil
	}
	calls, completed := pendingSessionCalls(session.Transcript)
	if len(calls) <= completed {
		return 0, nil
	}
	var runtime agentToolRuntime
	for _, call := range calls[completed:] {
		if call.Function.Name != subagent.DelegateToolName {
			continue
		}
		if m.toolRuntime == nil || m.client == nil {
			return 0, errors.New("delegation recovery requires the configured model and tools")
		}
		base, err := configuredAgentToolRuntime(m.toolRuntime, mcpconfig.RoleDefault, m.activeConfig(), m.workspaceStore.Root)
		if err != nil {
			return 0, err
		}
		runtime, err = m.configuredDelegationRuntime(base, m.workspaceStore.Root)
		if err != nil {
			return 0, err
		}
		if delegation, ok := runtime.(*delegationRuntime); ok && observe != nil {
			delegation.observeDelegationEvents(observe)
		}
		break
	}
	recovered := 0
	if recoveryContext == nil {
		recoveryContext = context.Background()
	}
	for _, call := range calls[completed:] {
		var result client.ToolResult
		if call.Function.Name == subagent.DelegateToolName {
			var err error
			result, err = runtime.Call(recoveryContext, call)
			if err != nil {
				return recovered, fmt.Errorf("recover delegation %s: %w", call.ID, err)
			}
		} else {
			result = client.ToolResult{Content: `{"status":"unknown","detail":"execution outcome unknown after session restart"}`, IsError: true}
		}
		message := client.ToolResultMessage(call, result)
		updated := *session
		updated.Transcript = append(append([]client.Message(nil), session.Transcript...), message)
		if contextCalls, contextCompleted := pendingSessionCalls(session.Context); len(contextCalls) > contextCompleted {
			// A compacted context can differ from the full transcript, but its
			// current tool turn must retain the same call ordinal.
			if contextCompleted >= len(contextCalls) || contextCalls[contextCompleted].ID != call.ID {
				return recovered, errors.New("saved delegation context does not match transcript")
			}
			updated.Context = append(append([]client.Message(nil), session.Context...), message)
		}
		if err := m.workspaceStore.Save(updated); err != nil {
			return recovered, err
		}
		*session = updated
		m.archiveToolCall(call)
		status := sessionstore.StatusSucceeded
		if result.IsError {
			status = sessionstore.StatusFailed
			if call.Function.Name != subagent.DelegateToolName {
				status = sessionstore.StatusUnknown
			}
		}
		m.archiveMessage(message, status, result.IsError)
		recovered++
	}
	return recovered, nil
}
