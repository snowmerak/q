package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/changes"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
	"github.com/snowmerak/q/workspacememory"
)

const (
	defaultReviewRequest  = "Review the current working-tree changes for correctness, regressions, security risks, and missing tests."
	maximumReviewPatchSet = 512 << 10
)

type reviewExecutionStore interface {
	ArchiveReviewExecution(subagent.ReviewExecutionLog) (string, error)
}

func normalizeReviewRequest(request string) string {
	request = strings.TrimSpace(request)
	if request == "" {
		return defaultReviewRequest
	}
	return request
}

func collectReviewRequest(ctx context.Context, directory, objective, runID string) (subagent.ReviewRequest, error) {
	objective = normalizeReviewRequest(objective)
	snapshot, err := changes.List(ctx, directory)
	if err != nil {
		return subagent.ReviewRequest{}, err
	}
	if len(snapshot.Files) == 0 {
		return subagent.ReviewRequest{}, errors.New("review: working tree has no changes")
	}
	request := subagent.ReviewRequest{
		ID: strings.TrimSpace(runID), Objective: objective,
		Files: make([]subagent.ReviewFile, 0, len(snapshot.Files)),
	}
	remaining := maximumReviewPatchSet
	for _, file := range snapshot.Files {
		detail, err := changes.Read(ctx, snapshot.Root, file)
		if err != nil {
			return subagent.ReviewRequest{}, fmt.Errorf("review: read %s: %w", file.Path, err)
		}
		reviewed := subagent.ReviewFile{Path: file.Path, OldPath: file.OldPath, Status: file.Status}
		for _, section := range detail.Sections {
			patch := section.Patch
			truncated := section.Truncated
			if remaining <= 0 {
				patch = "Patch omitted because the aggregate review input reached its safety limit."
				truncated = true
			} else if len(patch) > remaining {
				patch = truncateUTF8(patch, remaining)
				truncated = true
				remaining = 0
			} else {
				remaining -= len(patch)
			}
			reviewed.Sections = append(reviewed.Sections, subagent.ReviewPatch{
				Title: section.Title, Patch: patch, Truncated: truncated,
			})
		}
		request.Files = append(request.Files, reviewed)
	}
	return request, nil
}

func (m model) startReview(requestText string) (tea.Model, tea.Cmd) {
	if m.client == nil || m.toolRuntime == nil || m.workspaceStore == nil {
		m.status = "Review requires an available model, tool runtime, and workspace"
		return m, m.input.Focus()
	}
	requestText = normalizeReviewRequest(requestText)
	m.planArmed = false
	m.debugArmed = false
	m.input.Placeholder = "Type a message…"
	m.beginTurn()
	m.turnMessageStart = len(m.messages)
	m.touchSessionMetadata(requestText)
	message := client.Message{Role: client.RoleUser, Content: requestText}
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
	m.status = "Reviewing working-tree changes…"
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
	return m, tea.Batch(m.spinner.Tick, m.sendReviewRequest(requestText), learning)
}

func (m *model) sendReviewRequest(requestText string) tea.Cmd {
	configuredClient := m.client
	toolRuntime := m.toolRuntime
	value := m.activeConfig()
	runID := m.runID
	archive := m.archive
	turnContext := m.activeTurnContext()
	turnID := m.turnID
	workingDirectory := m.workspaceStore.Root
	var executionStore reviewExecutionStore = m.workspaceStore
	events := make(chan agentEvent)
	return func() tea.Msg {
		go streamReviewWorkflow(
			turnContext, configuredClient, toolRuntime, value, runID, archive,
			workingDirectory, requestText, executionStore, events,
		)
		return waitAgentEvent(events, turnID)()
	}
}

func streamReviewWorkflow(
	ctx context.Context,
	configuredClient chatClient,
	toolRuntime agentToolRuntime,
	value config.Config,
	runID string,
	archive recordArchive,
	workingDirectory string,
	requestText string,
	executionStore reviewExecutionStore,
	events chan<- agentEvent,
) {
	defer close(events)
	reviewRequest, err := collectReviewRequest(ctx, workingDirectory, requestText, runID)
	if err != nil {
		emitAgentEvent(ctx, events, agentEvent{err: err})
		return
	}
	recorder := newReviewLogRecorder(runID, reviewRequest.Objective, reviewRequest.Files)
	result := subagent.ReviewWorkflowResult{}
	fail := func(err error) {
		outcome := subagent.ReviewOutcomeFailed
		if errors.Is(err, context.Canceled) {
			outcome = subagent.ReviewOutcomeCanceled
		}
		execution := recorder.finishReview(result, outcome, err)
		if executionStore != nil {
			if _, archiveErr := executionStore.ArchiveReviewExecution(execution); archiveErr != nil {
				err = errors.Join(err, archiveErr)
			}
		}
		emitAgentEvent(ctx, events, agentEvent{err: err})
	}
	models, err := configuredClient.ListModels(ctx)
	if err != nil {
		fail(fmt.Errorf("review: list models: %w", err))
		return
	}
	advisorSpec, err := subagent.Resolve(value, config.AgentRoleAdvisor, models)
	if err != nil {
		fail(err)
		return
	}
	scoutSpec, err := subagent.Resolve(value, config.AgentRoleScout, models)
	if err != nil {
		fail(err)
		return
	}
	advisorTools, err := configuredAgentToolRuntime(toolRuntime, advisorSpec.Role, value, workingDirectory)
	if err != nil {
		fail(fmt.Errorf("review: configure Advisor tools: %w", err))
		return
	}
	progress := func(progress subagent.ProgressEvent) {
		recorder.recordProgress(progress)
		activity := agentActivity{
			Agent: progress.Agent, TaskID: progress.TaskID, ParentID: progress.ParentID,
			Action: progress.Action, Detail: progress.Detail,
		}
		_ = emitAgentEvent(ctx, events, agentEvent{activity: &activity})
	}
	trace := func(event subagent.TraceEvent) {
		recorder.recordTrace(event)
		entry := agentTrace{
			Agent: event.Agent, TaskID: event.TaskID, ParentID: event.ParentID,
			Kind: event.Kind, CallID: event.CallID, Name: event.Name, Content: event.Content, IsError: event.IsError,
		}
		_ = emitAgentEvent(ctx, events, agentEvent{trace: &entry})
	}
	scoutRunID := runID
	if archive == nil {
		scoutRunID = ""
	}
	scout := subagent.ScoutRunner{
		Client: configuredClient, Tools: scopeTools(toolRuntime, scoutSpec.Role), Spec: scoutSpec,
		Sink: archive, RunID: scoutRunID, WorkingDirectory: workingDirectory,
		Progress: progress, Trace: trace,
	}
	report, err := (subagent.ReviewRunner{
		Client: configuredClient, Tools: advisorTools, Scout: scout, Spec: advisorSpec,
		Capture: configuredInvocationCapture(toolRuntime), WorkingDirectory: workingDirectory,
		Progress: progress, Trace: trace,
	}).Run(ctx, reviewRequest)
	if err != nil {
		fail(err)
		return
	}
	result.Report = report
	execution := recorder.finishReview(result, subagent.ReviewOutcomeSucceeded, nil)
	if executionStore != nil {
		if _, err := executionStore.ArchiveReviewExecution(execution); err != nil {
			emitAgentEvent(ctx, events, agentEvent{err: err})
			return
		}
	}
	content := subagent.RenderReviewReport(report)
	emitAgentEvent(ctx, events, agentEvent{response: &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, Content: content,
	}}}}})
}

// RunReview performs one read-only working-tree review in a fresh durable
// session for the standalone CLI command.
func RunReview(
	ctx context.Context,
	store config.Store,
	workspaceStore workspace.Store,
	requestText string,
	output io.Writer,
) (returnErr error) {
	requestText = normalizeReviewRequest(requestText)
	if output == nil {
		output = io.Discard
	}
	loaded, err := store.Load()
	if errors.Is(err, config.ErrNotFound) {
		return errors.New("q is not configured; run `q` first")
	}
	if err != nil {
		return err
	}
	if err := workspaceStore.MigrateLegacySession(); err != nil {
		return err
	}
	reviewRequest, err := collectReviewRequest(ctx, workspaceStore.Root, requestText, "")
	if err != nil {
		return err
	}

	runtimeContext, cancelRuntime := context.WithCancel(ctx)
	providerContext, cancelProvider := context.WithCancel(context.WithoutCancel(ctx))
	memoryContext, cancelMemory := context.WithCancel(context.WithoutCancel(ctx))
	memoryDone := make(chan error, 1)
	go func() { memoryDone <- workspacememory.Run(memoryContext, store.Dir, io.Discard) }()
	libraryDone := make(chan error, 1)
	go func() { libraryDone <- qlibrary.Run(runtimeContext, store.Dir, io.Discard) }()

	lifecycle := newStartupLifecycle()
	var configuredClient chatClient
	var sessionLock *workspace.Lock
	var manager *providerhost.Manager
	defer func() {
		cancelRuntime()
		lifecycle.waitIfStarted()
		resourcesErr := lifecycle.closeResources()
		var clientErr error
		if configuredClient != nil {
			clientErr = configuredClient.Close()
		} else if startupClient := lifecycle.startupClient(); startupClient != nil {
			clientErr = startupClient.Close()
		}
		cancelProvider()
		cancelMemory()
		memoryErr := <-memoryDone
		libraryErr := <-libraryDone
		var lockErr, managerErr error
		if sessionLock != nil {
			lockErr = sessionLock.Close()
		}
		if manager != nil {
			managerErr = manager.Close()
		}
		returnErr = errors.Join(returnErr, resourcesErr, clientErr, memoryErr, libraryErr, lockErr, managerErr)
	}()

	manager, err = providerhost.NewManager(providerContext, providerhost.Store{Dir: store.Dir})
	if err != nil {
		return err
	}
	factory := managedClientFactory(manager)
	startup := startupRequest{
		ctx: runtimeContext, store: store, workspaceStore: workspaceStore,
		memoryCtx: memoryContext, loaded: loaded, manager: manager,
		factory: factory, lifecycle: lifecycle,
	}.run(nil)
	if startup.err != nil {
		return startup.err
	}
	if startup.client == nil || startup.tools == nil {
		return errors.Join(errors.New("model or workspace tools are unavailable"), startup.startupErr)
	}
	configuredClient = startup.client
	for _, warning := range reviewStartupWarnings(startup) {
		fmt.Fprintln(output, "Warning:", warning)
	}

	sessionStore, lock, err := workspace.CreateSession(workspaceStore.Root, "q review")
	if err != nil {
		return err
	}
	sessionLock = lock
	reviewRequest.ID = sessionStore.SessionID
	advisorSpec, err := subagent.Resolve(startup.config, config.AgentRoleAdvisor, startup.models)
	if err != nil {
		return err
	}
	scoutSpec, err := subagent.Resolve(startup.config, config.AgentRoleScout, startup.models)
	if err != nil {
		return err
	}
	advisorTools, err := configuredAgentToolRuntime(
		startup.tools, advisorSpec.Role, startup.config, workspaceStore.Root,
	)
	if err != nil {
		return fmt.Errorf("review: configure Advisor tools: %w", err)
	}
	recorder := newReviewLogRecorder(sessionStore.SessionID, reviewRequest.Objective, reviewRequest.Files)
	progress := func(event subagent.ProgressEvent) {
		recorder.recordProgress(event)
		status := activityStatus(agentActivity{
			Agent: event.Agent, TaskID: event.TaskID, ParentID: event.ParentID,
			Action: event.Action, Detail: event.Detail,
		})
		if status != "" {
			fmt.Fprintln(output, status)
		}
	}
	trace := func(event subagent.TraceEvent) { recorder.recordTrace(event) }
	scoutRunID := sessionStore.SessionID
	if startup.archive == nil {
		scoutRunID = ""
	}
	scout := subagent.ScoutRunner{
		Client: configuredClient, Tools: scopeTools(startup.tools, scoutSpec.Role), Spec: scoutSpec,
		Sink: startup.archive, RunID: scoutRunID, WorkingDirectory: workspaceStore.Root,
		Progress: progress, Trace: trace,
	}
	fmt.Fprintf(output, "Review started · %d changed file(s) · %s\n", len(reviewRequest.Files), reviewRequest.Objective)
	result := subagent.ReviewWorkflowResult{}
	report, runErr := (subagent.ReviewRunner{
		Client: configuredClient, Tools: advisorTools, Scout: scout, Spec: advisorSpec,
		Capture: configuredInvocationCapture(startup.tools), WorkingDirectory: workspaceStore.Root,
		Progress: progress, Trace: trace,
	}).Run(runtimeContext, reviewRequest)
	if runErr != nil {
		outcome := subagent.ReviewOutcomeFailed
		if errors.Is(runErr, context.Canceled) {
			outcome = subagent.ReviewOutcomeCanceled
		}
		execution := recorder.finishReview(result, outcome, runErr)
		if _, archiveErr := sessionStore.ArchiveReviewExecution(execution); archiveErr != nil {
			runErr = errors.Join(runErr, archiveErr)
		}
		return runErr
	}
	result.Report = report
	execution := recorder.finishReview(result, subagent.ReviewOutcomeSucceeded, nil)
	if _, err := sessionStore.ArchiveReviewExecution(execution); err != nil {
		return err
	}
	fmt.Fprintln(output, subagent.RenderReviewReport(report))
	return nil
}

func reviewStartupWarnings(result runtimeInitializedMsg) []error {
	warnings := make([]error, 0, 3+len(result.mcpStatuses))
	for _, warning := range []error{result.startupErr, result.archiveErr, result.mcpErr} {
		if warning != nil {
			warnings = append(warnings, warning)
		}
	}
	for _, status := range result.mcpStatuses {
		if status.Error != "" {
			warnings = append(warnings, fmt.Errorf("MCP %s: %s", status.ID, status.Error))
		}
	}
	return warnings
}

func RunReviewDefault(ctx context.Context, request string, output io.Writer) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	workspaceStore, err := workspace.DefaultStore()
	if err != nil {
		return err
	}
	if err := RunReview(ctx, store, workspaceStore, request, output); err != nil {
		return fmt.Errorf("q review: %w", err)
	}
	return nil
}
