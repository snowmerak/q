package app

import (
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
)

func (m model) updateAgentEvent(message agentEventMsg) (tea.Model, tea.Cmd) {
	if message.turnID != 0 && message.turnID != m.turnID {
		return m, nil
	}
	event := message.event
	if event.taskStarted != nil {
		m.activeTask = cloneActiveTask(event.taskStarted)
		if err := m.saveWorkspaceSession(); err != nil {
			m.status = err.Error()
		}
		return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
	}
	if event.taskCompleted {
		m.activeTask = nil
		if err := m.saveWorkspaceSession(); err != nil {
			m.status = err.Error()
		}
		return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
	}
	if event.streamDelta != nil {
		delta := *event.streamDelta
		switch delta.Kind {
		case chatStreamThinking:
			if delta.Start || len(m.transcriptThoughts) == 0 {
				m.transcriptThoughts = append(m.transcriptThoughts, transcriptThought{Before: len(m.messages)})
			}
			m.transcriptThoughts[len(m.transcriptThoughts)-1].Content += delta.Content
			m.status = "Thinking…"
		case chatStreamResponse:
			if delta.Start {
				m.streamResponse = ""
			}
			m.streamResponse += delta.Content
			m.status = "Responding…"
		}
		m.refreshTranscript()
		return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
	}
	if event.learningName != "" {
		command := m.enqueueLearningSpecial(event.learningName, event.learningPayload)
		return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID), command)
	}
	if event.plan != nil {
		m.status = agentPlanStatus(*event.plan)
		return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
	}
	if event.activity != nil {
		m.appendAgentActivity(*event.activity)
		m.status = activityStatus(*event.activity)
		m.resize(m.width, m.height)
		return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
	}
	if event.trace != nil {
		m.appendAgentTrace(*event.trace)
		m.resize(m.width, m.height)
		return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
	}
	if event.status != "" {
		m.status = event.status
		return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
	}
	if event.compaction != nil {
		if err := m.applyAgentContextCompaction(*event.compaction); err != nil {
			m.status = "apply agent context compaction: " + err.Error()
		} else {
			m.status = "Context compacted · continuing…"
		}
		return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
	}
	if event.contextReplace != nil {
		if m.memory != nil {
			if err := m.memory.Replace(event.contextReplace.Index, event.contextReplace.Message); err != nil {
				m.status = "update model context: " + err.Error()
			}
		}
		return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
	}
	if event.call != nil {
		m.archiveToolCall(*event.call)
		m.status = "Running tool · " + describeToolCall(*event.call)
		var learning tea.Cmd
		if event.call.Function.Name == "learn" {
			learning = m.enqueueExplicitLearning()
		}
		return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID), learning)
	}
	if event.question != nil {
		m.asking = true
		m.pendingQuestion = *event.question
		m.questionChoice = 0
		m.questionAnswer = event.answer
		m.questionEvents = message.events
		m.questionTurnID = message.turnID
		m.input.Reset()
		m.input.Placeholder = "Type a custom answer…"
		m.status = "Choose an option or type a custom answer"
		m.resize(m.width, m.height)
		if len(m.pendingQuestion.Choices) > 0 {
			m.questionViewport.GotoBottom()
		} else {
			m.questionViewport.GotoTop()
		}
		return m, m.input.Focus()
	}
	if event.message != nil {
		m.streamResponse = ""
		m.messages = append(m.messages, *event.message)
		learning := m.observeLearningMessage(*event.message)
		if m.memory != nil {
			m.memory.Append(*event.message)
		}
		if event.message.Role == client.RoleAssistant {
			m.archiveMessage(*event.message, sessionstore.StatusSucceeded, false)
			m.status = "Preparing tool call…"
		} else {
			status := sessionstore.StatusSucceeded
			if event.toolIsError {
				status = sessionstore.StatusFailed
			}
			m.archiveMessage(*event.message, status, event.toolIsError)
			m.status = "Thinking… · " + event.message.Name + " completed"
		}
		m.refreshTranscript()
		return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID), learning)
	}
	return m.Update(chatResultMsg{
		turnID:   message.turnID,
		response: event.response, requestEstimate: event.requestEstimate,
		toolCalls: event.toolCalls, err: event.err,
	})
}

func (m model) updateChatResult(message chatResultMsg) (tea.Model, tea.Cmd) {
	if message.turnID != 0 && message.turnID != m.turnID {
		return m, nil
	}
	m.finishTurn()
	m.waiting = false
	m.compacting = false
	m.asking = false
	m.pendingQuestion = askToUserInput{}
	m.questionAnswer = nil
	m.questionEvents = nil
	m.questionTurnID = 0
	m.pendingMessage = client.Message{}
	m.streamResponse = ""
	if message.err != nil {
		m.compactionTarget = 0
		m.archiveFailure("chat", message.err)
		if archiveErr := m.flushArchive(); archiveErr != nil {
			m.status = message.err.Error() + " · archive: " + archiveErr.Error()
			m.offerPlanExecutionResume()
			return m, m.input.Focus()
		}
		m.status = message.err.Error()
		m.offerPlanExecutionResume()
		return m, m.input.Focus()
	}
	if message.response == nil || len(message.response.Choices) == 0 {
		m.compactionTarget = 0
		err := errors.New("provider returned no choices")
		m.archiveFailure("chat", err)
		m.status = "provider returned no choices"
		if archiveErr := m.flushArchive(); archiveErr != nil {
			m.status += " · archive: " + archiveErr.Error()
		}
		m.offerPlanExecutionResume()
		return m, m.input.Focus()
	}
	assistant := message.response.Choices[0].Message
	if assistant.Role == "" {
		assistant.Role = client.RoleAssistant
	}
	if message.response.ConversationID != "" {
		m.conversationID = message.response.ConversationID
	}
	m.messages = append(m.messages, message.intermediate...)
	m.messages = append(m.messages, assistant)
	learningCommands := make([]tea.Cmd, 0, len(message.intermediate)+2)
	for _, intermediate := range message.intermediate {
		learningCommands = append(learningCommands, m.observeLearningMessage(intermediate))
	}
	learningCommands = append(learningCommands, m.observeLearningMessage(assistant))
	for _, intermediate := range message.intermediate {
		m.archiveMessage(intermediate, sessionstore.StatusSucceeded, false)
	}
	m.archiveMessage(assistant, sessionstore.StatusSucceeded, false)
	if m.memory != nil {
		for _, intermediate := range message.intermediate {
			m.memory.Append(intermediate)
		}
		requestEstimate := message.requestEstimate
		if requestEstimate == 0 {
			requestEstimate = m.requestEstimate
		}
		m.memory.ObserveUsage(message.response.Usage.PromptTokens, requestEstimate)
		m.memory.Append(assistant)
	}
	if m.compactionTarget > 0 && message.response.Usage.PromptTokens > m.compactionTarget {
		m.status = fmt.Sprintf("Context remains above target · %s/%s", formatTokens(message.response.Usage.PromptTokens), formatTokens(m.compactionTarget))
	} else {
		m.status = ""
		if message.toolCalls > 0 {
			m.status = fmt.Sprintf("Tools used · %d", message.toolCalls)
		}
	}
	if len(m.agentTraces) > 0 {
		m.agentTraceExpanded = false
		if m.status == "" {
			m.status = "Workflow completed · ctrl+g inspect subagent trace"
		}
	}
	if cacheStatus := promptCacheStatus(message.response.Usage); cacheStatus != "" {
		if m.status != "" {
			m.status += " · "
		}
		m.status += cacheStatus
	}
	m.compactionTarget = 0
	m.sessionUpdatedAt = time.Now().UTC()
	m.resize(m.width, m.height)
	if err := m.saveWorkspaceSession(); err != nil {
		m.status = err.Error()
	}
	if err := m.flushArchive(); err != nil {
		m.status = "archive: " + err.Error()
	}
	focus := m.input.Focus()
	learningCommands = append(learningCommands, focus, m.startNextLearningSegment())
	return m, tea.Batch(learningCommands...)
}

func (m model) updateCompactionResult(message compactionResultMsg) (tea.Model, tea.Cmd) {
	if message.turnID != 0 && message.turnID != m.turnID {
		return m, nil
	}
	if message.err != nil {
		m.rollbackPendingMessage()
		m.archiveFailure("context_compaction", message.err)
		m.status = "compact context: " + message.err.Error()
		if archiveErr := m.flushArchive(); archiveErr != nil {
			m.status += " · archive: " + archiveErr.Error()
		}
		return m, m.input.Focus()
	}
	if message.response == nil || len(message.response.Choices) == 0 {
		m.rollbackPendingMessage()
		err := errors.New("provider returned no choices")
		m.archiveFailure("context_compaction", err)
		m.status = "compact context: provider returned no choices"
		if archiveErr := m.flushArchive(); archiveErr != nil {
			m.status += " · archive: " + archiveErr.Error()
		}
		return m, m.input.Focus()
	}
	checkpoint, err := m.memory.ApplyCheckpoint(message.plan, message.response.Choices[0].Message.TextContent())
	if err != nil {
		m.rollbackPendingMessage()
		m.archiveFailure("context_compaction", err)
		m.status = "compact context: " + err.Error()
		if archiveErr := m.flushArchive(); archiveErr != nil {
			m.status += " · archive: " + archiveErr.Error()
		}
		return m, m.input.Focus()
	}
	m.conversationID = ""
	m.archiveSummary(checkpoint)
	m.compacting = false
	m.compactionTarget = message.plan.TargetTokens
	m.status = fmt.Sprintf("Context compacted · %s → %s", formatTokens(message.plan.BeforeTokens), formatTokens(m.memory.PredictedTokens()))
	if err := m.saveWorkspaceSession(); err != nil {
		m.archiveFailure("context_compaction", err)
		m.status += " · save: " + err.Error()
	}
	return m, m.sendChatRequest()
}
