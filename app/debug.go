package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
)

type debugExecutionStore interface {
	ArchiveDebugExecution(subagent.DebugExecutionLog) (string, error)
}

func debugDefaultResolver(ctx context.Context, question subagent.UserQuestion) (subagent.UserAnswer, error) {
	if err := ctx.Err(); err != nil {
		return subagent.UserAnswer{Source: subagent.UserAnswerSourceAutoResolve}, err
	}
	parts := []string{
		"Resolve this diagnostic uncertainty autonomously instead of asking the user again.",
		"Question: " + strings.TrimSpace(question.Question),
	}
	if contextValue := strings.TrimSpace(question.Context); contextValue != "" {
		parts = append(parts, "Context: "+contextValue)
	}
	parts = append(parts,
		"Treat evidence from the current repository as primary: inspect the relevant code, configuration, tests, and history through Scout and the available repository tools before settling the answer.",
		"When Agent Skills are available and relevant, search for and load the smallest applicable set, then use their procedural guidance as additional evidence.",
		"Combine that repository- and skill-specific evidence with established general diagnostic patterns and common solutions, adapting the result to the repository's existing architecture and conventions rather than copying a generic fix blindly.",
		"Prefer an evidence-backed likely cause, the smallest safe fix direction, and a concrete verification method. If a private organizational policy, internal API contract, or operational fact cannot be established, do not invent it; record a bounded assumption, the remaining uncertainty, and the evidence needed to confirm or refute it.",
		"Continue the investigation as needed, then commit the concrete resolution to the debug brief and report instead of deferring the decision back to the user.",
	)
	return subagent.UserAnswer{
		Freeform: strings.Join(parts, "\n"),
		Source:   subagent.UserAnswerSourceAutoResolve,
	}, nil
}

func (m model) startDebug(issue string) (tea.Model, tea.Cmd) {
	issue = strings.TrimSpace(issue)
	if issue == "" || m.client == nil || m.toolRuntime == nil {
		m.status = "Debug mode requires an issue and an available tool runtime"
		return m, m.input.Focus()
	}
	m.debugArmed = false
	m.planArmed = false
	m.input.Placeholder = "Type a message…"
	m.beginTurn()
	m.turnMessageStart = len(m.messages)
	m.touchSessionMetadata(issue)
	message := client.Message{Role: client.RoleUser, Content: issue}
	m.archiveMessage(message, sessionstore.StatusSubmitted, false)
	m.messages = append(m.messages, message)
	learning := m.observeLearningMessage(message)
	if m.memory == nil {
		m.memory = memoryForPlan(m.activeConfig())
	}
	m.memory.Append(message)
	m.pendingMessage = message
	m.input.Reset()
	m.input.Blur()
	m.waiting = true
	m.status = "Investigating issue…"
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
	return m, tea.Batch(m.spinner.Tick, m.sendDebugRequest(issue), learning)
}

func (m *model) sendDebugRequest(issue string) tea.Cmd {
	configuredClient := m.client
	toolRuntime := m.toolRuntime
	value := m.activeConfig()
	runID := m.runID
	archive := m.archive
	turnContext := m.activeTurnContext()
	turnID := m.turnID
	workingDirectory := ""
	var executionStore debugExecutionStore
	if m.workspaceStore != nil {
		workingDirectory = m.workspaceStore.Root
		executionStore = m.workspaceStore
	}
	var history []client.Message
	if m.memory != nil {
		history = m.memory.Messages()
	}
	events := make(chan agentEvent)
	return func() tea.Msg {
		go streamDebugWorkflow(
			turnContext, configuredClient, toolRuntime, value, runID, archive,
			workingDirectory, issue, planContext(history), executionStore, events,
		)
		return waitAgentEvent(events, turnID)()
	}
}

func streamDebugWorkflow(
	ctx context.Context,
	configuredClient chatClient,
	toolRuntime agentToolRuntime,
	value config.Config,
	runID string,
	archive recordArchive,
	workingDirectory string,
	issue string,
	contextValues []string,
	executionStore debugExecutionStore,
	events chan<- agentEvent,
) {
	defer close(events)
	debugRecorder := newDebugLogRecorder(runID, issue, contextValues)
	var result subagent.DebugWorkflowResult
	fail := func(err error) {
		outcome := subagent.DebugOutcomeFailed
		if errors.Is(err, context.Canceled) {
			outcome = subagent.DebugOutcomeCanceled
		}
		execution := debugRecorder.finishDebug(result, outcome, err)
		if executionStore != nil {
			if _, archiveErr := executionStore.ArchiveDebugExecution(execution); archiveErr != nil {
				err = errors.Join(err, archiveErr)
			}
		}
		emitAgentEvent(ctx, events, agentEvent{err: err})
	}
	models, err := configuredClient.ListModels(ctx)
	if err != nil {
		fail(fmt.Errorf("debug: list models: %w", err))
		return
	}
	grillerSpec, err := subagent.Resolve(value, config.AgentRoleGriller, models)
	if err != nil {
		fail(err)
		return
	}
	scoutSpec, err := subagent.Resolve(value, config.AgentRoleScout, models)
	if err != nil {
		fail(err)
		return
	}
	interactiveAsk := subagent.AskUserFunc(func(ctx context.Context, question subagent.UserQuestion) (subagent.UserAnswer, error) {
		input := askToUserInput{Question: question.Question, Context: question.Context}
		for _, choice := range question.Choices {
			input.Choices = append(input.Choices, askToUserChoice{
				ID: choice.ID, Label: choice.Label, Description: choice.Description,
			})
		}
		answerChannel := make(chan askToUserOutput, 1)
		if !emitAgentEvent(ctx, events, agentEvent{question: &input, answer: answerChannel}) {
			err := ctx.Err()
			if err == nil {
				err = errors.New("debug: user interaction ended")
			}
			return subagent.UserAnswer{Source: subagent.UserAnswerSourceUser}, err
		}
		select {
		case answer := <-answerChannel:
			if answer.Err != nil {
				return subagent.UserAnswer{Source: subagent.UserAnswerSourceUser}, answer.Err
			}
			return subagent.UserAnswer{
				SelectedChoiceID: answer.SelectedChoiceID,
				Freeform:         answer.Freeform,
				Source:           subagent.UserAnswerSourceUser,
			}, nil
		case <-ctx.Done():
			return subagent.UserAnswer{Source: subagent.UserAnswerSourceUser}, ctx.Err()
		}
	})
	clarificationDelegate := interactiveAsk
	if value.Plan.AutoResolve {
		clarificationDelegate = debugDefaultResolver
	}
	clarificationAsk := subagent.AskUserFunc(func(ctx context.Context, question subagent.UserQuestion) (subagent.UserAnswer, error) {
		debugRecorder.recordQuestion(question)
		answer, err := clarificationDelegate(ctx, question)
		debugRecorder.recordAnswer(answer, err)
		return answer, err
	})
	progress := func(progress subagent.ProgressEvent) {
		debugRecorder.recordProgress(progress)
		activity := agentActivity{
			Agent: progress.Agent, TaskID: progress.TaskID, ParentID: progress.ParentID,
			Action: progress.Action, Detail: progress.Detail,
		}
		_ = emitAgentEvent(ctx, events, agentEvent{activity: &activity})
	}
	trace := func(event subagent.TraceEvent) {
		debugRecorder.recordTrace(event)
		entry := agentTrace{
			Agent: event.Agent, TaskID: event.TaskID, ParentID: event.ParentID,
			Kind: event.Kind, CallID: event.CallID, Name: event.Name, Content: event.Content, IsError: event.IsError,
		}
		_ = emitAgentEvent(ctx, events, agentEvent{trace: &entry})
	}
	grillerTools, err := configuredAgentToolRuntime(toolRuntime, grillerSpec.Role, value, workingDirectory)
	if err != nil {
		fail(fmt.Errorf("debug: configure Griller tools: %w", err))
		return
	}
	scoutRunID := runID
	if archive == nil {
		scoutRunID = ""
	}
	scout := subagent.ScoutRunner{
		Client: configuredClient, Tools: scopeTools(toolRuntime, scoutSpec.Role), Spec: scoutSpec,
		Sink: archive, RunID: scoutRunID, WorkingDirectory: workingDirectory, Progress: progress, Trace: trace,
	}
	griller := subagent.GrillerRunner{
		Client: configuredClient, Tools: grillerTools, Scout: scout, Spec: grillerSpec,
		Ask: clarificationAsk, Capture: configuredInvocationCapture(toolRuntime), WorkingDirectory: workingDirectory,
		AutoResolve: value.Plan.AutoResolve, Progress: progress, Trace: trace,
	}
	taskID := "debug"
	if strings.TrimSpace(runID) != "" {
		taskID += "-" + runID
	}
	debugContext := append([]string(nil), contextValues...)
	debugContext = append(debugContext,
		"This is a /debug investigation. Bound the reported behavior, evidence needed for diagnosis, and safe verification criteria; do not turn it into an implementation plan.",
	)
	result, err = (subagent.DebugWorkflow{
		Griller: griller, Progress: progress,
	}).Run(ctx, subagent.GrillTask{
		ID: taskID, Objective: issue, Context: debugContext,
	})
	if err != nil {
		fail(err)
		return
	}
	execution := debugRecorder.finishDebug(result, subagent.DebugOutcomeSucceeded, nil)
	if executionStore != nil {
		if _, err := executionStore.ArchiveDebugExecution(execution); err != nil {
			emitAgentEvent(ctx, events, agentEvent{err: err})
			return
		}
	}
	content := subagent.RenderDebugReport(result.Report)
	emitAgentEvent(ctx, events, agentEvent{response: &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, Content: content,
	}}}}})
}
