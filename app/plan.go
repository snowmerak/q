package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

type planExecutionStore interface {
	SaveExecution(subagent.ExecutionCheckpoint) error
	ClearExecution() error
}

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
	return m, tea.Batch(m.spinner.Tick, m.sendPlanRequest(objective))
}

func memoryForPlan(value config.Config) *memory.Manager {
	return memory.New(memoryPolicy(value), nil)
}

func (m *model) offerPlanExecutionResume() {
	if m.workspaceStore == nil || m.waiting || m.planResumePending {
		return
	}
	checkpoint, err := m.workspaceStore.LoadExecution()
	if errors.Is(err, workspace.ErrExecutionNotFound) {
		return
	}
	if err != nil {
		if m.status != "" {
			m.status += " · "
		}
		m.status += err.Error()
		return
	}
	m.planCheckpoint = checkpoint
	m.planResumePending = true
	m.asking = true
	m.questionChoice = 0
	m.pendingQuestion = askToUserInput{
		Question: "Resume interrupted plan?",
		Context:  renderPlanRecoveryContext(checkpoint, false),
		Choices: []askToUserChoice{
			{ID: "resume", Label: "Resume", Description: "Continue from the last durable checkpoint."},
			{ID: "inspect", Label: "Inspect", Description: "Show the full saved plan and recovery state first."},
			{ID: "discard", Label: "Discard", Description: "Remove the checkpoint without reverting workspace changes."},
		},
	}
	if checkpoint.RunID != "" {
		m.runID = checkpoint.RunID
	}
	m.input.Reset()
	m.input.Placeholder = "Type a custom answer…"
	if m.status != "" {
		m.status += " · resumable plan found"
	} else {
		m.status = "Resumable plan found"
	}
	m.resize(m.width, m.height)
	m.questionViewport.GotoBottom()
}

func (m model) submitPlanResumeAnswer(content string) (tea.Model, tea.Cmd) {
	decision := strings.ToLower(strings.TrimSpace(content))
	if decision == "" {
		if len(m.pendingQuestion.Choices) == 0 || customAnswerSelected(m.pendingQuestion, m.questionChoice) {
			m.status = "Choose resume, inspect, or discard"
			return m, m.input.Focus()
		}
		decision = m.pendingQuestion.Choices[min(max(m.questionChoice, 0), len(m.pendingQuestion.Choices)-1)].ID
	}
	switch decision {
	case "inspect":
		m.pendingQuestion.Context = renderPlanRecoveryContext(m.planCheckpoint, true)
		m.questionChoice = 0
		m.input.Reset()
		m.status = "Saved execution details"
		m.resize(m.width, m.height)
		m.questionViewport.GotoBottom()
		return m, m.input.Focus()
	case "discard":
		if m.workspaceStore == nil {
			m.status = "Workspace storage is unavailable"
			return m, m.input.Focus()
		}
		if err := m.workspaceStore.ClearExecution(); err != nil {
			m.status = err.Error()
			return m, m.input.Focus()
		}
		m.planResumePending = false
		m.planCheckpoint = subagent.ExecutionCheckpoint{}
		m.asking = false
		m.pendingQuestion = askToUserInput{}
		m.questionChoice = 0
		m.input.Reset()
		m.input.Placeholder = "Type a message…"
		m.status = "Saved plan discarded · workspace changes were kept"
		m.resize(m.width, m.height)
		return m, m.input.Focus()
	case "resume":
		return m.resumePlanExecution()
	default:
		m.status = "Choose resume, inspect, or discard"
		return m, m.input.Focus()
	}
}

func (m model) resumePlanExecution() (tea.Model, tea.Cmd) {
	if m.workspaceStore == nil || m.client == nil || m.toolRuntime == nil {
		m.status = "Plan resume requires workspace storage, a model, and tools"
		return m, m.input.Focus()
	}
	checkpoint, err := subagent.PrepareExecutionResume(m.planCheckpoint)
	if err != nil {
		m.status = err.Error()
		return m, m.input.Focus()
	}
	if err := m.workspaceStore.SaveExecution(checkpoint); err != nil {
		m.status = err.Error()
		return m, m.input.Focus()
	}
	m.planResumePending = false
	m.planCheckpoint = subagent.ExecutionCheckpoint{}
	m.asking = false
	m.pendingQuestion = askToUserInput{}
	m.questionChoice = 0
	m.questionAnswer = nil
	m.questionEvents = nil
	m.questionTurnID = 0
	m.input.Reset()
	m.input.Placeholder = "Type a message…"
	m.input.Blur()
	m.beginTurn()
	m.turnMessageStart = len(m.messages)
	m.waiting = true
	m.pendingMessage = client.Message{}
	taskLabel := "completed"
	if checkpoint.TaskIndex < len(checkpoint.Plan.Steps) {
		taskLabel = fmt.Sprintf("task %d/%d", checkpoint.TaskIndex+1, len(checkpoint.Plan.Steps))
	}
	m.status = fmt.Sprintf("Resuming plan · %s · %s", taskLabel, checkpoint.Phase)
	m.clearAgentActivities()
	m.resize(m.width, m.height)
	return m, tea.Batch(m.spinner.Tick, m.sendPlanResumeRequest(checkpoint))
}

func (m *model) sendPlanResumeRequest(checkpoint subagent.ExecutionCheckpoint) tea.Cmd {
	configuredClient := m.client
	toolRuntime := m.toolRuntime
	value := m.config
	archive := m.archive
	turnContext := m.activeTurnContext()
	turnID := m.turnID
	workingDirectory := ""
	var executionStore planExecutionStore
	if m.workspaceStore != nil {
		workingDirectory = m.workspaceStore.Root
		executionStore = m.workspaceStore
	}
	events := make(chan agentEvent)
	return func() tea.Msg {
		go streamPlanResume(
			turnContext, configuredClient, toolRuntime, value, archive, workingDirectory,
			checkpoint, executionStore, events,
		)
		return waitAgentEvent(events, turnID)()
	}
}

func streamPlanResume(
	ctx context.Context,
	configuredClient chatClient,
	toolRuntime agentToolRuntime,
	value config.Config,
	archive recordArchive,
	workingDirectory string,
	checkpoint subagent.ExecutionCheckpoint,
	executionStore planExecutionStore,
	events chan<- agentEvent,
) {
	defer close(events)
	models, err := configuredClient.ListModels(ctx)
	if err != nil {
		emitAgentEvent(ctx, events, agentEvent{err: fmt.Errorf("resume plan: list models: %w", err)})
		return
	}
	progress := func(event subagent.ProgressEvent) {
		activity := agentActivity{
			Agent: event.Agent, TaskID: event.TaskID, ParentID: event.ParentID,
			Action: event.Action, Detail: event.Detail,
		}
		_ = emitAgentEvent(ctx, events, agentEvent{activity: &activity})
	}
	trace := func(event subagent.TraceEvent) {
		entry := agentTrace{
			Agent: event.Agent, TaskID: event.TaskID, ParentID: event.ParentID,
			Kind: event.Kind, Name: event.Name, Content: event.Content, IsError: event.IsError,
		}
		_ = emitAgentEvent(ctx, events, agentEvent{trace: &entry})
	}
	runID := checkpoint.RunID
	if archive == nil {
		runID = ""
	}
	progress(subagent.ProgressEvent{Agent: "executor", Action: subagent.ProgressStarted, Detail: "resuming approved plan"})
	execution, err := executeApprovedPlan(
		ctx, configuredClient, toolRuntime, value, models, archive, runID,
		workingDirectory, checkpoint, executionStore, progress, trace,
	)
	if err != nil {
		progress(subagent.ProgressEvent{Agent: "executor", Action: subagent.ProgressFailed, Detail: err.Error()})
		emitAgentEvent(ctx, events, agentEvent{err: err})
		return
	}
	progress(subagent.ProgressEvent{Agent: "executor", Action: subagent.ProgressCompleted, Detail: "resumed plan executed"})
	content := subagent.RenderPlanExecutionResult(execution)
	emitAgentEvent(ctx, events, agentEvent{response: &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, Content: content,
	}}}}})
}

func renderPlanRecoveryContext(checkpoint subagent.ExecutionCheckpoint, detailed bool) string {
	task := "completed"
	if checkpoint.TaskIndex < len(checkpoint.Plan.Steps) {
		task = fmt.Sprintf("%d/%d · %s", checkpoint.TaskIndex+1, len(checkpoint.Plan.Steps), checkpoint.Plan.Steps[checkpoint.TaskIndex].Title)
	}
	body := fmt.Sprintf(
		"A durable plan checkpoint was found.\n\nSummary: %s\nTask: %s\nPhase: %s\nCompleted: %d/%d\nCoder results: %d\nRecovery count: %d\n\nDiscarding removes only the checkpoint; it does not revert files or commands already changed by a Coder.",
		checkpoint.Plan.Summary, task, checkpoint.Phase, checkpoint.CompletedTasks,
		len(checkpoint.Plan.Steps), checkpoint.Attempts, checkpoint.ResumeCount,
	)
	if detailed {
		body += "\n\nSaved plan:\n" + subagent.RenderPlanProposal(checkpoint.Plan)
		if len(checkpoint.Targets) > 0 {
			body += "\n\nResolved targets:\n- " + strings.Join(checkpoint.Targets, "\n- ")
		}
		if strings.TrimSpace(checkpoint.Feedback) != "" {
			body += "\n\nRetry feedback:\n" + checkpoint.Feedback
		}
	}
	return body
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
	var executionStore planExecutionStore
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
		go streamPlanWorkflow(
			turnContext, configuredClient, toolRuntime, value, runID, archive,
			workingDirectory, objective, planContext(history), executionStore, events,
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
	executionStore planExecutionStore,
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
	progress := func(progress subagent.ProgressEvent) {
		activity := agentActivity{
			Agent: progress.Agent, TaskID: progress.TaskID, ParentID: progress.ParentID,
			Action: progress.Action, Detail: progress.Detail,
		}
		_ = emitAgentEvent(ctx, events, agentEvent{activity: &activity})
	}
	trace := func(event subagent.TraceEvent) {
		entry := agentTrace{
			Agent: event.Agent, TaskID: event.TaskID, ParentID: event.ParentID,
			Kind: event.Kind, Name: event.Name, Content: event.Content, IsError: event.IsError,
		}
		_ = emitAgentEvent(ctx, events, agentEvent{trace: &entry})
	}

	scoutRunID := runID
	if archive == nil {
		scoutRunID = ""
	}
	scout := subagent.ScoutRunner{
		Client: configuredClient, Tools: toolRuntime, Spec: scoutSpec,
		Sink: archive, RunID: scoutRunID, WorkingDirectory: workingDirectory, Progress: progress, Trace: trace,
	}
	griller := subagent.GrillerRunner{
		Client: configuredClient, Tools: toolRuntime, Scout: scout, Spec: grillerSpec,
		Ask: ask, WorkingDirectory: workingDirectory, Progress: progress, Trace: trace,
	}
	planner := subagent.PlannerRunner{
		Client: configuredClient, Tools: toolRuntime, Spec: plannerSpec, WorkingDirectory: workingDirectory,
		Progress: progress, Trace: trace,
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
		executionID, idErr := sessionstore.NewID()
		if idErr != nil {
			emitAgentEvent(ctx, events, agentEvent{err: fmt.Errorf("plan: create execution ID: %w", idErr)})
			return
		}
		executionID = "plan-execution-" + executionID
		checkpoint := subagent.NewExecutionCheckpoint(result.Plan)
		checkpoint.ExecutionID = executionID
		checkpoint.RunID = runID
		checkpoint.Objective = objective
		if executionStore != nil {
			if saveErr := executionStore.SaveExecution(checkpoint); saveErr != nil {
				emitAgentEvent(ctx, events, agentEvent{err: saveErr})
				return
			}
		}
		progress(subagent.ProgressEvent{Agent: "executor", Action: subagent.ProgressStarted, Detail: "executing approved plan"})
		execution, executionErr := executeApprovedPlan(
			ctx, configuredClient, toolRuntime, value, models, archive, scoutRunID,
			workingDirectory, checkpoint, executionStore, progress, trace,
		)
		if executionErr != nil {
			progress(subagent.ProgressEvent{Agent: "executor", Action: subagent.ProgressFailed, Detail: executionErr.Error()})
			emitAgentEvent(ctx, events, agentEvent{err: executionErr})
			return
		}
		progress(subagent.ProgressEvent{Agent: "executor", Action: subagent.ProgressCompleted, Detail: "approved plan executed"})
		content = subagent.RenderPlanExecutionResult(execution)
	}
	progress(subagent.ProgressEvent{Agent: "plan", Action: subagent.ProgressCompleted, Detail: "plan workflow finished"})
	response := &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, Content: content,
	}}}}
	emitAgentEvent(ctx, events, agentEvent{response: response})
}

func executeApprovedPlan(
	ctx context.Context,
	configuredClient chatClient,
	toolRuntime agentToolRuntime,
	value config.Config,
	models []client.Model,
	archive recordArchive,
	runID string,
	workingDirectory string,
	checkpoint subagent.ExecutionCheckpoint,
	executionStore planExecutionStore,
	progress subagent.ProgressFunc,
	trace subagent.TraceFunc,
) (subagent.PlanExecutionResult, error) {
	coderSpec, err := subagent.Resolve(value, config.AgentRoleCoder, models)
	if err != nil {
		return subagent.PlanExecutionResult{}, err
	}
	plannerSpec, err := subagent.Resolve(value, config.AgentRolePlanner, models)
	if err != nil {
		return subagent.PlanExecutionResult{}, err
	}
	environment := toolRuntime.Environment()
	coder := subagent.CoderRunner{
		Client: configuredClient, Tools: toolRuntime, Spec: coderSpec,
		Sink: archive, RunID: runID, ExecutionID: checkpoint.ExecutionID,
		WorkingDirectory: workingDirectory, Progress: progress, Trace: trace,
		Environment: fmt.Sprintf(
			"Runtime environment: OS=%s; architecture=%s; run_command shell=%s. Use commands and quoting compatible with this shell. For repository discovery, never traverse q's .q metadata directory and honor the workspace-root .qignore file, including in shell scans.",
			environment.OS, environment.Architecture, environment.Shell,
		),
	}
	reviewer := subagent.PlannerReviewRunner{
		Client: configuredClient, Tools: toolRuntime, Spec: plannerSpec,
		Sink: archive, RunID: runID, ExecutionID: checkpoint.ExecutionID,
		WorkingDirectory: workingDirectory, Progress: progress, Trace: trace,
	}
	loop := subagent.ExecutionLoop{
		Resolver: subagent.TargetResolver{Tools: toolRuntime},
		Coder:    coder.Run, Review: reviewer.Run, Progress: progress,
	}
	if executionStore != nil {
		loop.Checkpoint = func(_ context.Context, checkpoint subagent.ExecutionCheckpoint) error {
			return executionStore.SaveExecution(checkpoint)
		}
	}
	execution, err := loop.RunFrom(ctx, checkpoint)
	if err != nil {
		return execution, err
	}
	if executionStore != nil {
		if err := executionStore.ClearExecution(); err != nil {
			return execution, err
		}
	}
	return execution, nil
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
