package app

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"github.com/snowmerak/q/agentinstructions"
	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/sessionstore"
	"golang.org/x/text/unicode/norm"
	"strings"
	"time"
)

func (m model) updateChatKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.handleSlashCompletionKey(key) {
		return m, nil
	}
	if key.String() == "ctrl+o" {
		m.toggleToolResults()
		return m, nil
	}
	if key.String() == "ctrl+g" && len(m.agentTraces) > 0 {
		m.agentTraceExpanded = !m.agentTraceExpanded
		m.resize(m.width, m.height)
		return m, nil
	}
	if m.asking && len(m.pendingQuestion.Choices) > 0 && strings.TrimSpace(m.input.Value()) == "" {
		choiceCount := questionChoiceCount(m.pendingQuestion)
		switch key.String() {
		case "up", "shift+tab":
			m.questionChoice = (m.questionChoice - 1 + choiceCount) % choiceCount
			m.refreshQuestion()
			m.questionViewport.GotoBottom()
			return m, nil
		case "down", "tab":
			m.questionChoice = (m.questionChoice + 1) % choiceCount
			m.refreshQuestion()
			m.questionViewport.GotoBottom()
			return m, nil
		}
	}
	if m.asking && isQuestionScrollKey(key.String()) {
		var command tea.Cmd
		m.questionViewport, command = m.questionViewport.Update(key)
		return m, command
	}
	if !m.asking && m.agentTraceExpanded && len(m.agentTraces) > 0 && isAgentTraceScrollKey(key.String()) {
		var command tea.Cmd
		m.agentTraceViewport, command = m.agentTraceViewport.Update(key)
		return m, command
	}
	switch key.String() {
	case "esc":
		return m, tea.Quit
	case "ctrl+l":
		if !m.waiting && !m.planResumePending {
			if remote, ok := m.client.(*acpRemoteClient); ok {
				m.status = "Starting new ACP session…"
				return m, remote.resetSessionCommand(m.ctx)
			}
			m.resetConversation()
		}
		return m, nil
	case "ctrl+p":
		if !m.waiting {
			if _, ok := m.client.(*acpRemoteClient); ok {
				m.status = "Model and mode are controlled by the connected ACP agent"
				return m, nil
			}
			if m.runtime != nil {
				m.enterGatewaySettings()
				return m, nil
			}
			m.enterSetup(m.config)
			return m, m.setup[m.setupFocus].Focus()
		}
		return m, nil
	case "ctrl+s":
		return m.submitChat()
	case "enter":
		return m.deferChatSubmit()
	}
	var commands []tea.Cmd
	var command tea.Cmd
	m.viewport, command = m.viewport.Update(key)
	commands = append(commands, command)
	if !m.waiting || m.asking {
		m.input, command = m.input.Update(key)
		m.syncSlashCompletion()
		commands = append(commands, command)
		if m.asking && !m.pendingQuestion.ChoiceOnly && len(m.pendingQuestion.Choices) > 0 && strings.TrimSpace(m.input.Value()) != "" &&
			!customAnswerSelected(m.pendingQuestion, m.questionChoice) {
			m.questionChoice = len(m.pendingQuestion.Choices)
			m.refreshQuestion()
			m.questionViewport.GotoBottom()
		}
	}
	return m, tea.Batch(commands...)
}

func (m model) deferChatSubmit() (tea.Model, tea.Cmd) {
	if (m.waiting && !m.asking) || m.submitPending || m.client == nil {
		return m, nil
	}
	m.submitPending = true
	return m, tea.Tick(imeCommitGracePeriod, func(time.Time) tea.Msg {
		return deferredSubmitMsg{}
	})
}

func (m model) submitChat() (tea.Model, tea.Cmd) {
	m.submitPending = false
	content := strings.TrimSpace(norm.NFC.String(m.input.Value()))
	if m.asking {
		return m.submitQuestionAnswer(content)
	}
	if m.waiting || content == "" || m.client == nil {
		return m, nil
	}
	_, remoteChat := m.client.(*acpRemoteClient)
	if remoteChat && content == "/new" {
		m.input.Reset()
		m.status = "Starting new ACP session…"
		return m, m.client.(*acpRemoteClient).resetSessionCommand(m.ctx)
	}
	if !remoteChat {
		if updated, command, handled := m.startSkillCommand(content); handled {
			return updated, command
		}
		if command, handled := parsePlanAutomationCommand(content); handled {
			m.input.Reset()
			m.status = m.runPlanAutomationCommand(command)
			return m, m.input.Focus()
		}
		if customCommand(content) {
			return m.startCustom(content)
		}
		switch content {
		case "/plan":
			m.input.Reset()
			m.planArmed = true
			m.input.Placeholder = "Describe the work to plan…"
			m.status = "Plan mode · enter a planning request"
			m.resize(m.width, m.height)
			return m, m.input.Focus()
		case "/commit":
			return m.startCommit()
		case "/changes":
			return m.enterChanges()
		case "/clear":
			m.input.Reset()
			m.resetConversation()
			return m, m.input.Focus()
		case "/new":
			m.input.Reset()
			if err := m.startNewWorkspaceSession(); err != nil {
				m.status = err.Error()
			}
			return m, m.input.Focus()
		case "/sessions":
			m.input.Reset()
			return m.enterSessions()
		case "/learn":
			m.input.Reset()
			if m.learningDisabled() {
				m.status = "Learning is disabled for this workspace · /learn on to enable"
				return m, m.input.Focus()
			}
			m.status = "Learning checkpoint enqueued"
			return m, tea.Batch(m.input.Focus(), m.enqueueExplicitLearning())
		case "/learn off":
			m.input.Reset()
			if err := m.setWorkspaceLearningDisabled(true); err != nil {
				m.status = err.Error()
				return m, m.input.Focus()
			}
			m.status = "Learning disabled for this workspace"
			return m, m.input.Focus()
		case "/learn on":
			m.input.Reset()
			if err := m.setWorkspaceLearningDisabled(false); err != nil {
				m.status = err.Error()
				return m, m.input.Focus()
			}
			m.status = "Learning enabled for this workspace"
			return m, tea.Batch(m.input.Focus(), m.startNextLearningSegment())
		case "/learn status":
			m.input.Reset()
			if m.learningDisabled() {
				m.status = "Learning is disabled for this workspace"
			} else {
				m.status = "Learning is enabled for this workspace"
			}
			return m, m.input.Focus()
		case "/model":
			m.input.Reset()
			return m.discoverCurrentModels()
		case "/gateway":
			m.input.Reset()
			if m.runtime != nil {
				m.enterGatewaySettings()
				return m, nil
			}
			m.enterSetup(m.config)
			return m, m.setup[m.setupFocus].Focus()
		case "/library":
			m.input.Reset()
			return m, m.enterLibrarySettings()
		case "/loom":
			m.input.Reset()
			return m.enterLoom()
		case "/ignore":
			m.input.Reset()
			return m.enterIgnore()
		case "/skills":
			m.input.Reset()
			return m.enterSkills()
		case "/lsp":
			m.input.Reset()
			return m.enterLSP()
		case "/mcp":
			m.input.Reset()
			return m.enterMCP()
		case "/help":
			m.input.Reset()
			return m.enterHelp()
		}
		if strings.HasPrefix(content, "/plan ") {
			return m.startPlan(strings.TrimSpace(strings.TrimPrefix(content, "/plan")))
		}
		if m.planArmed {
			return m.startPlan(content)
		}
	}
	return m.startChatTurn(content, !remoteChat)
}

func (m model) startChatTurn(content string, compact bool) (tea.Model, tea.Cmd) {
	content = strings.TrimSpace(norm.NFC.String(content))
	if m.waiting || content == "" || m.client == nil {
		return m, nil
	}
	m.clearAgentActivities()
	m.resize(m.width, m.height)
	m.beginTurn()
	m.turnMessageStart = len(m.messages)
	m.touchSessionMetadata(content)
	userMessage := client.Message{Role: client.RoleUser, Content: content}
	m.archiveMessage(userMessage, sessionstore.StatusSubmitted, false)
	m.messages = append(m.messages, userMessage)
	learning := m.observeLearningMessage(userMessage)
	if m.memory == nil {
		m.memory = memory.New(memoryPolicy(m.activeConfig()), nil)
	}
	m.memory.Append(userMessage)
	m.pendingMessage = userMessage
	m.input.Reset()
	m.input.Blur()
	m.waiting = true
	m.refreshTranscript()
	if compact && m.memory.ShouldCompact() {
		plan, err := m.memory.Plan()
		if err != nil {
			m.rollbackPendingMessage()
			m.archiveFailure("context_compaction", err)
			m.status = err.Error()
			if archiveErr := m.flushArchive(); archiveErr != nil {
				m.status += " · archive: " + archiveErr.Error()
			}
			return m, m.input.Focus()
		}
		m.compacting = true
		m.status = "Compacting context…"
		return m, tea.Batch(m.spinner.Tick, m.compactContext(plan), learning)
	}
	m.status = "Thinking…"
	return m, tea.Batch(m.spinner.Tick, m.sendChatRequest(), learning)
}

func (m model) submitQuestionAnswer(content string) (tea.Model, tea.Cmd) {
	if m.planResumePending {
		return m.submitPlanResumeAnswer(content)
	}
	if m.questionAnswer == nil || m.questionEvents == nil {
		return m, nil
	}
	var answer askToUserOutput
	if content == "" {
		if len(m.pendingQuestion.Choices) == 0 {
			return m, nil
		}
		if customAnswerSelected(m.pendingQuestion, m.questionChoice) {
			m.status = "Type a custom answer below"
			m.input.Placeholder = "Type a custom answer…"
			return m, m.input.Focus()
		}
		choice := m.pendingQuestion.Choices[min(max(m.questionChoice, 0), len(m.pendingQuestion.Choices)-1)]
		answer = askToUserOutput{SelectedChoiceID: choice.ID}
	} else if customAnswerSelected(m.pendingQuestion, m.questionChoice) {
		answer = askToUserOutput{Freeform: content}
	} else {
		answer = answerForQuestion(m.pendingQuestion, content)
		if m.pendingQuestion.ChoiceOnly && answer.SelectedChoiceID == "" {
			m.status = "Choose one of the available permission options"
			m.input.Reset()
			return m, m.input.Focus()
		}
	}
	answerChannel := m.questionAnswer
	events := m.questionEvents
	turnID := m.questionTurnID
	turnContext := m.activeTurnContext()
	m.asking = false
	m.planArmed = false
	m.pendingQuestion = askToUserInput{}
	m.questionChoice = 0
	m.questionAnswer = nil
	m.questionEvents = nil
	m.questionTurnID = 0
	m.input.Reset()
	m.input.Placeholder = "Type a message…"
	m.input.Blur()
	m.status = "Thinking…"
	m.resize(m.width, m.height)
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		select {
		case answerChannel <- answer:
			return waitAgentEvent(events, turnID)()
		case <-turnContext.Done():
			return agentEventMsg{events: events, event: agentEvent{err: turnContext.Err()}, turnID: turnID}
		}
	})
}

func (m *model) beginTurn() {
	if m.turnCancel != nil {
		m.turnCancel()
	}
	m.turnID++
	m.streamResponse = ""
	m.turnContext, m.turnCancel = context.WithCancel(m.ctx)
}

func (m *model) finishTurn() {
	if m.turnCancel != nil {
		m.turnCancel()
	}
	m.turnContext = nil
	m.turnCancel = nil
	m.turnMessageStart = 0
}

func (m model) activeTurnContext() context.Context {
	if m.turnContext != nil {
		return m.turnContext
	}
	return m.ctx
}

func (m model) interruptTurn() (tea.Model, tea.Cmd) {
	if !m.waiting {
		return m, nil
	}
	if m.turnCancel != nil {
		m.turnCancel()
	}
	m.turnID++
	m.completeInterruptedToolCalls()
	m.archiveTurnCancelled("interrupted by user")
	m.turnContext = nil
	m.turnCancel = nil
	m.turnMessageStart = 0
	m.waiting = false
	m.compacting = false
	m.asking = false
	m.planArmed = false
	m.submitPending = false
	m.pendingMessage = client.Message{}
	m.streamResponse = ""
	m.pendingQuestion = askToUserInput{}
	m.questionAnswer = nil
	m.questionEvents = nil
	m.questionTurnID = 0
	m.conversationID = ""
	m.compactionTarget = 0
	m.input.Reset()
	m.input.Placeholder = "Type a message…"
	m.status = "Turn interrupted"
	m.sessionUpdatedAt = time.Now().UTC()
	m.resize(m.width, m.height)
	if err := m.saveWorkspaceSession(); err != nil {
		m.status += " · " + err.Error()
	}
	if err := m.flushArchive(); err != nil {
		m.status += " · archive: " + err.Error()
	}
	m.offerPlanExecutionResume()
	return m, m.input.Focus()
}

func (m *model) completeInterruptedToolCalls() {
	start := min(max(m.turnMessageStart, 0), len(m.messages))
	completed := make(map[string]struct{})
	for _, message := range m.messages[start:] {
		if message.Role == client.RoleTool && message.ToolCallID != "" {
			completed[message.ToolCallID] = struct{}{}
		}
	}
	for _, message := range m.messages[start:] {
		for _, call := range message.ToolCalls {
			if call.ID == "" {
				continue
			}
			if _, found := completed[call.ID]; found {
				continue
			}
			m.archiveToolCall(call)
			cancelled := client.Message{
				Role: client.RoleTool, Name: call.Function.Name, ToolCallID: call.ID,
				Content: "Tool error: interrupted by user",
			}
			m.messages = append(m.messages, cancelled)
			if m.memory != nil {
				m.memory.Append(cancelled)
			}
			m.archiveMessage(cancelled, sessionstore.StatusCancelled, true)
			completed[call.ID] = struct{}{}
		}
	}
	m.refreshTranscript()
}

func (m model) compactContext(plan memory.Plan) tea.Cmd {
	configuredClient := m.client
	modelID := m.activeModel()
	reasoningEffort := m.activeConfig().Provider.EffectiveReasoningEffort()
	turnContext := m.activeTurnContext()
	turnID := m.turnID
	return func() tea.Msg {
		response, err := chatWithEmptyResponseRecovery(turnContext, configuredClient, client.ChatRequest{
			Model: modelID, Messages: plan.RequestMessages(),
			ReasoningEffort: reasoningEffort,
		})
		return compactionResultMsg{turnID: turnID, response: response, plan: plan, err: err}
	}
}

func (m *model) sendChatRequest() tea.Cmd {
	history := m.memory.Messages()
	m.requestEstimate = memory.CountMessages(history)
	conversationID := m.conversationID
	configuredClient := m.client
	modelID := m.activeModel()
	reasoningEffort := m.activeConfig().Provider.EffectiveReasoningEffort()
	workingDirectory := ""
	if m.workspaceStore != nil {
		workingDirectory = m.workspaceStore.Root
	}
	toolRuntime, toolRuntimeErr := configuredAgentToolRuntime(
		m.toolRuntime, mcpconfig.RoleDefault, m.activeConfig(), workingDirectory,
	)
	if toolRuntimeErr == nil {
		toolRuntime, toolRuntimeErr = m.configuredDelegationRuntime(toolRuntime, workingDirectory)
	}
	turnContext := m.activeTurnContext()
	turnID := m.turnID
	streamEnabled := m.streamsActiveChat()
	coalesceInstructions := modelNeedsSystemInstructionCoalescing(
		m.gatewayConfig, m.activeConfig().ModelGroups, modelID, nil,
	)
	activeTask := cloneActiveTask(m.activeTask)
	if toolRuntimeErr != nil {
		return func() tea.Msg {
			return chatResultMsg{turnID: turnID, requestEstimate: m.requestEstimate, err: toolRuntimeErr}
		}
	}
	if remote, ok := configuredClient.(*acpRemoteClient); ok {
		events := make(chan agentEvent)
		content := m.pendingMessage.TextContent()
		return func() tea.Msg {
			go remote.runPrompt(turnContext, content, events)
			return waitAgentEvent(events, turnID)()
		}
	}
	if toolRuntime == nil && !streamEnabled {
		return func() tea.Msg {
			response, err := chatWithEmptyResponseRecovery(turnContext, configuredClient, client.ChatRequest{
				Model: modelID, Messages: providerMessages(agentinstructions.Normalize(history), coalesceInstructions), ConversationID: conversationID,
				ReasoningEffort: reasoningEffort, WorkingDirectory: workingDirectory,
			})
			return chatResultMsg{turnID: turnID, response: response, requestEstimate: m.requestEstimate, err: err}
		}
	}
	events := make(chan agentEvent)
	return func() tea.Msg {
		if toolRuntime == nil && streamEnabled {
			go streamSingleChat(turnContext, configuredClient, modelID, reasoningEffort, history, conversationID, workingDirectory, coalesceInstructions, m.requestEstimate, events)
		} else {
			go RunAgentLoop(turnContext, AgentLoopRequest{
				Client: configuredClient, Tools: toolRuntime, Model: modelID, ReasoningEffort: reasoningEffort,
				Messages: history, ConversationID: conversationID, WorkingDirectory: workingDirectory,
				ActiveTask: activeTask, Stream: streamEnabled, CoalesceInstructions: coalesceInstructions,
				ContextPolicy: memoryPolicy(m.activeConfig()),
			}, events)
		}
		return waitAgentEvent(events, turnID)()
	}
}

func streamSingleChat(
	ctx context.Context,
	configuredClient chatClient,
	modelID, reasoningEffort string,
	history []client.Message,
	conversationID string,
	workingDirectory string,
	coalesceInstructions bool,
	requestEstimate int,
	events chan<- agentEvent,
) {
	defer close(events)
	response, err := streamChatWithEmptyResponseRecovery(ctx, configuredClient, client.ChatRequest{
		Model: modelID, Messages: providerMessages(agentinstructions.Normalize(history), coalesceInstructions), ConversationID: conversationID,
		ReasoningEffort: reasoningEffort, WorkingDirectory: workingDirectory,
	}, func(delta chatStreamDelta) bool {
		return emitAgentEvent(ctx, events, agentEvent{streamDelta: &delta})
	})
	emitAgentEvent(ctx, events, agentEvent{response: response, complete: true, requestEstimate: requestEstimate, err: err})
}

func waitAgentEvent(events <-chan agentEvent, turnID uint64) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			event.err = errors.New("agent event stream closed unexpectedly")
		}
		return agentEventMsg{events: events, event: event, turnID: turnID}
	}
}

func providerMessages(messages []client.Message, coalesceInstructions bool) []client.Message {
	return agentloop.ProviderMessages(messages, coalesceInstructions)
}

func (m *model) rollbackPendingMessage() {
	m.finishTurn()
	if m.pendingMessage.Content != "" {
		if len(m.messages) > 0 {
			m.messages = m.messages[:len(m.messages)-1]
		}
		if m.memory != nil {
			m.memory.PopLast()
		}
		m.input.SetValue(m.pendingMessage.Content)
	}
	m.pendingMessage = client.Message{}
	m.streamResponse = ""
	m.waiting = false
	m.compacting = false
	m.asking = false
	m.planArmed = false
	m.pendingQuestion = askToUserInput{}
	m.questionAnswer = nil
	m.questionEvents = nil
	m.questionTurnID = 0
	m.compactionTarget = 0
	m.submitPending = false
	m.refreshTranscript()
}
