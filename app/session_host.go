package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/hostruntime"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/workspace"
)

// ErrSessionRuntimeUnavailable indicates that a headless session could not
// start because Q's configured model runtime is unavailable.
var ErrSessionRuntimeUnavailable = errors.New("session runtime unavailable")

// SessionEvent is the transport-neutral projection of one default-loop event.
// Studio uses it for streaming, while the ordinary model remains responsible
// for tools, persistence, recovery, and execution order.
type SessionEvent struct {
	Type             string                `json:"type"`
	WorkingDirectory string                `json:"working_directory,omitempty"`
	SessionID        string                `json:"session_id,omitempty"`
	Created          bool                  `json:"created,omitempty"`
	Agent            string                `json:"agent,omitempty"`
	TaskID           string                `json:"task_id,omitempty"`
	ParentID         string                `json:"parent_id,omitempty"`
	Action           string                `json:"action,omitempty"`
	Detail           string                `json:"detail,omitempty"`
	Kind             string                `json:"kind,omitempty"`
	Start            bool                  `json:"start,omitempty"`
	CallID           string                `json:"call_id,omitempty"`
	Name             string                `json:"name,omitempty"`
	Role             string                `json:"role,omitempty"`
	Content          string                `json:"content,omitempty"`
	IsError          bool                  `json:"is_error,omitempty"`
	Question         string                `json:"question,omitempty"`
	Context          string                `json:"context,omitempty"`
	Choices          []AgentQuestionChoice `json:"choices,omitempty"`
	Outcome          string                `json:"outcome,omitempty"`
}

type SessionEventSink func(SessionEvent) error

// SessionHost owns process-wide dependencies shared by rendererless default
// loop turns. Each Run opens only the requested repository and session.
type SessionHost struct {
	ctx           context.Context
	runtime       *hostruntime.Runtime
	store         config.Store
	manager       *providerhost.Manager
	factory       clientFactory
	providerMu    sync.Mutex
	providerReady bool
}

func NewSessionHost(parent context.Context, store config.Store) (*SessionHost, error) {
	if parent == nil {
		parent = context.Background()
	}
	runtime, err := hostruntime.Open(parent, hostruntime.Options{Directory: store.Dir})
	if err != nil {
		return nil, err
	}
	manager := runtime.Manager()
	return &SessionHost{
		ctx: runtime.Context(), runtime: runtime, store: store, manager: manager,
		factory: managedClientFactory(manager, runtime.Recorder()),
	}, nil
}

func (host *SessionHost) Close() error {
	if host == nil || host.runtime == nil {
		return nil
	}
	return host.runtime.Close()
}

func (host *SessionHost) ensureProvider(loaded config.Config) (config.Config, error) {
	host.providerMu.Lock()
	defer host.providerMu.Unlock()
	if host.providerReady {
		return loaded, nil
	}
	initialized, err := initializeManagedProvider(host.ctx, host.store, host.manager, loaded, nil)
	if err != nil {
		return loaded, err
	}
	if host.manager.Endpoint() != "" {
		host.providerReady = true
	}
	return initialized, nil
}

// Run executes one prompt through Q's default loop in the requested
// repository. An empty sessionID creates a new persisted session.
func (host *SessionHost) Run(
	requestContext context.Context,
	workspaceStore workspace.Store,
	sessionID, prompt string,
	emit SessionEventSink,
) (returnErr error) {
	if host == nil {
		return fmt.Errorf("%w: host is unavailable", ErrSessionRuntimeUnavailable)
	}
	if err := workspace.RejectHomeDirectory(workspaceStore.Root); err != nil {
		return err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return errors.New("prompt is required")
	}
	if requestContext == nil {
		requestContext = context.Background()
	}
	runContext, cancelRun := context.WithCancel(requestContext)
	stopHostCancellation := context.AfterFunc(host.ctx, cancelRun)
	defer stopHostCancellation()
	defer cancelRun()

	loaded, err := host.store.Load()
	if errors.Is(err, config.ErrNotFound) {
		return fmt.Errorf("%w: q is not configured; configure a model in Studio first", ErrSessionRuntimeUnavailable)
	}
	if err != nil {
		return err
	}
	if err := workspaceStore.MigrateLegacySession(); err != nil {
		return err
	}

	var sessionLock *workspace.Lock
	created := false
	if strings.TrimSpace(sessionID) != "" {
		workspaceStore, err = workspaceStore.ForSession(sessionID)
		if err != nil {
			return err
		}
		sessionLock, err = workspace.AcquireSessionLock(workspaceStore.Root, workspaceStore.SessionID, "q studio")
		if err != nil {
			return err
		}
		if _, err = workspaceStore.Load(); err != nil {
			_ = sessionLock.Close()
			return err
		}
	}

	lifecycle := newStartupLifecycle()
	var configuredClient chatClient
	defer func() {
		lifecycle.waitIfStarted()
		resourcesErr := lifecycle.closeResources()
		var clientErr error
		if configuredClient != nil {
			clientErr = configuredClient.Close()
		} else if startupClient := lifecycle.startupClient(); startupClient != nil {
			clientErr = startupClient.Close()
		}
		var lockErr error
		if sessionLock != nil {
			lockErr = sessionLock.Close()
		}
		returnErr = errors.Join(returnErr, resourcesErr, clientErr, lockErr)
	}()

	loaded, err = host.ensureProvider(loaded)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSessionRuntimeUnavailable, err)
	}
	startup := startupRequest{
		ctx: runContext, memoryCtx: runContext, store: host.store, workspaceStore: workspaceStore,
		loaded: loaded, manager: host.manager, factory: host.factory, lifecycle: lifecycle, providerReady: true,
	}.run(nil)
	if startup.err != nil {
		return fmt.Errorf("%w: %v", ErrSessionRuntimeUnavailable, startup.err)
	}
	if startup.client == nil || startup.tools == nil {
		return fmt.Errorf("%w: %v", ErrSessionRuntimeUnavailable,
			errors.Join(errors.New("model or workspace tools are unavailable"), startup.startupErr))
	}
	configuredClient = startup.client

	if sessionLock == nil {
		workspaceStore, sessionLock, err = workspace.CreateSession(workspaceStore.Root, "q studio")
		if err != nil {
			return err
		}
		created = true
	}

	state := newManagedModel(runContext, host.store, host.factory, host.manager)
	state.workspaceStore = &workspaceStore
	state.workspaceLock = sessionLock
	state.toolRuntime = startup.tools
	state.libraryClient = startup.library
	state.setArchiveWriter(startup.archive)
	state.archiveSearch = startup.archiveSearch
	state.archiveErr = startup.archiveErr
	state.models = append(state.models, startup.models...)
	state.gatewayConfig = startup.gatewayConfig
	state.enterChat(startup.config, configuredClient)
	defer state.stopSessionLearning()

	if emit != nil {
		if err := emit(SessionEvent{
			Type: "session", WorkingDirectory: workspaceStore.Root,
			SessionID: workspaceStore.SessionID, Created: created,
		}); err != nil {
			return err
		}
		for _, warning := range sessionStartupWarnings(startup) {
			if err := emit(SessionEvent{Type: "status", Detail: "Warning: " + warning.Error()}); err != nil {
				return err
			}
		}
	}

	updated, initial := state.startChatTurn(prompt, true)
	state = updated.(model)
	if !state.waiting || initial == nil {
		message := strings.TrimSpace(state.status)
		if message == "" {
			message = "session turn did not start"
		}
		return fmt.Errorf("%w: %s", ErrSessionRuntimeUnavailable, message)
	}

	execution := sessionExecutionModel{state: state, initial: initial, emit: emit, cancel: cancelRun}
	final, runErr := tea.NewProgram(
		execution, tea.WithContext(runContext), tea.WithInput(nil), tea.WithOutput(io.Discard),
		tea.WithoutRenderer(), tea.WithoutSignalHandler(),
	).Run()
	if result, ok := final.(sessionExecutionModel); ok {
		return errors.Join(runErr, result.err)
	}
	return runErr
}

func sessionStartupWarnings(result runtimeInitializedMsg) []error {
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

type sessionExecutionModel struct {
	state   model
	initial tea.Cmd
	emit    SessionEventSink
	cancel  context.CancelFunc
	err     error
}

func (m sessionExecutionModel) Init() tea.Cmd { return m.initial }

func (m sessionExecutionModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	eventMessage, isAgentEvent := message.(agentEventMsg)
	if !isAgentEvent {
		updated, command := m.state.Update(message)
		m.state = updated.(model)
		return m, command
	}
	event := eventMessage.event
	if projected, ok := projectSessionAgentEvent(event); ok && m.emit != nil {
		if err := m.emit(projected); err != nil {
			m.err = err
			m.cancel()
			return m, tea.Quit
		}
	}
	if event.question != nil {
		if event.answer != nil {
			event.answer <- askToUserOutput{Err: ErrInteractionUnavailable}
		}
		return m, waitAgentEvent(eventMessage.events, eventMessage.turnID)
	}
	if event.err != nil || event.response != nil {
		updated, _ := m.state.Update(message)
		m.state = updated.(model)
		if event.err != nil {
			m.err = event.err
			return m, tea.Quit
		}
		if event.response == nil || len(event.response.Choices) == 0 {
			m.err = errors.New("session turn returned no response")
			return m, tea.Quit
		}
		if m.emit != nil {
			sessionID := ""
			if m.state.workspaceStore != nil {
				sessionID = m.state.workspaceStore.SessionID
			}
			if err := m.emit(SessionEvent{
				Type: "result", SessionID: sessionID, Outcome: event.outcome,
				Content: strings.TrimSpace(event.response.Choices[0].Message.TextContent()),
			}); err != nil {
				m.err = err
			}
		}
		return m, tea.Quit
	}
	updated, command := m.state.Update(message)
	m.state = updated.(model)
	return m, command
}

func (m sessionExecutionModel) View() tea.View { return tea.NewView("") }

func projectSessionAgentEvent(event agentEvent) (SessionEvent, bool) {
	switch {
	case event.activity != nil:
		return SessionEvent{Type: "activity", Agent: event.activity.Agent, TaskID: event.activity.TaskID, ParentID: event.activity.ParentID, Action: event.activity.Action, Detail: event.activity.Detail}, true
	case event.trace != nil:
		return SessionEvent{Type: "trace", Agent: event.trace.Agent, TaskID: event.trace.TaskID, ParentID: event.trace.ParentID, Kind: event.trace.Kind, CallID: event.trace.CallID, Name: event.trace.Name, Content: event.trace.Content, IsError: event.trace.IsError}, true
	case event.streamDelta != nil:
		return SessionEvent{Type: "stream", Kind: string(event.streamDelta.Kind), Start: event.streamDelta.Start, Content: event.streamDelta.Content}, true
	case event.status != "":
		return SessionEvent{Type: "status", Detail: event.status}, true
	case event.call != nil:
		return SessionEvent{Type: "tool_call", CallID: event.call.ID, Name: event.call.Function.Name, Content: event.call.Function.Arguments}, true
	case event.question != nil:
		return SessionEvent{Type: "question", Question: event.question.Question, Context: event.question.Context, Choices: event.question.Choices}, true
	case event.message != nil:
		return SessionEvent{Type: "message", Role: string(event.message.Role), Name: event.message.Name, CallID: event.message.ToolCallID, Content: event.message.TextContent(), IsError: event.toolIsError}, true
	default:
		return SessionEvent{}, false
	}
}
