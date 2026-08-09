package app

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
)

func (m model) startPlan(objective string) (tea.Model, tea.Cmd) {
	objective = strings.TrimSpace(objective)
	if objective == "" || m.client == nil || m.toolRuntime == nil {
		m.status = "Plan mode requires a request and an available tool runtime"
		return m, m.input.Focus()
	}
	m.planArmed = false
	m.input.Placeholder = "Type a message…"
	m.beginTurn()
	m.turnMessageStart = len(m.messages)
	message := client.Message{Role: client.RoleUser, Content: objective}
	m.archiveMessage(message, sessionstore.StatusSubmitted, false)
	m.messages = append(m.messages, message)
	if m.memory == nil {
		m.memory = memoryForPlan(m.config)
	}
	m.memory.Append(message)
	m.pendingMessage = message
	m.input.Reset()
	m.input.Blur()
	m.waiting = true
	m.status = "Grilling planning request…"
	m.refreshTranscript()
	return m, tea.Batch(m.spinner.Tick, m.sendPlanRequest(objective))
}

func memoryForPlan(value config.Config) *memory.Manager {
	return memory.New(memoryPolicy(value), nil)
}

func (m *model) sendPlanRequest(objective string) tea.Cmd {
	configuredClient := m.client
	toolRuntime := m.toolRuntime
	value := m.config
	runID := m.runID
	archive := m.archive
	turnContext := m.activeTurnContext()
	turnID := m.turnID
	workingDirectory := ""
	if m.workspaceStore != nil {
		workingDirectory = m.workspaceStore.Root
	}
	var history []client.Message
	if m.memory != nil {
		history = m.memory.Messages()
	}
	events := make(chan agentEvent)
	return func() tea.Msg {
		go streamPlanWorkflow(
			turnContext, configuredClient, toolRuntime, value, runID, archive,
			workingDirectory, objective, planContext(history), events,
		)
		return waitAgentEvent(events, turnID)()
	}
}

func streamPlanWorkflow(
	ctx context.Context,
	configuredClient chatClient,
	toolRuntime agentToolRuntime,
	value config.Config,
	runID string,
	archive recordArchive,
	workingDirectory string,
	objective string,
	contextValues []string,
	events chan<- agentEvent,
) {
	defer close(events)
	models, err := configuredClient.ListModels(ctx)
	if err != nil {
		emitAgentEvent(ctx, events, agentEvent{err: fmt.Errorf("plan: list models: %w", err)})
		return
	}
	grillerSpec, err := subagent.Resolve(value, config.AgentRoleGriller, models)
	if err != nil {
		emitAgentEvent(ctx, events, agentEvent{err: err})
		return
	}
	scoutSpec, err := subagent.Resolve(value, config.AgentRoleScout, models)
	if err != nil {
		emitAgentEvent(ctx, events, agentEvent{err: err})
		return
	}
	plannerSpec, err := subagent.Resolve(value, config.AgentRolePlanner, models)
	if err != nil {
		emitAgentEvent(ctx, events, agentEvent{err: err})
		return
	}

	ask := func(ctx context.Context, question subagent.UserQuestion) (subagent.UserAnswer, error) {
		input := askToUserInput{Question: question.Question, Context: question.Context}
		for _, choice := range question.Choices {
			input.Choices = append(input.Choices, askToUserChoice{
				ID: choice.ID, Label: choice.Label, Description: choice.Description,
			})
		}
		answerChannel := make(chan askToUserOutput, 1)
		if !emitAgentEvent(ctx, events, agentEvent{question: &input, answer: answerChannel}) {
			return subagent.UserAnswer{}, ctx.Err()
		}
		select {
		case answer := <-answerChannel:
			return subagent.UserAnswer{
				SelectedChoiceID: answer.SelectedChoiceID, Freeform: answer.Freeform,
			}, nil
		case <-ctx.Done():
			return subagent.UserAnswer{}, ctx.Err()
		}
	}
	progress := func(stage, detail string) {
		status := planStatus(stage, detail)
		_ = emitAgentEvent(ctx, events, agentEvent{status: status})
	}

	scout := subagent.ScoutRunner{
		Client: configuredClient, Tools: toolRuntime, Spec: scoutSpec,
		Sink: archive, RunID: runID, WorkingDirectory: workingDirectory,
	}
	griller := subagent.GrillerRunner{
		Client: configuredClient, Tools: toolRuntime, Scout: scout, Spec: grillerSpec,
		Ask: ask, WorkingDirectory: workingDirectory, Progress: progress,
	}
	planner := subagent.PlannerRunner{
		Client: configuredClient, Spec: plannerSpec, WorkingDirectory: workingDirectory,
		Progress: progress,
	}
	workflow := subagent.PlanWorkflow{
		Griller: griller, Planner: planner, Ask: ask, Progress: progress,
	}
	result, err := workflow.Run(ctx, subagent.GrillTask{
		ID: "grill-" + runID, Objective: objective, Context: contextValues,
	})
	if err != nil {
		emitAgentEvent(ctx, events, agentEvent{err: err})
		return
	}
	content := "Planning canceled. No work was executed."
	if result.Approved {
		content = "Plan approved. Execution has not started.\n\n" + subagent.RenderPlanProposal(result.Plan)
	}
	response := &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, Content: content,
	}}}}
	emitAgentEvent(ctx, events, agentEvent{response: response})
}

func planContext(messages []client.Message) []string {
	const maximumMessages = 20
	result := make([]string, 0, min(len(messages), maximumMessages))
	for _, message := range messages {
		if message.Role != client.RoleUser && message.Role != client.RoleAssistant {
			continue
		}
		content := strings.TrimSpace(message.TextContent())
		if content == "" {
			continue
		}
		result = append(result, string(message.Role)+": "+content)
	}
	if len(result) > maximumMessages {
		result = result[len(result)-maximumMessages:]
	}
	return result
}

func planStatus(stage, detail string) string {
	label := stage
	switch stage {
	case "griller":
		label = "Griller"
	case "scout":
		label = "Scout"
	case "planner":
		label = "Planner"
	case "confirm":
		label = "Plan review"
	}
	if strings.TrimSpace(detail) == "" {
		return label + "…"
	}
	return label + " · " + detail
}
