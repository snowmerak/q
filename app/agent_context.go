package app

import (
	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/memory"
)

// agentContextCompaction transfers a loop-local compaction to the owning TUI
// or ACP session memory without changing the full transcript.
type agentContextCompaction = agentloop.Compaction

func (m *model) applyAgentContextCompaction(compaction agentContextCompaction) error {
	if m.memory == nil {
		m.memory = memory.New(memoryPolicy(m.activeConfig()), nil)
	}
	checkpoint, err := m.memory.ApplyCheckpoint(compaction.Plan, compaction.Summary)
	if err != nil {
		return err
	}
	m.conversationID = ""
	m.archiveSummary(checkpoint)
	return m.saveWorkspaceSession()
}
