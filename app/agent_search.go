package app

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/coder/acp-go-sdk"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
)

const agentSearchCommand = "/agent:search"

func parseAgentSearchCommand(command string) (query string, handled bool) {
	command = strings.TrimSpace(command)
	if command == agentSearchCommand {
		return "", true
	}
	if !strings.HasPrefix(command, agentSearchCommand+" ") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(command, agentSearchCommand)), true
}

func explicitAgentSearchInput(query string) subagent.ExternalSearchInput {
	return subagent.ExternalSearchInput{
		Query: strings.TrimSpace(query),
		CompletionCriteria: []string{
			"Return a concise evidence-backed answer with direct source URLs.",
		},
	}
}

func (m model) startAgentSearch(query string) (tea.Model, tea.Cmd) {
	query = strings.TrimSpace(query)
	if query == "" {
		m.status = "Usage: /agent:search <query>"
		return m, m.input.Focus()
	}
	workingDirectory := ""
	if m.workspaceStore != nil {
		workingDirectory = m.workspaceStore.Root
	}
	search := configuredExternalSearch(m.activeConfig(), workingDirectory)
	if search == nil {
		m.status = "Search agent is not configured · assign it with /agents"
		return m, m.input.Focus()
	}

	m.beginTurn()
	m.turnMessageStart = len(m.messages)
	m.touchSessionMetadata(query)
	message := client.Message{Role: client.RoleUser, Content: query}
	m.archiveMessage(message, sessionstore.StatusSubmitted, false)
	m.messages = append(m.messages, message)
	learning := m.observeLearningMessage(message)
	if m.memory == nil {
		m.memory = memory.New(memoryPolicy(m.activeConfig()), nil)
	}
	m.memory.Append(message)
	m.pendingMessage = message
	m.input.Reset()
	m.input.Blur()
	m.waiting = true
	m.status = "Starting Search agent…"
	m.clearAgentActivities()
	m.resize(m.width, m.height)
	m.refreshTranscript()
	if err := m.saveWorkspaceSession(); err != nil {
		m.finishTurn()
		m.waiting = false
		m.pendingMessage = client.Message{}
		m.status = err.Error()
		return m, m.input.Focus()
	}
	return m, tea.Batch(m.spinner.Tick, m.sendAgentSearch(search, query), learning)
}

func (m *model) sendAgentSearch(search subagent.ExternalSearchFunc, query string) tea.Cmd {
	turnContext := m.activeTurnContext()
	turnID := m.turnID
	events := make(chan agentEvent)
	return func() tea.Msg {
		go streamAgentSearch(turnContext, search, query, events)
		return waitAgentEvent(events, turnID)()
	}
}

func streamAgentSearch(
	ctx context.Context,
	search subagent.ExternalSearchFunc,
	query string,
	events chan<- agentEvent,
) {
	defer close(events)
	if search == nil {
		emitAgentEvent(ctx, events, agentEvent{err: errors.New("search agent is not configured")})
		return
	}
	started := agentActivity{Agent: "search", Action: subagent.ProgressStarted, Detail: query}
	if !emitAgentEvent(ctx, events, agentEvent{activity: &started}) {
		return
	}
	result, err := search(ctx, explicitAgentSearchInput(query))
	if err != nil {
		failed := agentActivity{Agent: "search", Action: subagent.ProgressFailed, Detail: err.Error()}
		emitAgentEvent(ctx, events, agentEvent{activity: &failed})
		emitAgentEvent(ctx, events, agentEvent{err: err})
		return
	}
	completed := agentActivity{Agent: "search", Action: subagent.ProgressCompleted, Detail: "external evidence received"}
	if !emitAgentEvent(ctx, events, agentEvent{activity: &completed}) {
		return
	}
	emitAgentEvent(ctx, events, agentEvent{response: &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, Content: result.Summary,
	}}}}})
}

func (a *acpAgent) runACPAgentSearch(ctx context.Context, query string) (acp.PromptResponse, error) {
	search := a.externalSearch
	if search == nil {
		message := "Search agent is not configured. Assign an enabled ACP connection to the search role with q agents."
		if err := a.updateContext(ctx, acp.UpdateAgentMessageText(message)); err != nil {
			return acp.PromptResponse{}, err
		}
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}

	a.state.turnMessageStart = len(a.state.messages)
	titleChanged := a.state.touchSessionMetadata(query)
	message := client.Message{Role: client.RoleUser, Content: query}
	a.state.archiveMessage(message, sessionstore.StatusSubmitted, false)
	a.state.messages = append(a.state.messages, message)
	if a.state.memory == nil {
		a.state.memory = memory.New(memoryPolicy(a.state.activeConfig()), nil)
	}
	a.state.memory.Append(message)
	a.launchLearning(a.state.observeLearningMessage(message))
	if err := a.state.saveWorkspaceSession(); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.emitSessionInfo(titleChanged); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.updateContext(ctx, acp.UpdateAgentThoughtText("Search agent started.\n")); err != nil {
		return acp.PromptResponse{}, err
	}

	result, err := search(ctx, explicitAgentSearchInput(query))
	if err != nil {
		a.state.archiveFailure("ACP agent search failed", err)
		_ = a.state.flushArchive()
		return acp.PromptResponse{}, err
	}
	streamedResponse := ""
	return a.finishPrompt(client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, Content: result.Summary,
	}}}}, 0, &streamedResponse)
}
