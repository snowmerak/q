package app

import (
	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/memory"
)

type AgentContextCompaction = agentloop.AgentContextCompaction
type agentContextCompaction = AgentContextCompaction

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
