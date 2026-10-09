package app

import (
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
)

func (m model) updateThinkerResult(message thinkerResultMsg) (tea.Model, tea.Cmd) {
	if message.jobID == "" || message.jobID != m.thinkerJobID || message.sessionGeneration != m.sessionGeneration {
		return m, nil
	}
	m.thinkerBusy = false
	m.thinkerJobID = ""
	if message.result.LogError != "" {
		m.archiveFailure("thinker log", errors.New(message.result.LogError))
		_ = m.flushArchive()
	}
	if message.err != nil {
		m.archiveFailure("thinker", message.err)
		_ = m.flushArchive()
		if !m.waiting {
			m.status = "Thinker: " + message.err.Error()
			m.resize(m.width, m.height)
		}
		return m, nil
	}
	if m.learning != nil {
		if err := m.learning.Commit(message.jobID); err != nil {
			m.status = "Thinker checkpoint: " + err.Error()
			return m, nil
		}
		if err := m.saveWorkspaceSession(); err != nil {
			m.status = err.Error()
			return m, nil
		}
	}
	if message.checkpointStore != nil {
		if err := message.checkpointStore.ClearThinkerCheckpoint(message.jobID); err != nil {
			m.archiveFailure("clear thinker checkpoint", err)
			_ = m.flushArchive()
			m.status = "Thinker checkpoint cleanup: " + err.Error()
			m.resize(m.width, m.height)
			return m, m.startNextLearningSegment()
		}
	}
	if !m.waiting && message.result.Processed > 0 {
		m.status = fmt.Sprintf(
			"Thinker processed %d proposition(s) · %d created · %d merged · %d discarded",
			message.result.Processed, message.result.Created, message.result.Merged, message.result.Discarded,
		)
		m.resize(m.width, m.height)
	}
	return m, m.startNextLearningSegment()
}
