package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/archiveembed"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/hostruntime"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workspace"
	"github.com/snowmerak/q/workspacememory"
)

// ErrSessionRuntimeUnavailable indicates that a headless session could not
// start because Q's configured model runtime is unavailable.
var ErrSessionRuntimeUnavailable = errors.New("session runtime unavailable")

// SessionEvent is the transport-neutral projection of one default-loop event.
// Studio uses it for streaming, while the ordinary model remains responsible
// for tools, persistence, recovery, and execution order.
type SessionEvent struct {
	Type             string                `json:"type"`
	RunID            string                `json:"run_id,omitempty"`
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
	ContextUsed      int                   `json:"context_used,omitempty"`
	ContextSize      int                   `json:"context_size,omitempty"`
	Usage            *workspace.TokenUsage `json:"usage,omitempty"`
}

type SessionEventSink func(SessionEvent) error

// SessionWorkspaceContext identifies the Studio project that owns a session's
// primary workspace and the other project roots available to that session.
type SessionWorkspaceContext struct {
	ProjectID      string
	ProjectName    string
	AuxiliaryRoots []string
}

// SessionWorkspaceResolver resolves Studio-owned project context for a
// primary workspace. The bool is false for independent sessions.
type SessionWorkspaceResolver func(primaryRoot string) (SessionWorkspaceContext, bool, error)

type sessionRunControlRequest struct {
	action string
	answer string
	ack    chan error
}

// SessionRunControl lets a rendererless host interact with one running turn.
// Each command is acknowledged only after the Bubble Tea state has accepted
// it, so transports can distinguish stale commands from successful delivery.
type SessionRunControl struct {
	commands chan sessionRunControlRequest
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	err      error
}

// NewSessionRunControl creates a control owned by exactly one RunControlled
// call. It must not be reused for another turn.
func NewSessionRunControl() *SessionRunControl {
	return &SessionRunControl{commands: make(chan sessionRunControlRequest), done: make(chan struct{})}
}

func (control *SessionRunControl) Answer(ctx context.Context, answer string) error {
	return control.send(ctx, "answer", answer)
}

func (control *SessionRunControl) Pause(ctx context.Context) error {
	return control.send(ctx, "pause", "")
}

func (control *SessionRunControl) Resume(ctx context.Context) error {
	return control.send(ctx, "resume", "")
}

func (control *SessionRunControl) Cancel(ctx context.Context) error {
	return control.send(ctx, "cancel", "")
}

func (control *SessionRunControl) send(ctx context.Context, action, answer string) error {
	if control == nil {
		return errors.New("session run control is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request := sessionRunControlRequest{action: action, answer: answer, ack: make(chan error, 1)}
	select {
	case control.commands <- request:
	case <-control.done:
		return control.result()
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.ack:
		return err
	case <-control.done:
		select {
		case err := <-request.ack:
			return err
		default:
		}
		return control.result()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (control *SessionRunControl) finish(err error) {
	if control == nil {
		return
	}
	control.once.Do(func() {
		control.mu.Lock()
		control.err = err
		control.mu.Unlock()
		close(control.done)
	})
}

func (control *SessionRunControl) result() error {
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.err != nil {
		return control.err
	}
	return errors.New("session turn is no longer running")
}

// SessionHost owns process-wide dependencies and live repository sessions.
// Runs reuse each session's model, provider affinity and tool connections.
type SessionHost struct {
	liveMu            sync.Mutex
	liveSessions      map[string]*liveSession
	liveClosed        bool
	ctx               context.Context
	runtime           *hostruntime.Runtime
	store             config.Store
	manager           *providerhost.Manager
	factory           clientFactory
	providerMu        sync.Mutex
	providerReady     bool
	workspaceMu       sync.RWMutex
	workspace         SessionWorkspaceResolver
	logs              *runtimeLogBuffer
	delegationMu      sync.Mutex
	delegationRuns    map[string]*delegationRunControl
	delegationWG      sync.WaitGroup
	delegationCtx     context.Context
	delegationCancel  context.CancelFunc
	delegationClosing bool
}

func NewSessionHost(parent context.Context, store config.Store) (*SessionHost, error) {
	if parent == nil {
		parent = context.Background()
	}
	logs := newRuntimeLogBuffer(256)
	runtime, err := hostruntime.Open(parent, hostruntime.Options{Directory: store.Dir, ServiceOutput: logs})
	if err != nil {
		return nil, err
	}
	manager := runtime.Manager()
	return &SessionHost{
		ctx: runtime.Context(), runtime: runtime, store: store, manager: manager,
		factory: managedClientFactory(manager, runtime.Recorder()), logs: logs,
	}, nil
}

// SetSessionWorkspaceResolver installs the Studio project resolver used for
// subsequent turns. It is safe to replace while the host is running.
func (host *SessionHost) SetSessionWorkspaceResolver(resolver SessionWorkspaceResolver) {
	if host == nil {
		return
	}
	host.workspaceMu.Lock()
	host.workspace = resolver
	host.workspaceMu.Unlock()
}

func (host *SessionHost) resolveSessionWorkspace(primary string) (SessionWorkspaceContext, bool, error) {
	host.workspaceMu.RLock()
	resolver := host.workspace
	host.workspaceMu.RUnlock()
	if resolver == nil {
		return SessionWorkspaceContext{}, false, nil
	}
	value, found, err := resolver(primary)
	value.AuxiliaryRoots = append([]string(nil), value.AuxiliaryRoots...)
	return value, found, err
}

func (host *SessionHost) Close() error {
	if host == nil {
		return nil
	}
	host.delegationMu.Lock()
	host.delegationClosing = true
	if host.delegationCancel != nil {
		host.delegationCancel()
	}
	for _, run := range host.delegationRuns {
		run.cancel(context.Canceled)
	}
	host.delegationMu.Unlock()
	sessionsErr := host.closeLiveSessions()
	host.delegationWG.Wait()
	if host.runtime == nil {
		return sessionsErr
	}
	return errors.Join(sessionsErr, host.runtime.Close())
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

// ApplyGateway replaces the managed Gateway child and persists its provider
// configuration. Existing sessions use the replacement endpoint on their next
// turn without restarting Studio.
func (host *SessionHost) ApplyGateway(ctx context.Context, value gateway.Config) error {
	if host == nil || host.manager == nil {
		return fmt.Errorf("%w: Gateway runtime is unavailable", ErrSessionRuntimeUnavailable)
	}
	host.providerMu.Lock()
	defer host.providerMu.Unlock()
	if err := host.manager.Apply(ctx, value); err != nil {
		return err
	}
	host.providerReady = true
	return nil
}

// SyncEmbeddings applies the saved embedding model to the global Library and,
// when root is provided, rebuilds that workspace's semantic archive before
// returning. A workspace with an active turn may reject the vector transition
// until that turn releases its lease.
func (host *SessionHost) SyncEmbeddings(ctx context.Context, root string, value config.Config) (returnErr error) {
	if host == nil || host.runtime == nil {
		return fmt.Errorf("%w: embedding runtime is unavailable", ErrSessionRuntimeUnavailable)
	}
	if err := host.releaseIdleSessions(); err != nil {
		return err
	}
	value, err := host.ensureProvider(value)
	if err != nil {
		return err
	}
	configuredClient, err := host.factory(value)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, configuredClient.Close()) }()

	libraryRuntime, err := qlibrary.Ensure(ctx, host.store.Dir)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, libraryRuntime.Close()) }()
	var embedder qlibrary.Embedder
	if value.Embedding.Model != "" {
		var ok bool
		embedder, ok = configuredClient.(qlibrary.Embedder)
		if !ok {
			return errors.New("configured LLM client does not support embeddings")
		}
	}
	if err := libraryRuntime.Client().ConfigureEmbedding(embedder, value.Embedding.Model, value.Embedding.Dimensions); err != nil {
		return err
	}
	if _, err := libraryRuntime.Client().SyncSkillEmbeddings(ctx); err != nil {
		return err
	}

	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	if err := workspace.RejectHomeDirectory(root); err != nil {
		return err
	}
	memoryRuntime, err := workspacememory.Ensure(ctx, host.store.Dir)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, memoryRuntime.Close()) }()
	vector := sessionstore.VectorConfig{}
	if value.Embedding.Model != "" {
		vector = sessionstore.VectorConfig{Model: value.Embedding.Model, Dimensions: value.Embedding.Dimensions}
	}
	archiveStore, err := memoryRuntime.Client().OpenWorkspace(ctx, root, vector)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, archiveStore.Close()) }()
	archive := archiveembed.New(archiveStore)
	if value.Embedding.Model == "" {
		return archive.Disable()
	}
	if err := archive.Configure(embedder, value.Embedding.Model, value.Embedding.Dimensions); err != nil {
		return err
	}
	_, err = archive.Backfill(ctx)
	return err
}

// Run executes one prompt through Q's default loop in the requested
// repository. An empty sessionID creates a new persisted session.
func (host *SessionHost) Run(
	requestContext context.Context,
	workspaceStore workspace.Store,
	sessionID, prompt string,
	emit SessionEventSink,
) (returnErr error) {
	return host.run(requestContext, workspaceStore, sessionID, prompt, emit, nil)
}

// RunControlled executes one prompt and accepts interactive commands through
// control. The run lifetime follows requestContext, which embedding services
// should derive from their own process lifetime rather than a browser request.
func (host *SessionHost) RunControlled(
	requestContext context.Context,
	workspaceStore workspace.Store,
	sessionID, prompt string,
	emit SessionEventSink,
	control *SessionRunControl,
) (returnErr error) {
	if control == nil {
		return errors.New("session run control is required")
	}
	defer func() { control.finish(returnErr) }()
	return host.run(requestContext, workspaceStore, sessionID, prompt, emit, control)
}

func (host *SessionHost) run(
	requestContext context.Context,
	workspaceStore workspace.Store,
	sessionID, prompt string,
	emit SessionEventSink,
	control *SessionRunControl,
) error {
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
	session, err := host.acquireLiveSession(workspaceStore, sessionID)
	if err != nil {
		return err
	}
	defer session.busy.Unlock()
	_, err = session.execute(requestContext, prompt, false, emit, control)
	return err
}

// Compact summarizes older context for one inactive session without adding a
// user message or changing its transcript.
func (host *SessionHost) Compact(requestContext context.Context, workspaceStore workspace.Store, sessionID string) (string, error) {
	if host == nil {
		return "", fmt.Errorf("%w: host is unavailable", ErrSessionRuntimeUnavailable)
	}
	if err := workspace.RejectHomeDirectory(workspaceStore.Root); err != nil {
		return "", err
	}
	if strings.TrimSpace(sessionID) == "" {
		return "", errors.New("session ID is required")
	}
	session, err := host.acquireLiveSession(workspaceStore, sessionID)
	if err != nil {
		return "", err
	}
	defer session.busy.Unlock()
	return session.execute(requestContext, "", true, nil, nil)
}

type preparedSession struct {
	state     model
	store     workspace.Store
	lock      *workspace.Lock
	lifecycle *startupLifecycle
	client    chatClient
	warnings  []error
	created   bool
}

func (prepared *preparedSession) Close() error {
	if prepared == nil {
		return nil
	}
	prepared.state.stopSessionLearning()
	prepared.lifecycle.waitIfStarted()
	resourcesErr := prepared.lifecycle.closeResources()
	var clientErr error
	if prepared.client != nil {
		clientErr = prepared.client.Close()
	} else if startupClient := prepared.lifecycle.startupClient(); startupClient != nil {
		clientErr = startupClient.Close()
	}
	var lockErr error
	if prepared.lock != nil {
		lockErr = prepared.lock.Close()
	}
	return errors.Join(resourcesErr, clientErr, lockErr)
}

func (host *SessionHost) prepareSession(runContext context.Context, workspaceStore workspace.Store, sessionID string) (_ *preparedSession, returnErr error) {
	loaded, err := host.store.Load()
	if errors.Is(err, config.ErrNotFound) {
		return nil, fmt.Errorf("%w: q is not configured; configure a model in Studio first", ErrSessionRuntimeUnavailable)
	}
	if err != nil {
		return nil, err
	}
	if err := workspaceStore.MigrateLegacySession(); err != nil {
		return nil, err
	}

	prepared := &preparedSession{store: workspaceStore, lifecycle: newStartupLifecycle()}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, prepared.Close())
		}
	}()
	if strings.TrimSpace(sessionID) != "" {
		prepared.store, err = workspaceStore.ForSession(sessionID)
		if err != nil {
			return nil, err
		}
		prepared.lock, err = workspace.AcquireSessionLock(prepared.store.Root, prepared.store.SessionID, "q studio")
		if err != nil {
			return nil, err
		}
		if _, err = prepared.store.Load(); err != nil {
			return nil, err
		}
	}

	loaded, err = host.ensureProvider(loaded)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSessionRuntimeUnavailable, err)
	}
	projectContext, projectFound, err := host.resolveSessionWorkspace(prepared.store.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve Studio project workspaces: %w", err)
	}
	memoryContext := host.ctx
	if host.runtime != nil {
		memoryContext = host.runtime.MemoryContext()
	}
	startup := startupRequest{
		ctx: runContext, memoryCtx: memoryContext, store: host.store, workspaceStore: prepared.store,
		loaded: loaded, manager: host.manager, factory: host.factory, lifecycle: prepared.lifecycle, providerReady: true,
		auxiliaryRoots: append([]string(nil), projectContext.AuxiliaryRoots...),
	}.run(nil)
	if startup.err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSessionRuntimeUnavailable, startup.err)
	}
	if startup.client == nil || startup.tools == nil {
		return nil, fmt.Errorf("%w: %v", ErrSessionRuntimeUnavailable,
			errors.Join(errors.New("model or workspace tools are unavailable"), startup.startupErr))
	}
	prepared.client = startup.client
	prepared.warnings = sessionStartupWarnings(startup)

	if prepared.lock == nil {
		prepared.store, prepared.lock, err = workspace.CreateSession(prepared.store.Root, "q studio")
		if err != nil {
			return nil, err
		}
		prepared.created = true
	}

	prepared.state = newManagedModel(runContext, host.store, host.factory, host.manager)
	prepared.state.workspaceStore = &prepared.store
	prepared.state.workspaceLock = prepared.lock
	prepared.state.toolRuntime = startup.tools
	prepared.state.libraryClient = startup.library
	prepared.state.setArchiveWriter(startup.archive)
	prepared.state.archiveSearch = startup.archiveSearch
	prepared.state.archiveErr = startup.archiveErr
	prepared.state.models = append(prepared.state.models, startup.models...)
	prepared.state.gatewayConfig = startup.gatewayConfig
	if projectFound {
		prepared.state.studioWorkspaceContext = &projectContext
	}
	prepared.state.enterChat(startup.config, prepared.client)
	return prepared, nil
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
	keepAlive             bool
	finished              func(model, error)
	state                 model
	initial               tea.Cmd
	emit                  SessionEventSink
	cancel                context.CancelFunc
	control               *SessionRunControl
	paused                bool
	cancelling            bool
	deferred              *agentEventMsg
	pendingQuestionCallID string
	contextUsed           int
	contextSize           int
	err                   error
}

type sessionRunControlMsg struct {
	request sessionRunControlRequest
	control *SessionRunControl
}

func (m sessionExecutionModel) Init() tea.Cmd {
	return tea.Batch(m.initial, waitSessionRunControl(m.control, m.state.ctx))
}

func (m sessionExecutionModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if result, ok := message.(chatResultMsg); ok {
		if result.err == nil && (result.response == nil || len(result.response.Choices) == 0) {
			result.err = errors.New("session turn returned no response")
		}
		return m.updateAgentEvent(agentEventMsg{turnID: result.turnID, event: agentEvent{
			response: result.response, err: result.err, requestEstimate: result.requestEstimate, toolCalls: result.toolCalls,
		}})
	}
	if command, ok := message.(sessionRunControlMsg); ok {
		if command.control != m.control {
			command.request.ack <- errors.New("session turn has finished")
			return m, nil
		}
		return m.updateControl(command.request)
	}
	eventMessage, isAgentEvent := message.(agentEventMsg)
	if !isAgentEvent {
		updated, command := m.state.Update(message)
		m.state = updated.(model)
		if !m.emitContextUsage() {
			return m.failExecution()
		}
		return m, command
	}
	if eventMessage.turnID != 0 && eventMessage.turnID != m.state.turnID {
		return m, nil
	}
	if m.paused {
		m.deferred = &eventMessage
		return m, nil
	}
	return m.updateAgentEvent(eventMessage)
}

func (m sessionExecutionModel) updateAgentEvent(eventMessage agentEventMsg) (tea.Model, tea.Cmd) {
	if eventMessage.turnID != 0 && eventMessage.turnID != m.state.turnID {
		return m, nil
	}
	if m.cancelling {
		return m.finishExecution(nil)
	}
	event := eventMessage.event
	projected, projectedOK := projectSessionAgentEvent(event)
	if event.call != nil && event.call.Function.Name == askToUserToolName {
		m.pendingQuestionCallID = event.call.ID
	}
	if event.question != nil {
		projected.CallID = m.pendingQuestionCallID
	}
	if projectedOK && m.emit != nil {
		if err := m.emit(projected); err != nil {
			m.err = err
			m.cancel()
			return m.failExecution()
		}
	}
	if event.question != nil {
		if m.control == nil && event.answer != nil {
			event.answer <- askToUserOutput{Err: ErrInteractionUnavailable}
		}
		if m.control != nil {
			updated, command := m.state.Update(eventMessage)
			m.state = updated.(model)
			if !m.emitContextUsage() {
				return m.failExecution()
			}
			return m, command
		}
		return m, waitAgentEvent(eventMessage.events, eventMessage.turnID)
	}
	if event.err != nil || event.response != nil {
		updated, command := m.state.Update(eventMessage)
		m.state = updated.(model)
		if !m.emitContextUsage() {
			return m.failExecution()
		}
		if event.err != nil {
			m.err = event.err
			return m.finishExecution(command)
		}
		if event.response == nil || len(event.response.Choices) == 0 {
			m.err = errors.New("session turn returned no response")
			return m.finishExecution(command)
		}
		if m.emit != nil {
			sessionID := ""
			if m.state.workspaceStore != nil {
				sessionID = m.state.workspaceStore.SessionID
			}
			if err := m.emit(SessionEvent{
				Type: "result", SessionID: sessionID, Outcome: event.outcome,
				Content: strings.TrimSpace(event.response.Choices[0].Message.TextContent()),
				Usage:   responseTokenUsage(event.response.Usage),
			}); err != nil {
				m.err = err
			}
		}
		return m.finishExecution(command)
	}
	updated, command := m.state.Update(eventMessage)
	m.state = updated.(model)
	if !m.emitContextUsage() {
		return m.failExecution()
	}
	return m, command
}

func (m *sessionExecutionModel) detach() {
	m.emit, m.control, m.cancel, m.finished = nil, nil, nil, nil
	m.paused, m.cancelling = false, false
	m.deferred = nil
	m.pendingQuestionCallID = ""
}

func (m sessionExecutionModel) finishExecution(command tea.Cmd) (tea.Model, tea.Cmd) {
	if !m.keepAlive {
		return m, tea.Quit
	}
	if m.finished != nil {
		m.finished(m.state, m.err)
	}
	m.detach()
	return m, command
}

func (m sessionExecutionModel) failExecution() (tea.Model, tea.Cmd) {
	if m.keepAlive && m.state.waiting {
		updated, _ := m.state.interruptTurn()
		m.state = updated.(model)
	}
	return m.finishExecution(nil)
}

func sessionContextUsageEvent(state model) (SessionEvent, bool) {
	if state.memory == nil {
		return SessionEvent{}, false
	}
	stats := state.memory.Stats()
	if stats.ContextWindow <= 0 {
		return SessionEvent{}, false
	}
	return SessionEvent{
		Type: "context_usage", ContextUsed: max(0, stats.PredictedTokens), ContextSize: stats.ContextWindow,
	}, true
}

func (m *sessionExecutionModel) emitContextUsage() bool {
	usage, ok := sessionContextUsageEvent(m.state)
	if !ok || usage.ContextUsed == m.contextUsed && usage.ContextSize == m.contextSize {
		return true
	}
	m.contextUsed, m.contextSize = usage.ContextUsed, usage.ContextSize
	if m.emit == nil {
		return true
	}
	if err := m.emit(usage); err != nil {
		m.err = err
		m.cancel()
		return false
	}
	return true
}

func (m sessionExecutionModel) updateControl(request sessionRunControlRequest) (tea.Model, tea.Cmd) {
	acknowledge := func(err error) { request.ack <- err }
	nextControl := waitSessionRunControl(m.control, m.state.ctx)
	switch request.action {
	case "answer":
		answer := strings.TrimSpace(request.answer)
		if !m.state.asking || m.state.questionAnswer == nil {
			acknowledge(errors.New("session turn is not waiting for an answer"))
			return m, nextControl
		}
		if answer == "" {
			acknowledge(errors.New("answer is required"))
			return m, nextControl
		}
		parsed := answerForQuestion(m.state.pendingQuestion, answer)
		if m.state.pendingQuestion.ChoiceOnly && parsed.SelectedChoiceID == "" {
			acknowledge(errors.New("answer must select one of the available choices"))
			return m, nextControl
		}
		updated, command := m.state.submitQuestionAnswer(answer)
		m.state = updated.(model)
		if m.emit != nil {
			if err := m.emit(SessionEvent{Type: "question_answered", CallID: m.pendingQuestionCallID}); err != nil {
				m.err = err
				acknowledge(err)
				m.cancel()
				return m.failExecution()
			}
		}
		m.pendingQuestionCallID = ""
		acknowledge(nil)
		return m, tea.Batch(command, nextControl)
	case "pause":
		if m.paused {
			acknowledge(nil)
			return m, nextControl
		}
		m.paused = true
		if m.emit != nil {
			if err := m.emit(SessionEvent{Type: "control", Action: "paused", Detail: "Turn paused"}); err != nil {
				m.err = err
				acknowledge(err)
				m.cancel()
				return m.failExecution()
			}
		}
		acknowledge(nil)
		return m, nextControl
	case "resume":
		if !m.paused {
			acknowledge(errors.New("session turn is not paused"))
			return m, nextControl
		}
		m.paused = false
		if m.emit != nil {
			if err := m.emit(SessionEvent{Type: "control", Action: "resumed", Detail: "Turn resumed"}); err != nil {
				m.err = err
				acknowledge(err)
				m.cancel()
				return m.failExecution()
			}
		}
		acknowledge(nil)
		if m.deferred != nil {
			deferred := *m.deferred
			m.deferred = nil
			updated, command := m.updateAgentEvent(deferred)
			return updated, tea.Batch(command, nextControl)
		}
		return m, nextControl
	case "cancel":
		m.cancelling = true
		updated, _ := m.state.interruptTurn()
		m.state = updated.(model)
		if m.emit != nil {
			if err := m.emit(SessionEvent{Type: "cancelled", Detail: "Turn interrupted by user"}); err != nil {
				m.err = err
			}
		}
		acknowledge(m.err)
		return m.finishExecution(nil)
	default:
		err := fmt.Errorf("unknown session control action %q", request.action)
		acknowledge(err)
		return m, nextControl
	}
}

func waitSessionRunControl(control *SessionRunControl, ctx context.Context) tea.Cmd {
	if control == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		select {
		case request := <-control.commands:
			return sessionRunControlMsg{request: request, control: control}
		case <-control.done:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

func (m sessionExecutionModel) View() tea.View { return tea.NewView("") }

type sessionCompactionModel struct {
	state   model
	initial tea.Cmd
	cancel  context.CancelFunc
	err     error
}

func (m sessionCompactionModel) Init() tea.Cmd { return m.initial }

func (m sessionCompactionModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	updated, command := m.state.Update(message)
	m.state = updated.(model)
	result, finished := message.(compactionResultMsg)
	if !finished {
		return m, command
	}
	if result.err != nil {
		m.err = result.err
	} else if result.response == nil || len(result.response.Choices) == 0 {
		m.err = errors.New("context compaction returned no response")
	}
	if m.err != nil {
		m.cancel()
	}
	return m, tea.Quit
}

func (m sessionCompactionModel) View() tea.View { return tea.NewView("") }

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
		projected := SessionEvent{Type: "message", Role: string(event.message.Role), Name: event.message.Name, CallID: event.message.ToolCallID, Content: event.message.TextContent(), IsError: event.toolIsError}
		if event.message.Role == client.RoleAssistant && event.usage != nil {
			projected.Usage = responseTokenUsage(*event.usage)
		}
		return projected, true
	default:
		return SessionEvent{}, false
	}
}
