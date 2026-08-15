package app

import (
	"encoding/json"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/thinker"
)

func (m *model) learningContextLength() int64 {
	if m == nil {
		return 0
	}
	models := append([]client.Model(nil), m.models...)
	spec, err := subagent.Resolve(m.config, config.AgentRoleThinker, models)
	if err != nil {
		return 0
	}
	if spec.Group == "" && spec.ContextLength <= 0 && spec.Model == m.config.Provider.Model {
		return m.config.EffectiveContextWindow()
	}
	return spec.ContextLength
}

func (m *model) ensureLearningMachine(state thinker.LearningState, seed []client.Message) error {
	machine, err := thinker.NewMachine(state, m.learningContextLength())
	if err != nil {
		return err
	}
	machine.Initialize(seed)
	m.learning = machine
	return nil
}

func (m *model) observeLearningMessage(message client.Message) tea.Cmd {
	if m == nil || m.learning == nil {
		return nil
	}
	m.refreshLearningContextLength()
	m.learning.AppendMessage(message)
	_ = m.saveWorkspaceSession()
	return m.startNextLearningSegment()
}

func (m *model) enqueueLearningSpecial(name string, payload json.RawMessage) tea.Cmd {
	if m == nil || m.learning == nil {
		return nil
	}
	m.refreshLearningContextLength()
	switch name {
	case thinker.TaskCompleteEventName:
		m.learning.AppendTaskComplete(payload)
	case thinker.PlanApprovedEventName:
		m.learning.AppendPlanApproved(payload)
	}
	_ = m.saveWorkspaceSession()
	return m.startNextLearningSegment()
}

func (m *model) enqueueExplicitLearning() tea.Cmd {
	if m == nil || m.learning == nil {
		return nil
	}
	m.refreshLearningContextLength()
	m.learning.EnqueueExplicit()
	_ = m.saveWorkspaceSession()
	return m.startNextLearningSegment()
}

func (m *model) startNextLearningSegment() tea.Cmd {
	if m == nil || m.thinkerBusy || m.learning == nil || m.client == nil || m.libraryClient == nil || m.thinkerSerial == nil {
		return nil
	}
	m.refreshLearningContextLength()
	segment, messages, ok := m.learning.Next()
	if !ok {
		return nil
	}
	m.thinkerBusy = true
	m.thinkerJobID = segment.ID
	m.ensureRunID()
	job := thinker.Job{
		ID: segment.ID, Messages: messages,
		Refs: []string{"run:" + m.runID, "learning-segment:" + segment.ID},
	}
	if m.workspaceStore != nil {
		job.WorkingDirectory = m.workspaceStore.Root
	}
	configuredClient := m.client
	libraryClient := m.libraryClient
	serial := m.thinkerSerial
	value := m.config
	models := append([]client.Model(nil), m.models...)
	ctx := m.ctx
	return func() tea.Msg {
		if len(models) == 0 {
			listed, err := configuredClient.ListModels(ctx)
			if err != nil {
				return thinkerResultMsg{jobID: job.ID, err: fmt.Errorf("resolve thinker model: %w", err)}
			}
			models = listed
		}
		spec, err := subagent.Resolve(value, config.AgentRoleThinker, models)
		if err != nil {
			return thinkerResultMsg{jobID: job.ID, err: err}
		}
		if spec.Group == "" && spec.ContextLength <= 0 && spec.Model == value.Provider.Model {
			spec.ContextLength = value.EffectiveContextWindow()
		}
		result, err := serial.Run(ctx, thinker.Runner{
			Client: configuredClient, Library: libraryClient, Spec: spec,
		}, job)
		return thinkerResultMsg{jobID: job.ID, result: result, err: err}
	}
}

func (m *model) refreshLearningContextLength() {
	if m == nil || m.learning == nil {
		return
	}
	if closed := m.learning.SetContextLength(m.learningContextLength()); len(closed) > 0 {
		_ = m.saveWorkspaceSession()
	}
}
