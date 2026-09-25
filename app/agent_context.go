package app

import (
	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
)

type AgentContextCompaction = agentloop.AgentContextCompaction
type agentContextCompaction = AgentContextCompaction
type agentLoopContext = agentloop.Context

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

func newAgentLoopContext(policy memory.Policy, initial []client.Message, tools []client.Tool) *agentLoopContext {
	return agentloop.NewContext(policy, initial, tools)
}
