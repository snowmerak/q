package app

import (
	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workspace"
)

// These mutations are shared by TUI, Studio, and ACP. Presentation and protocol
// notifications remain in their adapters; neither may acknowledge persistence
// until the session projection has been saved.
func (m *model) saveActiveTask(task *workspace.ActiveTask) error {
	m.activeTask = cloneActiveTask(task)
	return m.saveWorkspaceSession()
}

func (m *model) appendSessionMessage(message client.Message, usage *client.Usage) tea.Cmd {
	m.messages = append(m.messages, message)
	if message.Role == client.RoleAssistant && usage != nil {
		m.recordResponseUsage(*usage)
	}
	learning := m.observeLearningMessage(message)
	if m.memory != nil {
		m.memory.Append(message)
	}
	return learning
}

func (m *model) archiveSessionMessage(message client.Message, isError bool) {
	status := sessionstore.StatusSucceeded
	if isError {
		status = sessionstore.StatusFailed
	}
	m.archiveMessage(message, status, isError)
}

func (m *model) persistAgentMessage(event agentEvent) error {
	if err := m.saveWorkspaceSession(); err != nil {
		return err
	}
	acknowledgeAgentPersistence(event)
	return nil
}

func (m *model) persistAgentCompaction(event agentEvent) error {
	if err := m.applyAgentContextCompaction(*event.compaction); err != nil {
		return err
	}
	acknowledgeAgentPersistence(event)
	return nil
}

func acknowledgeAgentPersistence(event agentEvent) {
	if event.persistenceAck != nil {
		close(event.persistenceAck)
	}
}
