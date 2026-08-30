package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/workspace"
	"github.com/snowmerak/q/workspacememory"
)

// sprintModel runs the ordinary plan state machine without rendering the TUI.
// It terminates on the first final response or error and treats any unexpected
// user question as a failed autonomous run instead of hanging for input.
type sprintModel struct {
	state   model
	initial tea.Cmd
	output  io.Writer
	err     error
}

func (m sprintModel) Init() tea.Cmd { return m.initial }

func (m sprintModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	eventMessage, isAgentEvent := message.(agentEventMsg)
	if !isAgentEvent {
		updated, command := m.state.Update(message)
		m.state = updated.(model)
		return m, command
	}
	event := eventMessage.event
	if event.question != nil {
		m.err = errors.New("autonomous plan unexpectedly requested user input")
		if event.answer != nil {
			event.answer <- askToUserOutput{Err: m.err}
		}
		updated, _ := m.state.Update(chatResultMsg{turnID: eventMessage.turnID, err: m.err})
		m.state = updated.(model)
		return m, tea.Quit
	}
	if event.err != nil || event.response != nil {
		updated, _ := m.state.Update(message)
		m.state = updated.(model)
		m.err = event.err
		if m.err != nil {
			return m, tea.Quit
		}
		if event.response == nil || len(event.response.Choices) == 0 {
			m.err = errors.New("plan returned no response")
			return m, tea.Quit
		}
		content := strings.TrimSpace(event.response.Choices[0].Message.TextContent())
		if content != "" && m.output != nil {
			fmt.Fprintln(m.output, content)
		}
		return m, tea.Quit
	}

	updated, command := m.state.Update(message)
	m.state = updated.(model)
	if progress := sprintProgress(event); progress != "" && m.output != nil {
		fmt.Fprintln(m.output, progress)
	}
	return m, command
}

func (m sprintModel) View() tea.View { return tea.NewView("") }

func sprintProgress(event agentEvent) string {
	switch {
	case event.plan != nil:
		return agentPlanStatus(*event.plan)
	case event.activity != nil:
		return activityStatus(*event.activity)
	case event.status != "":
		return event.status
	default:
		return ""
	}
}

func sprintConfig(value config.Config) config.Config {
	value.Plan.AutoApprove = true
	value.Plan.AutoResolve = true
	return value
}

// RunSprint executes one autonomous plan in a fresh durable workspace session.
// Plan automation is applied only to the in-memory config used by this run.
func RunSprint(
	ctx context.Context,
	store config.Store,
	workspaceStore workspace.Store,
	objective string,
	output io.Writer,
) (returnErr error) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return errors.New("requirement must not be empty")
	}
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
	for _, warning := range sprintStartupWarnings(startup) {
		fmt.Fprintln(output, "Warning:", warning)
	}

	sessionStore, lock, err := workspace.CreateSession(workspaceStore.Root, "q sprint")
	if err != nil {
		return err
	}
	sessionLock = lock
	value := sprintConfig(startup.config)
	state := newManagedModel(runtimeContext, store, factory, manager)
	state.workspaceStore = &sessionStore
	state.toolRuntime = startup.tools
	state.libraryClient = startup.library
	state.archive = startup.archive
	state.archiveSearch = startup.archiveSearch
	state.archiveErr = startup.archiveErr
	state.models = append(state.models, startup.models...)
	state.gatewayConfig = startup.gatewayConfig
	state.enterChat(value, configuredClient)
	updated, planCommand := state.startPlan(objective)
	state = updated.(model)
	if !state.waiting || planCommand == nil {
		if state.status == "" {
			state.status = "plan did not start"
		}
		return errors.New(state.status)
	}

	fmt.Fprintf(output, "Sprint started · %s\n", objective)
	final, runErr := tea.NewProgram(
		sprintModel{state: state, initial: planCommand, output: output},
		tea.WithContext(runtimeContext), tea.WithInput(nil), tea.WithOutput(output),
		tea.WithoutRenderer(), tea.WithoutSignalHandler(),
	).Run()
	var sprintErr error
	if result, ok := final.(sprintModel); ok {
		sprintErr = result.err
	}
	return errors.Join(runErr, sprintErr)
}

func sprintStartupWarnings(result runtimeInitializedMsg) []error {
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

func RunSprintDefault(ctx context.Context, objective string, output io.Writer) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	workspaceStore, err := workspace.DefaultStore()
	if err != nil {
		return err
	}
	if err := RunSprint(ctx, store, workspaceStore, objective, output); err != nil {
		return fmt.Errorf("q sprint: %w", err)
	}
	return nil
}
