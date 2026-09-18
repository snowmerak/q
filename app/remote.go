package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
	"github.com/snowmerak/q/workspacememory"
)

type RemoteRunErrorKind string

const (
	RemoteSubagentNotFound RemoteRunErrorKind = "subagent_not_found"
	RemoteUnavailable      RemoteRunErrorKind = "subagent_unavailable"
)

type RemoteRunError struct {
	Kind RemoteRunErrorKind
	Err  error
}

func (e *RemoteRunError) Error() string { return e.Err.Error() }
func (e *RemoteRunError) Unwrap() error { return e.Err }

// RemoteEvent is the transport-neutral projection emitted by a remote
// headless host. The ordinary model still owns state, persistence, tools, and
// execution order; HTTP only serializes these events.
type RemoteEvent struct {
	Type             string `json:"type"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
	Created          bool   `json:"created,omitempty"`
	Agent            string `json:"agent,omitempty"`
	TaskID           string `json:"task_id,omitempty"`
	ParentID         string `json:"parent_id,omitempty"`
	Action           string `json:"action,omitempty"`
	Detail           string `json:"detail,omitempty"`
	Kind             string `json:"kind,omitempty"`
	CallID           string `json:"call_id,omitempty"`
	Name             string `json:"name,omitempty"`
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	IsError          bool   `json:"is_error,omitempty"`
	Question         string `json:"question,omitempty"`
	Context          string `json:"context,omitempty"`
	Outcome          string `json:"outcome,omitempty"`
}

type RemoteEventSink func(RemoteEvent) error

type RemoteSubagentInfo struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	Source           string `json:"source"`
	Kind             string `json:"kind"`
	Role             string `json:"role"`
	MutatesWorkspace bool   `json:"mutates_workspace"`
	Available        bool   `json:"available"`
}

type RemoteSubagentIssue struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// RemoteHost owns process-wide dependencies shared by q remote requests.
// Each Run creates only the workspace-bound runtime and session projection
// that the existing interactive path would create.
type RemoteHost struct {
	ctx            context.Context
	cancelRuntime  context.CancelFunc
	cancelProvider context.CancelFunc
	cancelMemory   context.CancelFunc
	store          config.Store
	manager        *providerhost.Manager
	recorder       io.Closer
	factory        clientFactory
	memoryDone     <-chan error
	libraryDone    <-chan error
	providerMu     sync.Mutex
	providerReady  bool
	providerConfig config.Config
	closeOnce      sync.Once
	closeErr       error
}

func (h *RemoteHost) ensureProvider(loaded config.Config) (config.Config, error) {
	h.providerMu.Lock()
	defer h.providerMu.Unlock()
	if h.providerReady {
		return h.providerConfig, nil
	}
	initialized, err := initializeManagedProvider(h.ctx, h.store, h.manager, loaded, nil)
	if err != nil {
		return loaded, err
	}
	if h.manager.Endpoint() != "" {
		h.providerReady = true
		h.providerConfig = initialized
	}
	return initialized, nil
}

func NewRemoteHost(parent context.Context, store config.Store) (*RemoteHost, error) {
	if parent == nil {
		parent = context.Background()
	}
	runtimeContext, cancelRuntime := context.WithCancel(parent)
	providerContext, cancelProvider := context.WithCancel(context.WithoutCancel(parent))
	memoryContext, cancelMemory := context.WithCancel(context.WithoutCancel(parent))

	memoryDone := make(chan error, 1)
	go func() { memoryDone <- workspacememory.Run(memoryContext, store.Dir, io.Discard) }()
	libraryDone := make(chan error, 1)
	go func() { libraryDone <- qlibrary.Run(runtimeContext, store.Dir, io.Discard) }()

	manager, err := providerhost.NewManager(providerContext, providerhost.Store{Dir: store.Dir})
	if err != nil {
		cancelRuntime()
		cancelProvider()
		cancelMemory()
		<-memoryDone
		<-libraryDone
		return nil, err
	}
	usageRecorder := newUsageRecorder(store)
	return &RemoteHost{
		ctx: runtimeContext, cancelRuntime: cancelRuntime, cancelProvider: cancelProvider, cancelMemory: cancelMemory,
		store: store, manager: manager,
		recorder:   usageRecorder,
		factory:    managedClientFactory(manager, usageRecorder),
		memoryDone: memoryDone, libraryDone: libraryDone,
	}, nil
}

func (h *RemoteHost) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		h.cancelRuntime()
		managerErr := h.manager.Close()
		recorderErr := h.recorder.Close()
		h.cancelProvider()
		h.cancelMemory()
		memoryErr := ignoreRemoteCancellation(<-h.memoryDone)
		libraryErr := ignoreRemoteCancellation(<-h.libraryDone)
		h.closeErr = errors.Join(managerErr, recorderErr, memoryErr, libraryErr)
	})
	return h.closeErr
}

func ignoreRemoteCancellation(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (h *RemoteHost) ListSubagents(root string) ([]RemoteSubagentInfo, []RemoteSubagentIssue, error) {
	value, err := h.store.Load()
	if err != nil {
		return nil, nil, err
	}
	state := newModel(h.ctx, h.store, nil)
	defer state.stopSessionLearning()
	state.config = value
	state.workspaceStore = &workspace.Store{Root: root}
	registry, err := buildSubagentRegistry(state.customStore())
	if err != nil {
		return nil, nil, err
	}
	infos := registry.List()
	result := make([]RemoteSubagentInfo, 0, len(infos))
	for _, info := range infos {
		definition, _ := registry.Get(info.Name)
		available := value.HasNativeRole(info.Role)
		if info.Kind == subagent.AgentKindExternal {
			_, _, available = externalDefinitionConnection(value, definition)
		}
		result = append(result, RemoteSubagentInfo{
			Name: info.Name, Description: info.Description, Source: info.Source, Kind: info.Kind,
			Role: info.Role, MutatesWorkspace: info.MutatesWorkspace, Available: available,
		})
	}
	var issues []RemoteSubagentIssue
	for _, entry := range state.customStore().List() {
		if entry.Err != nil {
			issues = append(issues, RemoteSubagentIssue{Path: entry.Path, Error: entry.Err.Error()})
		}
	}
	return result, issues, nil
}

// Run executes either the ordinary main agent loop or the existing direct
// /subagent flow. An empty subagent selects the main loop.
func (h *RemoteHost) Run(
	requestContext context.Context,
	workspaceStore workspace.Store,
	sessionID, subagentName, prompt string,
	emit RemoteEventSink,
) (returnErr error) {
	if h == nil {
		return errors.New("remote host is unavailable")
	}
	if err := workspace.RejectHomeDirectory(workspaceStore.Root); err != nil {
		return err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return errors.New("prompt is required")
	}
	runContext, cancelRun := context.WithCancel(requestContext)
	stopHostCancellation := context.AfterFunc(h.ctx, cancelRun)
	defer stopHostCancellation()
	defer cancelRun()

	loaded, err := h.store.Load()
	if errors.Is(err, config.ErrNotFound) {
		return &RemoteRunError{Kind: RemoteUnavailable, Err: errors.New("q is not configured; run `q` first")}
	}
	if err != nil {
		return err
	}
	if err := workspaceStore.MigrateLegacySession(); err != nil {
		return err
	}

	resolvedSubagent := ""
	if strings.TrimSpace(subagentName) != "" {
		probe := newModel(runContext, h.store, nil)
		defer probe.stopSessionLearning()
		probe.config = loaded
		probe.workspaceStore = &workspaceStore
		definition, resolveErr := probe.resolvePublicAgent(subagentName)
		if resolveErr != nil {
			return &RemoteRunError{Kind: RemoteSubagentNotFound, Err: resolveErr}
		}
		resolvedSubagent = definition.Info.Name
	}

	var sessionLock *workspace.Lock
	created := false
	if strings.TrimSpace(sessionID) != "" {
		workspaceStore, err = workspaceStore.ForSession(sessionID)
		if err != nil {
			return err
		}
		sessionLock, err = workspace.AcquireSessionLock(workspaceStore.Root, workspaceStore.SessionID, "q remote")
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
	loaded, err = h.ensureProvider(loaded)
	if err != nil {
		return &RemoteRunError{Kind: RemoteUnavailable, Err: err}
	}

	startup := startupRequest{
		ctx: runContext, memoryCtx: runContext, store: h.store, workspaceStore: workspaceStore,
		loaded: loaded, manager: h.manager, factory: h.factory, lifecycle: lifecycle, providerReady: true,
	}.run(nil)
	if startup.err != nil {
		return &RemoteRunError{Kind: RemoteUnavailable, Err: startup.err}
	}
	if startup.client == nil || startup.tools == nil {
		return &RemoteRunError{Kind: RemoteUnavailable, Err: errors.Join(errors.New("model or workspace tools are unavailable"), startup.startupErr)}
	}
	configuredClient = startup.client

	if sessionLock == nil {
		workspaceStore, sessionLock, err = workspace.CreateSession(workspaceStore.Root, "q remote")
		if err != nil {
			return err
		}
		created = true
	}

	state := newManagedModel(runContext, h.store, h.factory, h.manager)
	state.workspaceStore = &workspaceStore
	state.workspaceLock = sessionLock
	state.toolRuntime = startup.tools
	state.libraryClient = startup.library
	state.archive = startup.archive
	state.archiveSearch = startup.archiveSearch
	state.archiveErr = startup.archiveErr
	state.models = append(state.models, startup.models...)
	state.gatewayConfig = startup.gatewayConfig
	state.enterChat(startup.config, configuredClient)
	defer state.stopSessionLearning()

	if emit != nil {
		if err := emit(RemoteEvent{
			Type: "session", WorkingDirectory: workspaceStore.Root,
			SessionID: workspaceStore.SessionID, Created: created,
		}); err != nil {
			return err
		}
		for _, warning := range sprintStartupWarnings(startup) {
			if err := emit(RemoteEvent{Type: "status", Detail: "Warning: " + warning.Error()}); err != nil {
				return err
			}
		}
	}

	var initial tea.Cmd
	if resolvedSubagent == "" {
		updated, command := state.startChatTurn(prompt, true)
		state = updated.(model)
		initial = command
	} else {
		updated, command := state.startCustom("/subagent " + resolvedSubagent + " " + prompt)
		state = updated.(model)
		initial = command
	}
	if !state.waiting || initial == nil {
		message := strings.TrimSpace(state.status)
		if message == "" {
			message = "remote run did not start"
		}
		return &RemoteRunError{Kind: RemoteUnavailable, Err: errors.New(message)}
	}

	execution := remoteExecutionModel{state: state, initial: initial, emit: emit, cancel: cancelRun}
	final, runErr := tea.NewProgram(
		execution, tea.WithContext(runContext), tea.WithInput(nil), tea.WithOutput(io.Discard),
		tea.WithoutRenderer(), tea.WithoutSignalHandler(),
	).Run()
	if result, ok := final.(remoteExecutionModel); ok {
		state = result.state
		return errors.Join(runErr, result.err)
	}
	return runErr
}

type remoteExecutionModel struct {
	state   model
	initial tea.Cmd
	emit    RemoteEventSink
	cancel  context.CancelFunc
	err     error
}

func (m remoteExecutionModel) Init() tea.Cmd { return m.initial }

func (m remoteExecutionModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	eventMessage, isAgentEvent := message.(agentEventMsg)
	if !isAgentEvent {
		updated, command := m.state.Update(message)
		m.state = updated.(model)
		return m, command
	}
	event := eventMessage.event
	if projected, ok := projectRemoteAgentEvent(event); ok && m.emit != nil {
		if err := m.emit(projected); err != nil {
			m.err = err
			m.cancel()
			return m, tea.Quit
		}
	}
	if event.question != nil {
		if event.answer != nil {
			event.answer <- askToUserOutput{Err: errRemoteInteractionUnavailable}
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
			m.err = errors.New("remote run returned no response")
			return m, tea.Quit
		}
		content := strings.TrimSpace(event.response.Choices[0].Message.TextContent())
		if m.emit != nil {
			sessionID := ""
			if m.state.workspaceStore != nil {
				sessionID = m.state.workspaceStore.SessionID
			}
			if err := m.emit(RemoteEvent{Type: "result", SessionID: sessionID, Outcome: event.outcome, Content: content}); err != nil {
				m.err = err
			}
		}
		return m, tea.Quit
	}
	updated, command := m.state.Update(message)
	m.state = updated.(model)
	return m, command
}

func (m remoteExecutionModel) View() tea.View { return tea.NewView("") }

func projectRemoteAgentEvent(event agentEvent) (RemoteEvent, bool) {
	switch {
	case event.activity != nil:
		return RemoteEvent{Type: "activity", Agent: event.activity.Agent, TaskID: event.activity.TaskID, ParentID: event.activity.ParentID, Action: event.activity.Action, Detail: event.activity.Detail}, true
	case event.trace != nil:
		return RemoteEvent{Type: "trace", Agent: event.trace.Agent, TaskID: event.trace.TaskID, ParentID: event.trace.ParentID, Kind: event.trace.Kind, CallID: event.trace.CallID, Name: event.trace.Name, Content: event.trace.Content, IsError: event.trace.IsError}, true
	case event.status != "":
		return RemoteEvent{Type: "status", Detail: event.status}, true
	case event.call != nil:
		return RemoteEvent{Type: "tool_call", CallID: event.call.ID, Name: event.call.Function.Name, Content: event.call.Function.Arguments}, true
	case event.question != nil:
		return RemoteEvent{Type: "question", Question: event.question.Question, Context: event.question.Context}, true
	case event.message != nil:
		return RemoteEvent{Type: "message", Role: string(event.message.Role), Name: event.message.Name, CallID: event.message.ToolCallID, Content: event.message.TextContent(), IsError: event.toolIsError}, true
	default:
		return RemoteEvent{}, false
	}
}
