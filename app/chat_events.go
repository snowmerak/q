package app

import (
	"errors"
	"fmt"
	"strings"
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
		if err := m.saveActiveTask(event.taskStarted); err != nil {
			m.turnErr = errors.Join(m.turnErr, err)
			m.status = err.Error()
		}
		return m, tea.Batch(m.chatTick(), waitAgentEvent(message.events, message.turnID))
	}
	if event.taskCompleted {
		if err := m.saveActiveTask(nil); err != nil {
			m.turnErr = errors.Join(m.turnErr, err)
			m.status = err.Error()
		}
		return m, tea.Batch(m.chatTick(), waitAgentEvent(message.events, message.turnID))
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
		return m, tea.Batch(m.chatTick(), waitAgentEvent(message.events, message.turnID))
	}
	if event.learningName != "" {
		command := m.enqueueLearningSpecial(event.learningName, event.learningPayload)
		return m, tea.Batch(m.chatTick(), waitAgentEvent(message.events, message.turnID), command)
	}
	if event.plan != nil {
		m.status = agentPlanStatus(*event.plan)
		return m, tea.Batch(m.chatTick(), waitAgentEvent(message.events, message.turnID))
	}
	if event.activity != nil {
		m.appendAgentActivity(*event.activity)
		m.status = activityStatus(*event.activity)
		m.resize(m.width, m.height)
		return m, tea.Batch(m.chatTick(), waitAgentEvent(message.events, message.turnID))
	}
	if event.trace != nil {
		m.appendAgentTrace(*event.trace)
		m.resize(m.width, m.height)
		return m, tea.Batch(m.chatTick(), waitAgentEvent(message.events, message.turnID))
	}
	if event.status != "" {
		m.status = event.status
		return m, tea.Batch(m.chatTick(), waitAgentEvent(message.events, message.turnID))
	}
	if event.compaction != nil {
		if err := m.persistAgentCompaction(event); err != nil {
			m.turnErr = errors.Join(m.turnErr, err)
			m.status = "apply agent context compaction: " + err.Error()
			if event.persistenceAck != nil && m.turnCancel != nil {
				m.turnCancel()
			}
			acknowledgeAgentPersistence(event)
		} else {
			m.status = "Context compacted · continuing…"
		}
		return m, tea.Batch(m.chatTick(), waitAgentEvent(message.events, message.turnID))
	}
	if event.contextReplace != nil {
		if m.memory != nil {
			if err := m.memory.Replace(event.contextReplace.Index, event.contextReplace.Message); err != nil {
				m.status = "update model context: " + err.Error()
			}
		}
		return m, tea.Batch(m.chatTick(), waitAgentEvent(message.events, message.turnID))
	}
	if event.call != nil {
		m.archiveToolCall(*event.call)
		m.status = "Running tool · " + describeToolCall(*event.call)
		var learning tea.Cmd
		if event.call.Function.Name == "learn" {
			learning = m.enqueueExplicitLearning()
		}
		return m, tea.Batch(m.chatTick(), waitAgentEvent(message.events, message.turnID), learning)
	}
	if event.question != nil {
		m.asking = true
		m.pendingQuestion = *event.question
		m.questionChoice = 0
		m.questionAnswer = event.answer
		m.questionEvents = message.events
		m.questionTurnID = message.turnID
		if m.headless {
			return m, nil
		}
		m.resetChatInput()
		m.setChatPlaceholder("Type a custom answer…")
		m.status = "Choose an option or type a custom answer"
		m.resize(m.width, m.height)
		if len(m.pendingQuestion.Choices) > 0 {
			m.questionViewport.GotoBottom()
		} else {
			m.questionViewport.GotoTop()
		}
		return m, m.focusChatInput()
	}
	if event.message != nil {
		m.streamResponse = ""
		learning := m.appendSessionMessage(*event.message, event.usage)
		m.archiveSessionMessage(*event.message, event.toolIsError)
		if event.message.Role == client.RoleAssistant {
			m.status = "Preparing tool call…"
		} else {
			m.status = "Thinking… · " + event.message.Name + " completed"
		}
		m.refreshTranscript()
		if err := m.persistAgentMessage(event); err != nil {
			m.turnErr = errors.Join(m.turnErr, err)
			m.status = err.Error()
			if event.persistenceAck != nil && m.turnCancel != nil {
				m.turnCancel()
			}
			acknowledgeAgentPersistence(event)
		}
		return m, tea.Batch(m.chatTick(), waitAgentEvent(message.events, message.turnID), learning)
	}
	return m.updateChatResult(chatResultMsg{
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
		m.turnErr = errors.Join(m.turnErr, message.err)
		m.compactionTarget = 0
		m.archiveFailure("chat", message.err)
		if archiveErr := m.flushArchive(); archiveErr != nil {
			m.turnErr = errors.Join(m.turnErr, archiveErr)
			m.status = message.err.Error() + " · archive: " + archiveErr.Error()
			return m, m.focusChatInput()
		}
		m.status = message.err.Error()
		return m, m.focusChatInput()
	}
	if message.response == nil || len(message.response.Choices) == 0 {
		m.compactionTarget = 0
		err := errors.New("provider returned no choices")
		m.turnErr = errors.Join(m.turnErr, err)
		m.archiveFailure("chat", err)
		m.status = "provider returned no choices"
		if archiveErr := m.flushArchive(); archiveErr != nil {
			m.turnErr = errors.Join(m.turnErr, archiveErr)
			m.status += " · archive: " + archiveErr.Error()
		}
		return m, m.focusChatInput()
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
	m.recordResponseUsage(message.response.Usage)
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
	if m.sessionOperation != nil {
		operation := *m.sessionOperation
		operation.Completed = true
		operation.Result = strings.TrimSpace(assistant.TextContent())
		m.sessionOperation = &operation
	}
	m.resize(m.width, m.height)
	if err := m.saveWorkspaceSession(); err != nil {
		m.turnErr = errors.Join(m.turnErr, err)
		m.status = err.Error()
	}
	if err := m.flushArchive(); err != nil {
		m.turnErr = errors.Join(m.turnErr, err)
		m.status = "archive: " + err.Error()
	}
	focus := m.focusChatInput()
	learningCommands = append(learningCommands, focus, m.startNextLearningSegment())
	return m, tea.Batch(learningCommands...)
}

func (m model) updateCompactionResult(message compactionResultMsg) (tea.Model, tea.Cmd) {
	if message.turnID != 0 && message.turnID != m.turnID {
		return m, nil
	}
	if message.err != nil {
		m.turnErr = errors.Join(m.turnErr, message.err)
		m.rollbackPendingMessage()
		m.archiveFailure("context_compaction", message.err)
		m.status = "compact context: " + message.err.Error()
		if archiveErr := m.flushArchive(); archiveErr != nil {
			m.turnErr = errors.Join(m.turnErr, archiveErr)
			m.status += " · archive: " + archiveErr.Error()
		}
		return m, m.focusChatInput()
	}
	if message.checkpoint == "" && (message.response == nil || len(message.response.Choices) == 0) {
		m.rollbackPendingMessage()
		err := errors.New("provider returned no choices")
		m.turnErr = errors.Join(m.turnErr, err)
		m.archiveFailure("context_compaction", err)
		m.status = "compact context: provider returned no choices"
		if archiveErr := m.flushArchive(); archiveErr != nil {
			m.turnErr = errors.Join(m.turnErr, archiveErr)
			m.status += " · archive: " + archiveErr.Error()
		}
		return m, m.focusChatInput()
	}
	checkpointText := message.checkpoint
	if checkpointText == "" {
		checkpointText = message.response.Choices[0].Message.TextContent()
	}
	compactedMemory, checkpoint, err := m.memory.CheckpointCopy(message.plan, checkpointText)
	if err != nil {
		m.turnErr = errors.Join(m.turnErr, err)
		m.rollbackPendingMessage()
		m.archiveFailure("context_compaction", err)
		m.status = "compact context: " + err.Error()
		if archiveErr := m.flushArchive(); archiveErr != nil {
			m.turnErr = errors.Join(m.turnErr, archiveErr)
			m.status += " · archive: " + archiveErr.Error()
		}
		return m, m.focusChatInput()
	}
	if m.pendingMessageDeferred && !message.manual {
		compactedMemory.Append(m.pendingMessage)
	}
	candidate := m
	candidate.memory = compactedMemory
	if candidate.sessionOperation != nil && !message.manual {
		operation := *candidate.sessionOperation
		operation.ContextReady = true
		candidate.sessionOperation = &operation
	}
	candidate.conversationID = ""
	if err := candidate.saveWorkspaceSession(); err != nil {
		m.turnErr = errors.Join(m.turnErr, err)
		m.rollbackPendingMessage()
		m.archiveFailure("context_compaction", err)
		m.status = "compact context: " + err.Error()
		if archiveErr := m.flushArchive(); archiveErr != nil {
			m.turnErr = errors.Join(m.turnErr, archiveErr)
			m.status += " · archive: " + archiveErr.Error()
		}
		return m, m.focusChatInput()
	}
	m.memory = compactedMemory
	m.sessionOperation = candidate.sessionOperation
	m.pendingMessageDeferred = false
	m.conversationID = ""
	m.archiveSummary(checkpoint)
	m.compacting = false
	if message.manual {
		m.compactionTarget = 0
	} else {
		m.compactionTarget = message.plan.TargetTokens
	}
	m.status = fmt.Sprintf("Context compacted · %s → %s", formatTokens(message.plan.BeforeTokens), formatTokens(m.memory.PredictedTokens()))
	if message.manual {
		m.finishTurn()
		m.waiting = false
		if err := m.flushArchive(); err != nil {
			m.turnErr = errors.Join(m.turnErr, err)
			m.status += " · archive: " + err.Error()
		}
		return m, m.focusChatInput()
	}
	return m, m.sendChatRequest()
}
