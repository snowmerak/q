package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
	"github.com/snowmerak/q/workspacememory"
)

// BooleanOverride distinguishes an omitted process option from an explicit
// true or false value.
type BooleanOverride struct {
	Set   bool
	Value bool
}

// PlanAutomationOverrides replace persistent plan settings for the lifetime
// of one ACP process. They are never written back to config.yaml.
type PlanAutomationOverrides struct {
	AutoApprove BooleanOverride
	AutoResolve BooleanOverride
}

type ACPOptions struct {
	Plan PlanAutomationOverrides
}

// RunACPDefault serves ACP over stdin/stdout for a single workspace.
func RunACPDefault(ctx context.Context, root string, input io.Reader, output, logOutput io.Writer, options ...ACPOptions) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	return RunACP(ctx, store, root, input, output, logOutput, options...)
}

// RunACP serves ACP over the supplied streams. A server process is bound to one
// workspace and may keep multiple ACP sessions active concurrently. Each
// session owns an independent conversation projection, learning lifecycle, and
// workspace session lock.
func RunACP(ctx context.Context, store config.Store, root string, input io.Reader, output, logOutput io.Writer, options ...ACPOptions) (runErr error) {
	if input == nil || output == nil {
		return errors.New("ACP input and output are required")
	}

	canonicalRoot, err := canonicalWorkspaceRoot(root)
	if err != nil {
		return err
	}
	if logOutput == nil {
		logOutput = io.Discard
	}
	logger := slog.New(slog.NewTextHandler(logOutput, &slog.HandlerOptions{Level: slog.LevelInfo}))

	host, err := openACPHost(ctx, store, canonicalRoot)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, host.Close()) }()

	agent := newACPAgent(&host.model, canonicalRoot, logger)
	agent.planOverrides = mergeACPOptions(options).Plan
	connection := acp.NewAgentSideConnection(agent, output, input)
	connection.SetLogger(logger)
	agent.setConnection(connection)

	select {
	case <-ctx.Done():
	case <-connection.Done():
	}
	return agent.shutdown()
}

type acpHost struct {
	model          model
	lifecycle      *startupLifecycle
	manager        *providerhost.Manager
	client         chatClient
	cancel         context.CancelFunc
	providerCancel context.CancelFunc
	memoryCancel   context.CancelFunc
	memoryDone     <-chan error
	libraryDone    <-chan error
	closeOnce      sync.Once
	closeErr       error
}

func openACPHost(parent context.Context, store config.Store, root string) (*acpHost, error) {
	runtimeContext, cancel := context.WithCancel(parent)
	host := &acpHost{cancel: cancel}
	fail := func(err error) (*acpHost, error) {
		return nil, errors.Join(err, host.Close())
	}

	workspaceStore := workspace.Store{Root: root}
	if err := workspaceStore.MigrateLegacySession(); err != nil {
		return fail(err)
	}

	memoryContext, cancelMemory := context.WithCancel(context.WithoutCancel(parent))
	host.memoryCancel = cancelMemory
	memoryDone := make(chan error, 1)
	host.memoryDone = memoryDone
	go func() {
		memoryDone <- workspacememory.Run(memoryContext, store.Dir, io.Discard)
	}()

	libraryDone := make(chan error, 1)
	host.libraryDone = libraryDone
	go func() {
		libraryDone <- qlibrary.Run(runtimeContext, store.Dir, io.Discard)
	}()

	loaded, loadErr := store.Load()
	if loadErr != nil && !errors.Is(loadErr, config.ErrNotFound) {
		return fail(loadErr)
	}
	providerContext, cancelProvider := context.WithCancel(context.WithoutCancel(parent))
	host.providerCancel = cancelProvider
	manager, managerErr := providerhost.NewManager(providerContext, providerhost.Store{Dir: store.Dir})
	if managerErr != nil {
		return fail(managerErr)
	}
	host.manager = manager
	lifecycle := newStartupLifecycle()
	host.lifecycle = lifecycle
	initialized := (startupRequest{
		ctx:            runtimeContext,
		memoryCtx:      memoryContext,
		loaded:         loaded,
		configErr:      loadErr,
		store:          store,
		manager:        manager,
		workspaceStore: workspaceStore,
		lifecycle:      lifecycle,
		factory:        managedClientFactory(manager),
	}).run(nil)
	host.client = initialized.client
	if initialized.err != nil {
		return fail(initialized.err)
	}
	if initialized.startupErr != nil {
		return fail(initialized.startupErr)
	}
	if initialized.client == nil {
		return fail(errors.New("no model provider is configured; run `q gateway config` first"))
	}

	host.model = newManagedModel(runtimeContext, store, managedClientFactory(manager), manager)
	host.model.workspaceStore = &workspaceStore
	host.model.toolRuntime = initialized.tools
	host.model.archive = initialized.archive
	host.model.archiveSearch = initialized.archiveSearch
	host.model.archiveErr = initialized.archiveErr
	host.model.libraryClient = initialized.library
	host.model.models = initialized.models
	host.model.gatewayConfig = initialized.gatewayConfig
	host.model.enterChat(initialized.config, initialized.client)
	return host, nil
}

func (h *acpHost) Close() error {
	h.closeOnce.Do(func() {
		var closeErrors []error
		if h.cancel != nil {
			h.cancel()
		}
		if h.lifecycle != nil {
			h.lifecycle.waitIfStarted()
			closeErrors = append(closeErrors, h.lifecycle.closeResources())
		}

		if h.client != nil {
			closeErrors = append(closeErrors, h.client.Close())
		}
		if h.providerCancel != nil {
			h.providerCancel()
		}
		if h.memoryCancel != nil {
			h.memoryCancel()
		}
		if h.memoryDone != nil {
			closeErrors = append(closeErrors, <-h.memoryDone)
		}
		if h.libraryDone != nil {
			closeErrors = append(closeErrors, <-h.libraryDone)
		}
		if h.manager != nil {
			closeErrors = append(closeErrors, h.manager.Close())
		}
		if h.model.workspaceLock != nil {
			closeErrors = append(closeErrors, h.model.workspaceLock.Close())
			h.model.workspaceLock = nil
		}
		h.closeErr = errors.Join(closeErrors...)
	})
	return h.closeErr
}

type acpSessionConnection interface {
	SessionUpdate(context.Context, acp.SessionNotification) error
	UnstableCreateElicitation(context.Context, acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error)
}

type acpAgent struct {
	state         *model
	root          string
	logger        *slog.Logger
	planOverrides PlanAutomationOverrides

	stateMu             sync.Mutex
	activationWG        sync.WaitGroup
	promptMu            sync.Mutex
	mcpMu               sync.Mutex
	learningMu          sync.Mutex
	planConfigMu        sync.Mutex
	connection          acpSessionConnection
	clientCapabilities  acp.ClientCapabilities
	sessions            map[acp.SessionId]*acpAgent
	opening             map[acp.SessionId]chan struct{}
	closedSessions      map[acp.SessionId]struct{}
	owner               *acpAgent
	sessionID           acp.SessionId
	sessionOpen         bool
	closing             bool
	shuttingDown        bool
	turnCancel          context.CancelFunc
	learningInterrupt   context.CancelFunc
	learningVersion     uint64
	learningPending     bool
	learningDisabled    bool
	sessionMCPBase      mcpconfig.Config
	sessionMCPBaseSet   bool
	sessionMCPConfig    mcpconfig.Config
	sessionMCPIDs       map[string]struct{}
	sessionMCPOverride  bool
	sessionMCPSignature [sha256.Size]byte
	sessionMCPScoped    *qtools.ExternalScope
	commitSession       acpCommitSessionFactory
	pendingCommit       *acpPendingCommit
	pendingQuestion     *acpPendingQuestion
	promptLocked        func()
}

var (
	_ acp.Agent       = (*acpAgent)(nil)
	_ acp.AgentLoader = (*acpAgent)(nil)
)

func newACPAgent(state *model, root string, logger *slog.Logger) *acpAgent {
	return &acpAgent{
		state: state, root: root, logger: logger,
		sessions: make(map[acp.SessionId]*acpAgent), opening: make(map[acp.SessionId]chan struct{}),
		closedSessions: make(map[acp.SessionId]struct{}),
	}
}

func mergeACPOptions(options []ACPOptions) ACPOptions {
	var merged ACPOptions
	for _, option := range options {
		if option.Plan.AutoApprove.Set {
			merged.Plan.AutoApprove = option.Plan.AutoApprove
		}
		if option.Plan.AutoResolve.Set {
			merged.Plan.AutoResolve = option.Plan.AutoResolve
		}
	}
	return merged
}

func applyPlanAutomationOverrides(value config.PlanConfig, overrides PlanAutomationOverrides) config.PlanConfig {
	if overrides.AutoApprove.Set {
		value.AutoApprove = overrides.AutoApprove.Value
	}
	if overrides.AutoResolve.Set {
		value.AutoResolve = overrides.AutoResolve.Value
	}
	return value
}

// newSessionRuntime creates the mutable model projection owned by one ACP
// session. Provider, archive, Library, and builtin tool services are shared;
// conversation state, Thinker state, workspace lock, and optional ACP-provided
// MCP connections are not.
func (a *acpAgent) newSessionRuntime() *acpAgent {
	template := a.state
	state := newManagedModel(template.ctx, template.store, template.factory, template.runtime)
	state.toolRuntime = template.toolRuntime
	state.libraryClient = template.libraryClient
	state.archive = template.archive
	state.archiveSearch = template.archiveSearch
	state.archiveErr = template.archiveErr
	state.models = append([]client.Model(nil), template.models...)
	state.gatewayConfig = template.gatewayConfig
	coordinator := a.coordinator()
	coordinator.planConfigMu.Lock()
	state.config = template.config
	coordinator.planConfigMu.Unlock()
	state.client = template.client
	state.workspaceStore = &workspace.Store{Root: a.root}

	runtime := &acpAgent{
		state: &state, root: a.root, logger: a.logger, owner: a,
		planOverrides: a.planOverrides,
	}
	if a.commitSession != nil {
		runtime.commitSession = a.commitSession
	} else {
		runtime.commitSession = newACPCommitSessionFactory(&state, a.root)
	}
	return runtime
}

func (a *acpAgent) coordinator() *acpAgent {
	if a.owner != nil {
		return a.owner
	}
	return a
}

func (a *acpAgent) registerSession(runtime *acpAgent) error {
	if runtime == nil || runtime.sessionID == "" {
		return errors.New("ACP session runtime is unavailable")
	}
	a.learningMu.Lock()
	defer a.learningMu.Unlock()
	learning, err := (workspace.Store{Root: a.root}).LoadLearningConfig()
	if err != nil {
		return err
	}
	wasDisabled := runtime.state.learningDisabled()
	runtime.state.workspaceLearning = learning
	if learning.Disabled {
		runtime.state.stopSessionLearning()
	} else if wasDisabled {
		runtime.state.resetSessionLearning()
	}
	runtime.syncLearningInterrupt()
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.shuttingDown {
		return errors.New("ACP agent is shutting down")
	}
	if _, exists := a.sessions[runtime.sessionID]; exists {
		return fmt.Errorf("ACP session %q is already active", runtime.sessionID)
	}
	runtime.learningVersion = a.learningVersion
	runtime.learningDisabled = learning.Disabled
	a.sessions[runtime.sessionID] = runtime
	delete(a.closedSessions, runtime.sessionID)
	return nil
}

func (a *acpAgent) sessionRuntime(sessionID acp.SessionId) (*acpAgent, error) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.shuttingDown {
		return nil, errors.New("ACP agent is shutting down")
	}
	runtime := a.sessions[sessionID]
	if runtime == nil {
		return nil, fmt.Errorf("unknown ACP session %q", sessionID)
	}
	runtime.stateMu.Lock()
	available := runtime.sessionOpen && !runtime.closing
	runtime.stateMu.Unlock()
	if !available {
		return nil, fmt.Errorf("unknown ACP session %q", sessionID)
	}
	return runtime, nil
}

func (a *acpAgent) connectionState() (acpSessionConnection, acp.ClientCapabilities) {
	coordinator := a.coordinator()
	coordinator.stateMu.Lock()
	defer coordinator.stateMu.Unlock()
	return coordinator.connection, coordinator.clientCapabilities
}

func acpSessionID(sessionID string) acp.SessionId {
	return acp.SessionId("q-" + sessionID)
}

func workspaceSessionID(sessionID acp.SessionId) (string, error) {
	value, found := strings.CutPrefix(strings.TrimSpace(string(sessionID)), "q-")
	if !found || value == "" {
		return "", fmt.Errorf("unknown ACP session %q", sessionID)
	}
	store, err := (workspace.Store{}).ForSession(value)
	if err != nil {
		return "", fmt.Errorf("unknown ACP session %q", sessionID)
	}
	return store.SessionID, nil
}

func (a *acpAgent) setConnection(connection acpSessionConnection) {
	a.stateMu.Lock()
	a.connection = connection
	a.stateMu.Unlock()
}

func (a *acpAgent) Initialize(_ context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	a.stateMu.Lock()
	a.clientCapabilities = request.ClientCapabilities
	a.stateMu.Unlock()
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: true,
			McpCapabilities: acp.McpCapabilities{
				Http: true,
			},
			PromptCapabilities: acp.PromptCapabilities{
				EmbeddedContext: true,
				Image:           a.state.supportsACPImages(),
			},
			SessionCapabilities: acp.SessionCapabilities{
				Close:  &acp.SessionCloseCapabilities{},
				Delete: &acp.SessionDeleteCapabilities{},
				List:   &acp.SessionListCapabilities{},
				Resume: &acp.SessionResumeCapabilities{},
			},
		},
		AgentInfo:   &acp.Implementation{Name: "q", Version: "dev"},
		AuthMethods: []acp.AuthMethod{},
	}, nil
}

func (a *acpAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *acpAgent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

func (a *acpAgent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if err := a.beginActivation(); err != nil {
		return acp.NewSessionResponse{}, err
	}
	defer a.activationWG.Done()
	if err := a.validateSessionWorkspace(request.Cwd, request.AdditionalDirectories); err != nil {
		return acp.NewSessionResponse{}, err
	}
	store, lock, err := workspace.CreateSession(a.root, "q acp")
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	runtime := a.newSessionRuntime()
	if err := runtime.activateStore(ctx, store, lock, request.McpServers); err != nil {
		_ = store.ClearSession()
		_ = lock.Close()
		return acp.NewSessionResponse{}, err
	}
	if err := a.registerSession(runtime); err != nil {
		_ = runtime.closeRuntime()
		_ = store.ClearSession()
		return acp.NewSessionResponse{}, err
	}
	_ = runtime.startLearningIfOpen()
	return acp.NewSessionResponse{SessionId: runtime.sessionID}, nil
}

func (a *acpAgent) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	a.stateMu.Lock()
	runtime := a.sessions[request.SessionId]
	pending := a.opening[request.SessionId]
	_, closed := a.closedSessions[request.SessionId]
	a.stateMu.Unlock()
	if runtime == nil {
		if pending != nil {
			select {
			case <-pending:
				return a.CloseSession(ctx, request)
			case <-ctx.Done():
				return acp.CloseSessionResponse{}, ctx.Err()
			}
		}
		if closed {
			return acp.CloseSessionResponse{}, nil
		}
		return acp.CloseSessionResponse{}, fmt.Errorf("unknown ACP session %q", request.SessionId)
	}
	runtime.stateMu.Lock()
	runtime.closing = true
	cancel := runtime.turnCancel
	runtime.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
	runtime.promptMu.Lock()
	defer runtime.promptMu.Unlock()
	a.stateMu.Lock()
	if a.sessions[request.SessionId] != runtime {
		_, closed = a.closedSessions[request.SessionId]
		a.stateMu.Unlock()
		if closed {
			return acp.CloseSessionResponse{}, nil
		}
		return acp.CloseSessionResponse{}, fmt.Errorf("unknown ACP session %q", request.SessionId)
	}
	delete(a.sessions, request.SessionId)
	a.closedSessions[request.SessionId] = struct{}{}
	a.stateMu.Unlock()
	return acp.CloseSessionResponse{}, runtime.closeRuntime()
}

func (a *acpAgent) Cancel(_ context.Context, notification acp.CancelNotification) error {
	runtime, err := a.sessionRuntime(notification.SessionId)
	if err != nil {
		return err
	}
	runtime.cancelActiveTurn()
	return nil
}

func (a *acpAgent) ListSessions(_ context.Context, request acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	response := acp.ListSessionsResponse{Sessions: []acp.SessionInfo{}}
	if request.Cursor != nil && *request.Cursor != "" {
		return response, errors.New("q does not use session list cursors")
	}
	if request.Cwd != nil {
		requestedRoot, err := canonicalWorkspaceRoot(*request.Cwd)
		if err != nil {
			return response, err
		}
		if !sameWorkspaceRoot(requestedRoot, a.root) {
			return response, nil
		}
	}
	entries, err := (workspace.Store{Root: a.root}).ListSessions()
	if err != nil {
		return response, err
	}
	for _, entry := range entries {
		info := acp.SessionInfo{SessionId: acpSessionID(entry.Store.SessionID), Cwd: a.root}
		if title := strings.TrimSpace(entry.Title); title != "" {
			info.Title = acp.Ptr(title)
		}
		formatted := entry.UpdatedAt.UTC().Format(time.RFC3339Nano)
		info.UpdatedAt = &formatted
		response.Sessions = append(response.Sessions, info)
	}
	return response, nil
}

// UnstableDeleteSession implements the SDK's current name for ACP session/delete.
func (a *acpAgent) UnstableDeleteSession(
	ctx context.Context,
	request acp.UnstableDeleteSessionRequest,
) (acp.UnstableDeleteSessionResponse, error) {
	sessionID, err := workspaceSessionID(request.SessionId)
	if err != nil {
		return acp.UnstableDeleteSessionResponse{}, nil
	}
	a.stateMu.Lock()
	runtime := a.sessions[request.SessionId]
	pending := a.opening[request.SessionId]
	a.stateMu.Unlock()
	if runtime == nil && pending != nil {
		select {
		case <-pending:
			return a.UnstableDeleteSession(ctx, request)
		case <-ctx.Done():
			return acp.UnstableDeleteSessionResponse{}, ctx.Err()
		}
	}
	if runtime != nil {
		runtime.stateMu.Lock()
		runtime.closing = true
		cancel := runtime.turnCancel
		runtime.stateMu.Unlock()
		if cancel != nil {
			cancel()
		}
		runtime.promptMu.Lock()
		defer runtime.promptMu.Unlock()
		a.stateMu.Lock()
		if a.sessions[request.SessionId] != runtime {
			a.stateMu.Unlock()
			return acp.UnstableDeleteSessionResponse{}, fmt.Errorf("unknown ACP session %q", request.SessionId)
		}
		delete(a.sessions, request.SessionId)
		delete(a.closedSessions, request.SessionId)
		a.stateMu.Unlock()
		store := runtime.state.workspaceStore
		var clearErr error
		if store == nil || store.SessionID != sessionID {
			clearErr = errors.New("active ACP session storage is unavailable")
		}
		runtime.state.stopSessionLearning()
		if clearErr == nil {
			clearErr = store.ClearSession()
		}
		return acp.UnstableDeleteSessionResponse{}, errors.Join(clearErr, runtime.closeRuntime())
	}
	if err := workspace.DeleteSession(a.root, sessionID, "q acp session/delete"); err != nil {
		return acp.UnstableDeleteSessionResponse{}, err
	}
	a.stateMu.Lock()
	delete(a.closedSessions, request.SessionId)
	a.stateMu.Unlock()
	return acp.UnstableDeleteSessionResponse{}, nil
}

func (a *acpAgent) ResumeSession(ctx context.Context, request acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	if err := a.validateSessionWorkspace(request.Cwd, request.AdditionalDirectories); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	if runtime, active := a.activeSession(request.SessionId); active {
		if !runtime.matchesMCPServers(request.McpServers) {
			return acp.ResumeSessionResponse{}, fmt.Errorf("ACP session %q is already active; its MCP servers cannot be changed", request.SessionId)
		}
		if err := runtime.requireSession(request.SessionId); err != nil {
			return acp.ResumeSessionResponse{}, err
		}
		return acp.ResumeSessionResponse{}, nil
	}
	runtime, activated, err := a.activateSession(ctx, request.SessionId, request.McpServers)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	if activated {
		if err := runtime.startLearningIfOpen(); err != nil {
			return acp.ResumeSessionResponse{}, err
		}
	}
	return acp.ResumeSessionResponse{}, nil
}

func (a *acpAgent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	if err := a.validateSessionWorkspace(request.Cwd, request.AdditionalDirectories); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if runtime, active := a.activeSession(request.SessionId); active {
		if !runtime.matchesMCPServers(request.McpServers) {
			return acp.LoadSessionResponse{}, fmt.Errorf("ACP session %q is already active; its MCP servers cannot be changed", request.SessionId)
		}
		runtime.promptMu.Lock()
		defer runtime.promptMu.Unlock()
		if err := runtime.requireSession(request.SessionId); err != nil {
			return acp.LoadSessionResponse{}, err
		}
		if err := runtime.replayWorkspaceSession(ctx); err != nil {
			return acp.LoadSessionResponse{}, err
		}
		return acp.LoadSessionResponse{}, nil
	}
	runtime, activated, err := a.activateSession(ctx, request.SessionId, request.McpServers)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	runtime.promptMu.Lock()
	defer runtime.promptMu.Unlock()
	if err := runtime.requireSession(request.SessionId); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if err := runtime.replayWorkspaceSession(ctx); err != nil {
		a.stateMu.Lock()
		delete(a.sessions, request.SessionId)
		a.stateMu.Unlock()
		_ = runtime.closeRuntime()
		return acp.LoadSessionResponse{}, err
	}
	if activated {
		runtime.launchLearning(runtime.state.startNextLearningSegment())
	}
	return acp.LoadSessionResponse{}, nil
}

func (a *acpAgent) startLearningIfOpen() error {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	if err := a.requireSession(a.sessionID); err != nil {
		return err
	}
	a.launchLearning(a.state.startNextLearningSegment())
	return nil
}

func (a *acpAgent) activeSession(sessionID acp.SessionId) (*acpAgent, bool) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	runtime := a.sessions[sessionID]
	return runtime, runtime != nil
}

func (a *acpAgent) validateSessionWorkspace(cwd string, additionalDirectories []string) error {
	requestedRoot, err := canonicalWorkspaceRoot(cwd)
	if err != nil {
		return err
	}
	if !sameWorkspaceRoot(requestedRoot, a.root) {
		return fmt.Errorf("this ACP process is bound to %q, not %q", a.root, requestedRoot)
	}
	if len(additionalDirectories) > 0 {
		return errors.New("additional workspace directories are not supported")
	}
	return nil
}

func (a *acpAgent) activateSession(
	ctx context.Context,
	sessionID acp.SessionId,
	mcpServers []acp.McpServer,
) (*acpAgent, bool, error) {
	for {
		if err := a.requireRunning(); err != nil {
			return nil, false, err
		}
		a.stateMu.Lock()
		if a.shuttingDown {
			a.stateMu.Unlock()
			return nil, false, errors.New("ACP agent is shutting down")
		}
		if runtime := a.sessions[sessionID]; runtime != nil {
			a.stateMu.Unlock()
			if !runtime.matchesMCPServers(mcpServers) {
				return nil, false, fmt.Errorf("ACP session %q is already active; its MCP servers cannot be changed", sessionID)
			}
			if err := runtime.requireSession(sessionID); err != nil {
				return nil, false, err
			}
			return runtime, false, nil
		}
		if pending := a.opening[sessionID]; pending != nil {
			a.stateMu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
		}
		pending := make(chan struct{})
		a.opening[sessionID] = pending
		a.activationWG.Add(1)
		a.stateMu.Unlock()
		defer a.activationWG.Done()
		defer func() {
			a.stateMu.Lock()
			if a.opening[sessionID] == pending {
				delete(a.opening, sessionID)
				close(pending)
			}
			a.stateMu.Unlock()
		}()
		break
	}
	workspaceID, err := workspaceSessionID(sessionID)
	if err != nil {
		return nil, false, err
	}
	store, err := (workspace.Store{Root: a.root}).ForSession(workspaceID)
	if err != nil {
		return nil, false, fmt.Errorf("unknown ACP session %q", sessionID)
	}
	lock, err := workspace.AcquireSessionLock(a.root, workspaceID, "q acp")
	if err != nil {
		return nil, false, err
	}
	if _, err := store.Load(); errors.Is(err, workspace.ErrNotFound) {
		_ = lock.Close()
		return nil, false, fmt.Errorf("unknown ACP session %q", sessionID)
	} else if err != nil {
		_ = lock.Close()
		return nil, false, err
	}
	runtime := a.newSessionRuntime()
	if err := runtime.activateStore(ctx, store, lock, mcpServers); err != nil {
		_ = lock.Close()
		return nil, false, err
	}
	if err := a.registerSession(runtime); err != nil {
		_ = runtime.closeRuntime()
		return nil, false, err
	}
	return runtime, true, nil
}

func (a *acpAgent) activateStore(
	ctx context.Context,
	store workspace.Store,
	lock *workspace.Lock,
	mcpServers []acp.McpServer,
) error {
	if lock == nil || !lock.Owns(a.root) {
		return errors.New("ACP session lock is unavailable")
	}
	if err := a.configureSessionMCP(ctx, mcpServers); err != nil {
		return err
	}
	a.state.workspaceStore = &store
	a.state.workspaceLock = lock
	a.state.workspaceRestored = false
	a.state.enterChat(a.state.config, a.state.client)
	// TUI recovery is an interactive panel, not an ACP lifecycle response. Keep
	// the checkpoint on disk but do not leave the headless model waiting on an
	// invisible TUI question.
	a.state.planResumePending = false
	a.state.asking = false
	a.state.pendingQuestion = askToUserInput{}
	a.state.planCheckpoint = subagent.ExecutionCheckpoint{}
	a.stateMu.Lock()
	a.sessionID = acpSessionID(store.SessionID)
	a.sessionOpen = true
	a.learningInterrupt = a.state.learningCancel
	a.stateMu.Unlock()
	return nil
}

func (a *acpAgent) configureSessionMCP(ctx context.Context, servers []acp.McpServer) error {
	signature, err := acpMCPServerSignature(servers)
	if err != nil {
		return err
	}
	a.sessionMCPSignature = signature
	baseRuntime := a.coordinator().state.toolRuntime
	if len(servers) == 0 {
		_, configurable := baseRuntime.(externalMCPConfigurer)
		_, scoped := baseRuntime.(interface {
			NewExternalScope(context.Context, string, mcpconfig.Config) (*qtools.ExternalScope, []qtools.ExternalStatus)
		})
		if configurable && !scoped {
			base, err := a.state.mcpSettingsStore.LoadOrDefault()
			if err != nil {
				return err
			}
			a.sessionMCPBase = cloneMCPConfig(base)
			a.sessionMCPBaseSet = true
			a.sessionMCPConfig = cloneMCPConfig(base)
		}
		return nil
	}
	base, err := a.state.mcpSettingsStore.LoadOrDefault()
	if err != nil {
		return err
	}
	a.sessionMCPBase = cloneMCPConfig(base)
	a.sessionMCPBaseSet = true
	merged, sessionIDs, err := mergeACPMCPServers(base, servers)
	if err != nil {
		return err
	}
	a.sessionMCPConfig = cloneMCPConfig(merged)
	a.sessionMCPIDs = sessionIDs
	a.sessionMCPOverride = true

	// The production qtools runtime can own ACP-provided MCP connections in a
	// session overlay. This keeps server process state and routes alive without
	// ever replacing another active session's configuration.
	if scoper, ok := baseRuntime.(interface {
		NewExternalScope(context.Context, string, mcpconfig.Config) (*qtools.ExternalScope, []qtools.ExternalStatus)
	}); ok {
		scopeConfig := acpSessionMCPConfig(merged, sessionIDs)
		scope, statuses := scoper.NewExternalScope(ctx, a.root, scopeConfig)
		if err := ctx.Err(); err != nil {
			_ = scope.Close()
			return err
		}
		for _, status := range statuses {
			if status.Error != "" {
				_ = scope.Close()
				return fmt.Errorf("connect ACP MCP server %s: %s", status.ID, status.Error)
			}
		}
		a.sessionMCPScoped = scope
		a.state.toolRuntime = scope
		return nil
	}

	// Custom runtimes used by embedders may only implement the legacy global
	// replacement API. Validate here and context-switch under the coordinator's
	// MCP mutex for each complete prompt turn.
	configurer, ok := baseRuntime.(externalMCPConfigurer)
	if !ok || configurer == nil {
		return errors.New("ACP-provided MCP servers require q's external MCP runtime")
	}
	coordinator := a.coordinator()
	coordinator.mcpMu.Lock()
	defer coordinator.mcpMu.Unlock()
	statuses := configurer.ConfigureExternal(ctx, a.root, merged)
	if err := ctx.Err(); err != nil {
		_ = a.restoreWorkspaceMCP()
		return err
	}
	for _, status := range statuses {
		if _, supplied := sessionIDs[status.ID]; supplied && status.Error != "" {
			_ = a.restoreWorkspaceMCP()
			return fmt.Errorf("connect ACP MCP server %s: %s", status.ID, status.Error)
		}
	}
	return a.restoreWorkspaceMCP()
}

func acpMCPServerSignature(servers []acp.McpServer) ([sha256.Size]byte, error) {
	body, err := json.Marshal(servers)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("encode ACP MCP server configuration: %w", err)
	}
	return sha256.Sum256(body), nil
}

func (a *acpAgent) matchesMCPServers(servers []acp.McpServer) bool {
	signature, err := acpMCPServerSignature(servers)
	return err == nil && signature == a.sessionMCPSignature
}

func acpSessionMCPConfig(merged mcpconfig.Config, sessionIDs map[string]struct{}) mcpconfig.Config {
	value := mcpconfig.Default()
	for id := range sessionIDs {
		value.Servers[id] = merged.Servers[id]
	}
	for _, role := range mcpconfig.RoleIDs() {
		for _, id := range merged.Roles[role] {
			if _, included := sessionIDs[id]; included {
				value.Roles[role] = append(value.Roles[role], id)
			}
		}
	}
	return value
}

func (a *acpAgent) enterSessionMCP(ctx context.Context) (func(), error) {
	if a.sessionMCPScoped != nil {
		return func() {}, nil
	}
	coordinator := a.coordinator()
	configurer, configurable := coordinator.state.toolRuntime.(externalMCPConfigurer)
	_, scoped := coordinator.state.toolRuntime.(interface {
		NewExternalScope(context.Context, string, mcpconfig.Config) (*qtools.ExternalScope, []qtools.ExternalStatus)
	})
	legacyGlobal := configurable && configurer != nil && !scoped
	if !legacyGlobal {
		if !a.sessionMCPOverride {
			return func() {}, nil
		}
		return nil, errors.New("ACP-provided MCP servers require q's external MCP runtime")
	}
	coordinator.mcpMu.Lock()
	statuses := configurer.ConfigureExternal(ctx, a.root, a.sessionMCPConfig)
	if err := ctx.Err(); err != nil {
		_ = a.restoreWorkspaceMCP()
		coordinator.mcpMu.Unlock()
		return nil, err
	}
	for _, status := range statuses {
		if _, supplied := a.sessionMCPIDs[status.ID]; supplied && status.Error != "" {
			_ = a.restoreWorkspaceMCP()
			coordinator.mcpMu.Unlock()
			return nil, fmt.Errorf("connect ACP MCP server %s: %s", status.ID, status.Error)
		}
	}
	return func() {
		if err := a.restoreWorkspaceMCP(); err != nil && a.logger != nil {
			a.logger.Warn("restore workspace MCP after ACP turn", "error", err)
		}
		coordinator.mcpMu.Unlock()
	}, nil
}

func (a *acpAgent) restoreWorkspaceMCP() error {
	configurer, ok := a.coordinator().state.toolRuntime.(externalMCPConfigurer)
	if !ok || configurer == nil {
		return nil
	}
	a.stateMu.Lock()
	value := cloneMCPConfig(a.sessionMCPBase)
	configured := a.sessionMCPBaseSet
	a.stateMu.Unlock()
	if !configured {
		var err error
		value, err = a.state.mcpSettingsStore.LoadOrDefault()
		if err != nil {
			return err
		}
	}
	parent := a.state.ctx
	if parent == nil {
		parent = context.Background()
	}
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
	defer cancel()
	statuses := configurer.ConfigureExternal(cleanupContext, a.root, value)
	if err := cleanupContext.Err(); err != nil {
		return err
	}
	for _, status := range statuses {
		if status.Error != "" && a.logger != nil {
			a.logger.Warn("restore workspace MCP server", "server", status.ID, "error", status.Error)
		}
	}
	return nil
}

func mergeACPMCPServers(base mcpconfig.Config, servers []acp.McpServer) (mcpconfig.Config, map[string]struct{}, error) {
	value := cloneMCPConfig(base)
	if value.Servers == nil {
		value.Servers = make(map[string]mcpconfig.ServerConfig)
	}
	if value.Roles == nil {
		value.Roles = make(map[string][]string)
	}
	sessionIDs := make(map[string]struct{}, len(servers))
	nextID := 1
	for _, server := range servers {
		id := ""
		for id == "" {
			candidate := fmt.Sprintf("acp_session_%d", nextID)
			nextID++
			if _, exists := value.Servers[candidate]; !exists {
				id = candidate
			}
		}
		var configured mcpconfig.ServerConfig
		switch {
		case server.Stdio != nil:
			configured = mcpconfig.ServerConfig{
				Transport:   mcpconfig.TransportStdio,
				Command:     server.Stdio.Command,
				Args:        append([]string(nil), server.Stdio.Args...),
				ResolvedEnv: make(map[string]string, len(server.Stdio.Env)),
			}
			for _, variable := range server.Stdio.Env {
				configured.ResolvedEnv[variable.Name] = variable.Value
			}
		case server.Http != nil:
			configured = mcpconfig.ServerConfig{
				Transport:       mcpconfig.TransportStreamableHTTP,
				URL:             server.Http.Url,
				ResolvedHeaders: make(map[string]string, len(server.Http.Headers)),
			}
			for _, header := range server.Http.Headers {
				configured.ResolvedHeaders[header.Name] = header.Value
			}
		case server.Sse != nil:
			return mcpconfig.Config{}, nil, errors.New("ACP MCP SSE transport is not supported")
		case server.Acp != nil:
			return mcpconfig.Config{}, nil, errors.New("ACP-transport MCP servers are not supported")
		default:
			return mcpconfig.Config{}, nil, errors.New("ACP MCP server has no supported transport")
		}
		value.Servers[id] = configured
		sessionIDs[id] = struct{}{}
		for _, role := range mcpconfig.RoleIDs() {
			value.Roles[role] = append(value.Roles[role], id)
		}
	}
	if err := value.Validate(); err != nil {
		return mcpconfig.Config{}, nil, err
	}
	return value, sessionIDs, nil
}

func (a *acpAgent) deactivateSession() {
	_ = a.closeRuntime()
}

func (a *acpAgent) replayWorkspaceSession(ctx context.Context) error {
	for _, message := range workspaceSessionMessages(a.state.messages) {
		switch message.Role {
		case client.RoleUser:
			for _, update := range replayACPUserMessage(message) {
				if err := a.updateContext(ctx, update); err != nil {
					return err
				}
			}
		case client.RoleAssistant:
			if message.Content != "" {
				if err := a.updateContext(ctx, acp.UpdateAgentMessageText(message.Content)); err != nil {
					return err
				}
			}
			for _, call := range message.ToolCalls {
				if err := a.startToolCallContext(ctx, call); err != nil {
					return err
				}
			}
		case client.RoleTool:
			if message.ToolCallID == "" {
				continue
			}
			failed := strings.HasPrefix(message.Content, "Tool error:")
			if err := a.finishToolCallContext(ctx, message, failed); err != nil {
				return err
			}
		}
	}
	if err := a.emitSessionInfoContext(ctx, true); err != nil {
		return err
	}
	if a.state.activeTask != nil {
		if err := a.emitTaskPlanContext(ctx, a.state.activeTask.Objective, acp.PlanEntryStatusInProgress); err != nil {
			return err
		}
	}
	if store := a.state.workspaceStore; store != nil {
		checkpoint, err := store.LoadExecution()
		switch {
		case err == nil && checkpoint.RunID == a.state.runID:
			if err := a.emitAgentPlanContext(ctx, planUpdateFromCheckpoint(checkpoint)); err != nil {
				return err
			}
		case err != nil && !errors.Is(err, workspace.ErrExecutionNotFound):
			a.logger.Warn("could not restore ACP plan projection", "session_id", store.SessionID, "error", err)
		}
	}
	if err := a.emitAvailableCommandsContext(ctx); err != nil {
		return err
	}
	return nil
}

func (a *acpAgent) SetSessionMode(_ context.Context, request acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	if _, err := a.sessionRuntime(request.SessionId); err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetMode)
}

func (a *acpAgent) SetSessionConfigOption(_ context.Context, request acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	var sessionID acp.SessionId
	if request.Boolean != nil {
		sessionID = request.Boolean.SessionId
	} else if request.ValueId != nil {
		sessionID = request.ValueId.SessionId
	}
	if _, err := a.sessionRuntime(sessionID); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetConfigOption)
}

func (a *acpAgent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	runtime, err := a.sessionRuntime(request.SessionId)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	response, err := runtime.prompt(ctx, request)
	if err != nil {
		return response, runtime.sanitizeACPPromptError(err)
	}
	return response, nil
}

func (a *acpAgent) sanitizeACPPromptError(err error) error {
	var persistenceErr *workspaceSessionSaveError
	if !errors.As(err, &persistenceErr) {
		return err
	}
	if a.logger != nil {
		a.logger.Error("persist ACP session", "session_id", a.sessionID, "error", persistenceErr)
	}
	return acp.NewInternalError(map[string]any{
		"error": "Q could not persist the session. Please retry.",
	})
}

func (a *acpAgent) prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	if err := a.requireSession(request.SessionId); err != nil {
		return acp.PromptResponse{}, err
	}
	userMessage, hasNonText, err := acpPromptMessage(request.Prompt, a.state.supportsACPImages())
	if err != nil {
		return acp.PromptResponse{}, err
	}
	text := userMessage.TextContent()
	if !hasNonText && isWorkspaceLearningControl(text) {
		response, err := a.coordinator().runACPWorkspaceLearningControl(ctx, a, strings.TrimSpace(text))
		response.UserMessageId = request.MessageId
		return response, err
	}

	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	if a.promptLocked != nil {
		a.promptLocked()
	}
	if next := a.applyPendingLearningLocked(); next != nil {
		a.launchLearning(next)
	}
	turnContext, cancel := context.WithCancel(ctx)
	coordinator := a.coordinator()
	coordinator.stateMu.Lock()
	shuttingDown := coordinator.shuttingDown
	coordinator.stateMu.Unlock()
	if shuttingDown {
		cancel()
		return acp.PromptResponse{}, errors.New("ACP agent is shutting down")
	}
	a.stateMu.Lock()
	if a.closing || !a.sessionOpen || request.SessionId != a.sessionID {
		a.stateMu.Unlock()
		cancel()
		return acp.PromptResponse{}, fmt.Errorf("unknown ACP session %q", request.SessionId)
	}
	a.turnCancel = cancel
	a.stateMu.Unlock()
	defer func() {
		cancel()
		a.stateMu.Lock()
		a.turnCancel = nil
		a.stateMu.Unlock()
	}()
	restoreMCP, err := a.enterSessionMCP(turnContext)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	defer restoreMCP()
	if err := a.emitAvailableCommandsContext(turnContext); err != nil {
		return acp.PromptResponse{}, err
	}
	if response, handled, commitErr := a.runPendingACPCommit(turnContext, userMessage, hasNonText); handled {
		response.UserMessageId = request.MessageId
		return response, commitErr
	}
	if response, handled, questionErr := a.runPendingACPQuestion(turnContext, userMessage, hasNonText); handled {
		response.UserMessageId = request.MessageId
		return response, questionErr
	}
	if strings.TrimSpace(text) == "" && !hasNonText {
		return acp.PromptResponse{}, errors.New("prompt contains no supported content")
	}
	if !hasNonText {
		if response, handled, commandErr := a.runACPCommand(turnContext, text); handled {
			response.UserMessageId = request.MessageId
			return response, commandErr
		}
	}

	response, err := a.runPrompt(turnContext, userMessage)
	response.UserMessageId = request.MessageId
	return response, err
}

func isWorkspaceLearningControl(command string) bool {
	switch strings.TrimSpace(command) {
	case "/learn on", "/learn off", "/learn status":
		return true
	default:
		return false
	}
}

func (a *acpAgent) runACPWorkspaceLearningControl(
	ctx context.Context,
	target *acpAgent,
	command string,
) (acp.PromptResponse, error) {
	a.learningMu.Lock()
	defer a.learningMu.Unlock()
	target.promptMu.Lock()
	defer target.promptMu.Unlock()
	turnContext, cancel := context.WithCancel(ctx)
	a.stateMu.Lock()
	shuttingDown := a.shuttingDown
	a.stateMu.Unlock()
	if shuttingDown {
		cancel()
		return acp.PromptResponse{}, errors.New("ACP agent is shutting down")
	}
	target.stateMu.Lock()
	if target.closing || !target.sessionOpen {
		target.stateMu.Unlock()
		cancel()
		return acp.PromptResponse{}, fmt.Errorf("unknown ACP session %q", target.sessionID)
	}
	target.turnCancel = cancel
	target.stateMu.Unlock()
	defer func() {
		cancel()
		target.stateMu.Lock()
		target.turnCancel = nil
		target.stateMu.Unlock()
	}()
	if next := target.applyPendingLearningLocked(); next != nil {
		target.launchLearning(next)
	}
	if target.pendingCommit != nil || target.pendingQuestion != nil {
		restoreMCP, err := target.enterSessionMCP(turnContext)
		if err != nil {
			return acp.PromptResponse{}, err
		}
		defer restoreMCP()
	}
	if err := target.emitAvailableCommandsContext(turnContext); err != nil {
		return acp.PromptResponse{}, err
	}
	if response, handled, commitErr := target.runPendingACPCommit(
		turnContext,
		client.Message{Role: client.RoleUser, Content: command},
		false,
	); handled {
		return response, commitErr
	}
	if response, handled, questionErr := target.runPendingACPQuestion(
		turnContext,
		client.Message{Role: client.RoleUser, Content: command},
		false,
	); handled {
		return response, questionErr
	}

	output := ""
	var next tea.Cmd
	switch command {
	case "/learn status":
		if target.state.learningDisabled() {
			output = "Learning is disabled for this workspace."
		} else {
			output = "Learning is enabled for this workspace."
		}
	case "/learn off":
		if err := turnContext.Err(); err != nil {
			return acp.PromptResponse{}, err
		}
		if err := target.state.setWorkspaceLearningDisabled(true); err != nil {
			return acp.PromptResponse{}, err
		}
		target.syncLearningInterrupt()
		a.propagateLearningSetting(target, true)
		output = "Learning disabled for this workspace."
	case "/learn on":
		if err := turnContext.Err(); err != nil {
			return acp.PromptResponse{}, err
		}
		if err := target.state.setWorkspaceLearningDisabled(false); err != nil {
			return acp.PromptResponse{}, err
		}
		target.syncLearningInterrupt()
		a.propagateLearningSetting(target, false)
		next = target.state.startNextLearningSegment()
		output = "Learning enabled for this workspace."
	}
	if err := target.updateContext(turnContext, acp.UpdateAgentMessageText(output)); err != nil {
		return acp.PromptResponse{}, err
	}
	target.launchLearning(next)
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *acpAgent) propagateLearningSetting(target *acpAgent, disabled bool) {
	a.stateMu.Lock()
	a.learningVersion++
	version := a.learningVersion
	runtimes := make([]*acpAgent, 0, len(a.sessions))
	for _, runtime := range a.sessions {
		runtimes = append(runtimes, runtime)
	}
	a.stateMu.Unlock()
	target.stateMu.Lock()
	target.learningVersion = version
	target.learningPending = false
	target.learningDisabled = disabled
	target.stateMu.Unlock()
	for _, runtime := range runtimes {
		if runtime != target {
			runtime.scheduleLearningSetting(version, disabled)
		}
	}
}

func (a *acpAgent) scheduleLearningSetting(version uint64, disabled bool) {
	a.stateMu.Lock()
	if version < a.learningVersion || a.closing || !a.sessionOpen {
		a.stateMu.Unlock()
		return
	}
	a.learningVersion = version
	a.learningPending = true
	a.learningDisabled = disabled
	interrupt := a.learningInterrupt
	a.stateMu.Unlock()
	if disabled && interrupt != nil {
		interrupt()
	}
	apply := func() {
		a.promptMu.Lock()
		next := a.applyPendingLearningLocked()
		a.promptMu.Unlock()
		a.launchLearning(next)
	}
	if a.promptMu.TryLock() {
		next := a.applyPendingLearningLocked()
		a.promptMu.Unlock()
		a.launchLearning(next)
		return
	}
	go apply()
}

// applyPendingLearningLocked reconciles the process-wide workspace setting
// with one session model. The caller owns promptMu.
func (a *acpAgent) applyPendingLearningLocked() tea.Cmd {
	a.stateMu.Lock()
	if !a.learningPending || a.closing || !a.sessionOpen {
		a.stateMu.Unlock()
		return nil
	}
	disabled := a.learningDisabled
	a.learningPending = false
	a.stateMu.Unlock()
	a.state.workspaceLearning = workspace.LearningConfig{
		Version: workspace.LearningConfigVersion, Disabled: disabled,
	}
	if disabled {
		a.state.stopSessionLearning()
	} else {
		a.state.resetSessionLearning()
	}
	a.syncLearningInterrupt()
	if disabled {
		return nil
	}
	return a.state.startNextLearningSegment()
}

func (a *acpAgent) syncLearningInterrupt() {
	a.stateMu.Lock()
	a.learningInterrupt = a.state.learningCancel
	a.stateMu.Unlock()
}

func (a *acpAgent) runPrompt(ctx context.Context, userMessage client.Message) (acp.PromptResponse, error) {
	a.state.turnMessageStart = len(a.state.messages)
	titleSource := userMessage.TextContent()
	if titleSource == "" {
		titleSource = "Image prompt"
	}
	titleChanged := a.state.touchSessionMetadata(titleSource)
	a.state.archiveMessage(userMessage, sessionstore.StatusSubmitted, false)
	a.state.messages = append(a.state.messages, userMessage)
	a.state.memory.Append(userMessage)
	a.launchLearning(a.state.observeLearningMessage(userMessage))
	if err := a.state.saveWorkspaceSession(); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.emitSessionInfo(titleChanged); err != nil {
		return acp.PromptResponse{}, err
	}

	if err := a.compactIfNeeded(ctx); err != nil {
		a.state.archiveFailure("ACP context compaction failed", err)
		_ = a.state.flushArchive()
		return acp.PromptResponse{}, err
	}
	return a.runAgentTurn(ctx, a.state.memory.Messages())
}

func (a *acpAgent) runAgentTurn(ctx context.Context, history []client.Message) (acp.PromptResponse, error) {
	toolRuntime, err := configuredAgentToolRuntime(
		a.state.toolRuntime, mcpconfig.RoleDefault, a.state.activeConfig(), a.root,
	)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	_, capabilities := a.connectionState()
	persistent := capabilities.Elicitation == nil || capabilities.Elicitation.Form == nil
	workflowCtx := ctx
	cancelWorkflow := func() {}
	if persistent {
		workflowCtx, cancelWorkflow = context.WithCancel(a.state.ctx)
	}
	events := make(chan agentEvent)
	go streamAgentLoop(
		workflowCtx,
		a.state.client,
		toolRuntime,
		a.state.activeModel(),
		a.state.activeConfig().Provider.EffectiveReasoningEffort(),
		history,
		a.state.conversationID,
		a.state.activeTask,
		a.state.streamsActiveChat(),
		modelNeedsSystemInstructionCoalescing(
			a.state.gatewayConfig, a.state.activeConfig().ModelGroups, a.state.activeModel(), nil,
		),
		events,
	)

	streamedResponse := ""
	return a.continueACPAgentTurn(ctx, workflowCtx, cancelWorkflow, persistent, events, &streamedResponse)
}

func (a *acpAgent) continueACPAgentTurn(
	ctx context.Context,
	workflowCtx context.Context,
	cancelWorkflow context.CancelFunc,
	persistent bool,
	events <-chan agentEvent,
	streamedResponse *string,
) (response acp.PromptResponse, runErr error) {
	suspended := false
	stopPromptCancel := func() bool { return true }
	if persistent {
		stopPromptCancel = context.AfterFunc(ctx, cancelWorkflow)
	}
	defer func() {
		stopPromptCancel()
		if persistent && !suspended {
			cancelWorkflow()
		}
	}()

	for event := range events {
		if event.taskStarted != nil {
			a.state.activeTask = event.taskStarted
			if err := a.state.saveWorkspaceSession(); err != nil {
				return acp.PromptResponse{}, err
			}
			if err := a.emitTaskPlan(event.taskStarted.Objective, acp.PlanEntryStatusInProgress); err != nil {
				return acp.PromptResponse{}, err
			}
		}
		if event.taskCompleted {
			objective := ""
			if a.state.activeTask != nil {
				objective = a.state.activeTask.Objective
			}
			a.state.activeTask = nil
			if err := a.state.saveWorkspaceSession(); err != nil {
				return acp.PromptResponse{}, err
			}
			if objective != "" {
				if err := a.emitTaskPlan(objective, acp.PlanEntryStatusCompleted); err != nil {
					return acp.PromptResponse{}, err
				}
			}
		}
		if event.learningName != "" {
			a.launchLearning(a.state.enqueueLearningSpecial(event.learningName, event.learningPayload))
		}
		if event.streamDelta != nil {
			if event.streamDelta.Start && event.streamDelta.Kind == chatStreamResponse {
				*streamedResponse = ""
			}
			if event.streamDelta.Kind == chatStreamThinking {
				if err := a.update(acp.UpdateAgentThoughtText(event.streamDelta.Content)); err != nil {
					return acp.PromptResponse{}, err
				}
			} else {
				*streamedResponse += event.streamDelta.Content
				if err := a.update(acp.UpdateAgentMessageText(event.streamDelta.Content)); err != nil {
					return acp.PromptResponse{}, err
				}
			}
		}
		if event.call != nil {
			a.state.archiveToolCall(*event.call)
			if err := a.startToolCall(*event.call); err != nil {
				return acp.PromptResponse{}, err
			}
		}
		if event.question != nil {
			if !persistent {
				event.answer <- a.elicitAnswer(ctx, *event.question)
			} else {
				response, err := a.suspendACPQuestion(
					ctx,
					*event.question,
					event.answer,
					false,
					false,
					true,
					func(nextCtx context.Context) (acp.PromptResponse, error) {
						return a.continueACPAgentTurn(
							nextCtx, workflowCtx, cancelWorkflow, true, events, streamedResponse,
						)
					},
					func() error {
						cancelWorkflow()
						return nil
					},
				)
				if err != nil {
					return acp.PromptResponse{}, err
				}
				suspended = true
				return response, nil
			}
		}
		if event.message != nil {
			message := *event.message
			a.state.messages = append(a.state.messages, message)
			a.state.memory.Append(message)
			a.launchLearning(a.state.observeLearningMessage(message))
			if message.Role == client.RoleTool {
				if event.toolIsError {
					a.state.archiveMessage(message, sessionstore.StatusFailed, true)
				} else {
					a.state.archiveMessage(message, sessionstore.StatusSucceeded, false)
				}
				if err := a.finishToolCall(message, event.toolIsError); err != nil {
					return acp.PromptResponse{}, err
				}
				*streamedResponse = ""
			} else if message.Role == client.RoleAssistant {
				a.state.archiveMessage(message, sessionstore.StatusSucceeded, false)
				if err := a.emitMissingAssistantText(message.Content, streamedResponse); err != nil {
					return acp.PromptResponse{}, err
				}
			}
			if err := a.state.saveWorkspaceSession(); err != nil {
				return acp.PromptResponse{}, err
			}
		}
		if event.err != nil {
			if errors.Is(event.err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) ||
				errors.Is(workflowCtx.Err(), context.Canceled) {
				return a.finishCancelledPrompt(), nil
			}
			a.state.archiveFailure("ACP agent turn failed", event.err)
			_ = a.state.flushArchive()
			return acp.PromptResponse{}, event.err
		}
		if event.response != nil {
			return a.finishPrompt(*event.response, event.requestEstimate, streamedResponse)
		}
	}

	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(workflowCtx.Err(), context.Canceled) {
		return a.finishCancelledPrompt(), nil
	}
	return acp.PromptResponse{}, errors.New("agent turn ended without a response")
}

func (a *acpAgent) finishCancelledPrompt() acp.PromptResponse {
	previousLength := len(a.state.messages)
	a.state.completeInterruptedToolCalls()
	for _, message := range a.state.messages[previousLength:] {
		if message.Role == client.RoleTool {
			_ = a.finishToolCall(message, true)
		}
	}
	a.state.archiveTurnCancelled("cancelled by ACP client")
	a.state.sessionUpdatedAt = time.Now().UTC()
	if err := a.state.saveWorkspaceSession(); err != nil && a.logger != nil {
		a.logger.Error("persist cancelled ACP turn", "error", err)
	}
	if err := a.emitSessionInfo(false); err != nil && a.logger != nil {
		a.logger.Error("emit cancelled ACP session metadata", "error", err)
	}
	if err := a.state.flushArchive(); err != nil && a.logger != nil {
		a.logger.Error("flush cancelled ACP turn", "error", err)
	}
	return acp.PromptResponse{StopReason: acp.StopReasonCancelled}
}

func (a *acpAgent) finishPrompt(response client.ChatResponse, requestEstimate int, streamedResponse *string) (acp.PromptResponse, error) {
	if len(response.Choices) == 0 {
		return acp.PromptResponse{}, errors.New("model returned no response choices")
	}
	message := response.Choices[0].Message
	if message.Role == "" {
		message.Role = client.RoleAssistant
	}
	if err := a.emitMissingAssistantText(message.Content, streamedResponse); err != nil {
		return acp.PromptResponse{}, err
	}
	a.state.messages = append(a.state.messages, message)
	a.state.memory.Append(message)
	a.state.memory.ObserveUsage(response.Usage.PromptTokens, requestEstimate)
	a.state.conversationID = response.ConversationID
	a.state.archiveMessage(message, sessionstore.StatusSucceeded, false)
	a.launchLearning(a.state.observeLearningMessage(message))
	a.state.sessionUpdatedAt = time.Now().UTC()
	if err := a.state.saveWorkspaceSession(); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.emitSessionInfo(false); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.state.flushArchive(); err != nil {
		if a.logger != nil {
			a.logger.Error("flush ACP session archive", "error", err)
		}
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *acpAgent) compactIfNeeded(ctx context.Context) error {
	if !a.state.memory.ShouldCompact() {
		return nil
	}
	plan, err := a.state.memory.Plan()
	if err != nil {
		return err
	}
	maxCompletionTokens := plan.OutputBudget
	response, err := chatWithConversationRecovery(ctx, a.state.client, client.ChatRequest{
		Model:               a.state.activeModel(),
		ReasoningEffort:     a.state.activeConfig().Provider.EffectiveReasoningEffort(),
		Messages:            plan.RequestMessages(),
		MaxCompletionTokens: &maxCompletionTokens,
	})
	if err != nil {
		return err
	}
	if len(response.Choices) == 0 {
		return errors.New("context compaction returned no response choices")
	}
	summary := response.Choices[0].Message.Content
	if err := a.state.memory.Apply(plan, summary); err != nil {
		return err
	}
	a.state.conversationID = ""
	a.state.archiveSummary(summary)
	return a.state.saveWorkspaceSession()
}

func (a *acpAgent) emitMissingAssistantText(content string, streamed *string) error {
	if content == "" || content == *streamed {
		return nil
	}
	missing := content
	if strings.HasPrefix(content, *streamed) {
		missing = strings.TrimPrefix(content, *streamed)
	}
	if missing != "" {
		if err := a.update(acp.UpdateAgentMessageText(missing)); err != nil {
			return err
		}
	}
	*streamed = content
	return nil
}

func (a *acpAgent) startToolCall(call client.ToolCall) error {
	return a.startToolCallContext(a.state.ctx, call)
}

func (a *acpAgent) startToolCallContext(ctx context.Context, call client.ToolCall) error {
	title := describeToolCall(call)
	if title == "" {
		title = call.Function.Name
	}
	return a.updateContext(ctx, acp.StartToolCall(
		acp.ToolCallId(call.ID),
		title,
		acp.WithStartKind(classifyACPTool(call.Function.Name)),
		acp.WithStartStatus(acp.ToolCallStatusInProgress),
		acp.WithStartLocations(toolLocations(a.root, call)),
		acp.WithStartRawInput(decodeJSONValue(call.Function.Arguments)),
	))
}

func (a *acpAgent) finishToolCall(message client.Message, failed bool) error {
	return a.finishToolCallContext(a.state.ctx, message, failed)
}

func (a *acpAgent) finishToolCallContext(ctx context.Context, message client.Message, failed bool) error {
	status := acp.ToolCallStatusCompleted
	if failed {
		status = acp.ToolCallStatusFailed
	}
	return a.updateContext(ctx, acp.UpdateToolCall(
		acp.ToolCallId(message.ToolCallID),
		acp.WithUpdateStatus(status),
		acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(message.Content))}),
		acp.WithUpdateRawOutput(decodeJSONValue(message.Content)),
	))
}

func (a *acpAgent) elicitAnswer(ctx context.Context, question askToUserInput) askToUserOutput {
	connection, capabilities := a.connectionState()
	sessionID := a.sessionID
	supportsForm := capabilities.Elicitation != nil && capabilities.Elicitation.Form != nil
	if connection == nil || !supportsForm {
		return askToUserOutput{Err: errors.New("ACP client does not support form elicitation")}
	}

	description := question.Question
	if question.Context != "" {
		description += "\n\nContext: " + question.Context
	}
	if len(question.Choices) > 0 {
		description += "\n\nChoices:"
		for _, choice := range question.Choices {
			description += fmt.Sprintf("\n- %s: %s", choice.ID, choice.Label)
		}
	}
	response, err := connection.UnstableCreateElicitation(ctx, acp.UnstableCreateElicitationRequest{
		Form: &acp.UnstableCreateElicitationForm{
			Message:   question.Question,
			Mode:      "form",
			SessionId: sessionID,
			RequestedSchema: acp.UnstableElicitationSchema{
				Type: "object",
				Properties: map[string]any{
					"answer": map[string]any{"type": "string", "description": description},
				},
				Required: []string{"answer"},
			},
		},
	})
	if err != nil {
		return askToUserOutput{Err: fmt.Errorf("ACP elicitation failed: %w", err)}
	}
	if response.Decline != nil {
		return askToUserOutput{Err: fmt.Errorf("ACP elicitation declined: %w", context.Canceled)}
	}
	if response.Cancel != nil {
		return askToUserOutput{Err: fmt.Errorf("ACP elicitation cancelled: %w", context.Canceled)}
	}
	if response.Accept == nil {
		return askToUserOutput{Err: errors.New("ACP elicitation returned no outcome")}
	}
	answer, ok := response.Accept.Content["answer"].(string)
	if !ok || strings.TrimSpace(answer) == "" {
		return askToUserOutput{Err: errors.New("ACP elicitation returned no answer")}
	}
	return answerForQuestion(question, answer)
}

func (a *acpAgent) emitSessionInfo(includeTitle bool) error {
	return a.emitSessionInfoContext(a.state.ctx, includeTitle)
}

func (a *acpAgent) emitSessionInfoContext(ctx context.Context, includeTitle bool) error {
	update := &acp.SessionSessionInfoUpdate{}
	if includeTitle && a.state.sessionTitle != "" {
		update.Title = acp.Ptr(a.state.sessionTitle)
	}
	if !a.state.sessionUpdatedAt.IsZero() {
		formatted := a.state.sessionUpdatedAt.UTC().Format(time.RFC3339Nano)
		update.UpdatedAt = &formatted
	}
	if update.Title == nil && update.UpdatedAt == nil {
		return nil
	}
	return a.updateContext(ctx, acp.SessionUpdate{SessionInfoUpdate: update})
}

func (a *acpAgent) emitTaskPlan(objective string, status acp.PlanEntryStatus) error {
	return a.emitTaskPlanContext(a.state.ctx, objective, status)
}

func (a *acpAgent) emitTaskPlanContext(ctx context.Context, objective string, status acp.PlanEntryStatus) error {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return nil
	}
	return a.updateContext(ctx, acp.UpdatePlan(acp.PlanEntry{
		Content: objective, Priority: acp.PlanEntryPriorityHigh, Status: status,
	}))
}

func (a *acpAgent) emitAgentPlanContext(ctx context.Context, update agentPlanUpdate) error {
	entries := make([]acp.PlanEntry, 0, len(update.Entries))
	for _, entry := range update.Entries {
		status := acp.PlanEntryStatusPending
		switch entry.Status {
		case agentPlanInProgress:
			status = acp.PlanEntryStatusInProgress
		case agentPlanCompleted:
			status = acp.PlanEntryStatusCompleted
		}
		entries = append(entries, acp.PlanEntry{
			Content: entry.Content, Priority: acp.PlanEntryPriorityMedium, Status: status,
		})
	}
	return a.updateContext(ctx, acp.UpdatePlan(entries...))
}

func (a *acpAgent) emitAvailableCommandsContext(ctx context.Context) error {
	commands := []acp.AvailableCommand{
		{
			Name: "plan", Description: "Plan an implementation, request approval, then execute and review it.",
			Input: &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: "work to plan"}},
		},
		{
			Name: "debug", Description: "Investigate an issue and return an evidence-backed diagnostic report.",
			Input: &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: "issue to investigate"}},
		},
		{
			Name: "review", Description: "Review current working-tree changes without modifying them.",
			Input: &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: "optional review focus"}},
		},
		{
			Name: "auto-approve", Description: "Persistently control automatic plan approval.",
			Input: &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: "on, off, or status"}},
		},
		{
			Name: "auto-resolve", Description: "Persistently control automatic plan clarification.",
			Input: &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: "on, off, or status"}},
		},
		{
			Name: "autonomous", Description: "Persistently control both plan automation settings.",
			Input: &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: "on, off, or status"}},
		},
		{
			Name: "agent:search", Description: "Run the configured ACP Search agent and return its evidence report.",
			Input: &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: "query"}},
		},
		{Name: "commit", Description: "Review generated commit proposals, then commit or commit and push after approval."},
		{
			Name: "learn", Description: "Checkpoint or control q learning for this workspace.",
			Input: &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: "on, off, or status"}},
		},
		{Name: "clear", Description: "Clear q's conversation context for this workspace."},
		{Name: "help", Description: "Show the slash commands available through ACP."},
	}
	return a.updateContext(ctx, acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
		AvailableCommands: commands,
	}})
}

func (a *acpAgent) runACPCommand(ctx context.Context, text string) (acp.PromptResponse, bool, error) {
	command := strings.TrimSpace(text)
	planCommand, planAutomationControl := parsePlanAutomationCommand(command)
	var output string
	switch {
	case planAutomationControl:
		if !planCommand.valid {
			output = planCommand.usage()
			break
		}
		var err error
		output, err = a.runACPPlanAutomationCommand(planCommand)
		if err != nil {
			return acp.PromptResponse{}, true, err
		}
	case command == "/commit":
		response, err := a.runACPCommit(ctx)
		return response, true, err
	case command == agentSearchCommand:
		output = "Usage: /agent:search <query>"
	case strings.HasPrefix(command, agentSearchCommand+" "):
		query := strings.TrimSpace(strings.TrimPrefix(command, agentSearchCommand))
		if query == "" {
			output = "Usage: /agent:search <query>"
			break
		}
		response, err := a.runACPAgentSearch(ctx, query)
		return response, true, err
	case command == "/plan":
		output = "Usage: /plan <work to plan>"
	case strings.HasPrefix(command, "/plan "):
		objective := strings.TrimSpace(strings.TrimPrefix(command, "/plan "))
		if objective == "" {
			output = "Usage: /plan <work to plan>"
			break
		}
		response, err := a.runACPPlan(ctx, objective)
		return response, true, err
	case command == "/debug":
		output = "Usage: /debug <issue to investigate>"
	case strings.HasPrefix(command, "/debug "):
		issue := strings.TrimSpace(strings.TrimPrefix(command, "/debug "))
		if issue == "" {
			output = "Usage: /debug <issue to investigate>"
			break
		}
		response, err := a.runACPDebug(ctx, issue)
		return response, true, err
	case command == "/review":
		response, err := a.runACPReview(ctx, "")
		return response, true, err
	case strings.HasPrefix(command, "/review "):
		request := strings.TrimSpace(strings.TrimPrefix(command, "/review "))
		response, err := a.runACPReview(ctx, request)
		return response, true, err
	case command == "/help":
		output = "Available ACP commands:\n- /plan <work to plan>\n- /debug <issue to investigate>\n- /review [optional review focus]\n- /auto-approve [on|off|status]\n- /auto-resolve [on|off|status]\n- /autonomous [on|off|status]\n- /agent:search <query>\n- /commit\n- /learn [on|off|status]\n- /clear\n- /help"
	case command == "/learn":
		if a.state.learningDisabled() {
			output = "Learning is disabled for this workspace. Use /learn on to enable it."
		} else {
			output = "Learning checkpoint enqueued."
			a.launchLearning(a.state.enqueueExplicitLearning())
		}
	case command == "/learn on":
		if err := a.state.setWorkspaceLearningDisabled(false); err != nil {
			return acp.PromptResponse{}, true, err
		}
		output = "Learning enabled for this workspace."
		a.launchLearning(a.state.startNextLearningSegment())
	case command == "/learn off":
		if err := a.state.setWorkspaceLearningDisabled(true); err != nil {
			return acp.PromptResponse{}, true, err
		}
		output = "Learning disabled for this workspace."
	case command == "/learn status":
		if a.state.learningDisabled() {
			output = "Learning is disabled for this workspace."
		} else {
			output = "Learning is enabled for this workspace."
		}
	case command == "/clear":
		if err := a.clearACPConversation(ctx); err != nil {
			return acp.PromptResponse{}, true, err
		}
		output = "Conversation cleared for this workspace."
	default:
		if strings.HasPrefix(command, "/learn ") {
			output = "Usage: /learn [on|off|status]"
		} else {
			return acp.PromptResponse{}, false, nil
		}
	}
	if err := a.updateContext(ctx, acp.UpdateAgentMessageText(output)); err != nil {
		return acp.PromptResponse{}, true, err
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, true, nil
}

func (a *acpAgent) runACPPlanAutomationCommand(command planAutomationCommand) (string, error) {
	coordinator := a.coordinator()
	coordinator.planConfigMu.Lock()
	defer coordinator.planConfigMu.Unlock()
	configured := coordinator.state.config.Plan
	next, save := command.apply(configured)
	if save {
		value := coordinator.state.config
		value.Plan = next
		if err := coordinator.state.store.Save(value); err != nil {
			return "", err
		}
		coordinator.state.config = value
		configured = next
	}
	effective := applyPlanAutomationOverrides(configured, a.planOverrides)
	return renderPlanAutomationCommand(command, configured, effective, save), nil
}

func (a *acpAgent) effectivePlanConfig() config.PlanConfig {
	coordinator := a.coordinator()
	coordinator.planConfigMu.Lock()
	configured := coordinator.state.config.Plan
	coordinator.planConfigMu.Unlock()
	return applyPlanAutomationOverrides(configured, a.planOverrides)
}

func (a *acpAgent) clearACPConversation(ctx context.Context) error {
	runID := a.state.runID
	a.state.resetConversationState(runID)
	a.syncLearningInterrupt()
	// An ACP slash command cannot replace the active client-side session ID.
	// Keep its backing run stable while clearing only q's conversation projection.
	if a.state.workspaceStore == nil {
		return errors.New("workspace session storage is unavailable")
	}
	if err := errors.Join(a.state.workspaceStore.ClearExecution(), a.state.saveWorkspaceSession()); err != nil {
		return err
	}
	emptyTitle := ""
	updatedAt := a.state.sessionUpdatedAt.Format(time.RFC3339Nano)
	return a.updateContext(ctx, acp.SessionUpdate{SessionInfoUpdate: &acp.SessionSessionInfoUpdate{
		Title: &emptyTitle, UpdatedAt: &updatedAt,
	}})
}

func (a *acpAgent) runACPPlan(ctx context.Context, objective string) (acp.PromptResponse, error) {
	if a.state.client == nil || a.state.toolRuntime == nil {
		return acp.PromptResponse{}, errors.New("ACP plan requires an available model and tool runtime")
	}
	value := a.state.activeConfig()
	value.Plan = a.effectivePlanConfig()
	_, capabilities := a.connectionState()
	supportsForm := capabilities.Elicitation != nil && capabilities.Elicitation.Form != nil

	a.state.turnMessageStart = len(a.state.messages)
	titleChanged := a.state.touchSessionMetadata(objective)
	message := client.Message{Role: client.RoleUser, Content: objective}
	a.state.archiveMessage(message, sessionstore.StatusSubmitted, false)
	a.state.messages = append(a.state.messages, message)
	if a.state.memory == nil {
		a.state.memory = memoryForPlan(a.state.activeConfig())
	}
	a.state.memory.Append(message)
	a.launchLearning(a.state.observeLearningMessage(message))
	if err := a.state.saveWorkspaceSession(); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.emitSessionInfoContext(ctx, titleChanged); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.emitTaskPlanContext(ctx, objective, acp.PlanEntryStatusInProgress); err != nil {
		return acp.PromptResponse{}, err
	}

	workingDirectory := ""
	var executionStore planExecutionStore
	if a.state.workspaceStore != nil {
		workingDirectory = a.state.workspaceStore.Root
		executionStore = a.state.workspaceStore
	}
	traceID, err := sessionstore.NewID()
	if err != nil {
		return acp.PromptResponse{}, err
	}
	trace := newACPPlanTrace(a.root, traceID, a.updateContext)
	persistent := !supportsForm
	workflowParent := ctx
	if persistent {
		workflowParent = a.state.ctx
	}
	workflowCtx, cancel := context.WithCancel(workflowParent)
	events := make(chan agentEvent)
	go streamPlanWorkflow(
		workflowCtx, a.state.client, a.state.toolRuntime, value, a.state.runID, a.state.archive,
		workingDirectory, objective, planContext(a.state.memory.Messages()), executionStore, events,
	)
	run := &acpPlanContinuation{
		workflowCtx: workflowCtx, cancel: cancel, events: events, trace: trace, objective: objective, workflow: "plan",
	}
	return a.continueACPPlan(ctx, run, persistent)
}

func (a *acpAgent) runACPDebug(ctx context.Context, issue string) (acp.PromptResponse, error) {
	if a.state.client == nil || a.state.toolRuntime == nil {
		return acp.PromptResponse{}, errors.New("ACP debug requires an available model and tool runtime")
	}
	value := a.state.activeConfig()
	value.Plan = a.effectivePlanConfig()
	_, capabilities := a.connectionState()
	supportsForm := capabilities.Elicitation != nil && capabilities.Elicitation.Form != nil

	a.state.turnMessageStart = len(a.state.messages)
	titleChanged := a.state.touchSessionMetadata(issue)
	message := client.Message{Role: client.RoleUser, Content: issue}
	a.state.archiveMessage(message, sessionstore.StatusSubmitted, false)
	a.state.messages = append(a.state.messages, message)
	if a.state.memory == nil {
		a.state.memory = memoryForPlan(a.state.activeConfig())
	}
	a.state.memory.Append(message)
	a.launchLearning(a.state.observeLearningMessage(message))
	if err := a.state.saveWorkspaceSession(); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.emitSessionInfoContext(ctx, titleChanged); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.emitTaskPlanContext(ctx, issue, acp.PlanEntryStatusInProgress); err != nil {
		return acp.PromptResponse{}, err
	}

	workingDirectory := ""
	var executionStore debugExecutionStore
	if a.state.workspaceStore != nil {
		workingDirectory = a.state.workspaceStore.Root
		executionStore = a.state.workspaceStore
	}
	traceID, err := sessionstore.NewID()
	if err != nil {
		return acp.PromptResponse{}, err
	}
	trace := newACPPlanTrace(a.root, traceID, a.updateContext)
	persistent := !supportsForm
	workflowParent := ctx
	if persistent {
		workflowParent = a.state.ctx
	}
	workflowCtx, cancel := context.WithCancel(workflowParent)
	events := make(chan agentEvent)
	go streamDebugWorkflow(
		workflowCtx, a.state.client, a.state.toolRuntime, value, a.state.runID, a.state.archive,
		workingDirectory, issue, planContext(a.state.memory.Messages()), executionStore, events,
	)
	run := &acpPlanContinuation{
		workflowCtx: workflowCtx, cancel: cancel, events: events, trace: trace,
		objective: issue, workflow: "debug",
	}
	return a.continueACPPlan(ctx, run, persistent)
}

func (a *acpAgent) runACPReview(ctx context.Context, requestText string) (acp.PromptResponse, error) {
	if a.state.client == nil || a.state.toolRuntime == nil || a.state.workspaceStore == nil {
		return acp.PromptResponse{}, errors.New("ACP review requires an available model, tool runtime, and workspace")
	}
	requestText = normalizeReviewRequest(requestText)
	a.state.turnMessageStart = len(a.state.messages)
	titleChanged := a.state.touchSessionMetadata(requestText)
	message := client.Message{Role: client.RoleUser, Content: requestText}
	a.state.archiveMessage(message, sessionstore.StatusSubmitted, false)
	a.state.messages = append(a.state.messages, message)
	if a.state.memory == nil {
		a.state.memory = memoryForPlan(a.state.activeConfig())
	}
	a.state.memory.Append(message)
	a.launchLearning(a.state.observeLearningMessage(message))
	if err := a.state.saveWorkspaceSession(); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.emitSessionInfoContext(ctx, titleChanged); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.emitTaskPlanContext(ctx, requestText, acp.PlanEntryStatusInProgress); err != nil {
		return acp.PromptResponse{}, err
	}

	traceID, err := sessionstore.NewID()
	if err != nil {
		return acp.PromptResponse{}, err
	}
	trace := newACPPlanTrace(a.root, traceID, a.updateContext)
	workflowCtx, cancel := context.WithCancel(ctx)
	events := make(chan agentEvent)
	go streamReviewWorkflow(
		workflowCtx, a.state.client, a.state.toolRuntime, a.state.activeConfig(), a.state.runID, a.state.archive,
		a.state.workspaceStore.Root, requestText, a.state.workspaceStore, events,
	)
	run := &acpPlanContinuation{
		workflowCtx: workflowCtx, cancel: cancel, events: events, trace: trace,
		objective: requestText, workflow: "review",
	}
	return a.continueACPPlan(ctx, run, false)
}

func (a *acpAgent) continueACPPlan(
	ctx context.Context,
	run *acpPlanContinuation,
	persistent bool,
) (response acp.PromptResponse, runErr error) {
	suspended := false
	stopPromptCancel := func() bool { return true }
	if persistent {
		stopPromptCancel = context.AfterFunc(ctx, run.cancel)
	}
	defer func() {
		stopPromptCancel()
		if !suspended {
			if errors.Is(ctx.Err(), context.Canceled) && errors.Is(runErr, context.Canceled) {
				response, runErr = a.finishCancelledPrompt(), nil
			}
			runErr = errors.Join(runErr, run.finish(a, ctx, response, runErr))
		}
	}()

	for event := range run.events {
		if event.plan != nil {
			run.detailed = true
			if err := a.emitAgentPlanContext(ctx, *event.plan); err != nil {
				return acp.PromptResponse{}, err
			}
		}
		if event.activity != nil {
			progress := strings.TrimSpace(strings.Join([]string{acpTraceAgentLabel(event.activity.Agent, event.activity.TaskID), event.activity.Action, event.activity.Detail}, " · "))
			if progress != "" {
				if err := a.updateContext(ctx, acp.UpdateAgentThoughtText(progress+"\n")); err != nil {
					return acp.PromptResponse{}, err
				}
			}
		}
		if event.trace != nil {
			if err := run.trace.handle(ctx, *event.trace); err != nil {
				return acp.PromptResponse{}, err
			}
		}
		if event.question != nil {
			if !persistent {
				event.answer <- a.elicitAnswer(ctx, *event.question)
			} else {
				response, err := a.suspendACPQuestion(
					ctx,
					*event.question,
					event.answer,
					isPlanApprovalQuestion(*event.question),
					true,
					false,
					func(nextCtx context.Context) (acp.PromptResponse, error) {
						return a.continueACPPlan(nextCtx, run, true)
					},
					func() error {
						return run.finish(a, a.state.ctx, acp.PromptResponse{}, context.Canceled)
					},
				)
				if err != nil {
					return acp.PromptResponse{}, err
				}
				suspended = true
				return response, nil
			}
		}
		if event.learningName != "" {
			a.launchLearning(a.state.enqueueLearningSpecial(event.learningName, event.learningPayload))
		}
		if event.err != nil {
			if !run.detailed {
				_ = a.emitTaskPlanContext(a.state.ctx, run.objective, acp.PlanEntryStatusCompleted)
			}
			if errors.Is(event.err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) ||
				errors.Is(run.workflowCtx.Err(), context.Canceled) {
				return a.finishCancelledPrompt(), nil
			}
			a.state.archiveFailure("ACP "+run.workflowName()+" failed", event.err)
			_ = a.state.flushArchive()
			return acp.PromptResponse{}, event.err
		}
		if event.response != nil {
			if !run.detailed {
				if err := a.emitTaskPlanContext(ctx, run.objective, acp.PlanEntryStatusCompleted); err != nil {
					return acp.PromptResponse{}, err
				}
			}
			streamedResponse := ""
			return a.finishPrompt(*event.response, event.requestEstimate, &streamedResponse)
		}
	}

	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(run.workflowCtx.Err(), context.Canceled) {
		if !run.detailed {
			_ = a.emitTaskPlanContext(a.state.ctx, run.objective, acp.PlanEntryStatusCompleted)
		}
		return a.finishCancelledPrompt(), nil
	}
	return acp.PromptResponse{}, fmt.Errorf("%s workflow ended without a response", run.workflowName())
}

func (a *acpAgent) update(update acp.SessionUpdate) error {
	return a.updateContext(a.state.ctx, update)
}

func (a *acpAgent) updateContext(ctx context.Context, update acp.SessionUpdate) error {
	connection, _ := a.connectionState()
	if connection == nil {
		return errors.New("ACP connection is not initialized")
	}
	return connection.SessionUpdate(ctx, acp.SessionNotification{SessionId: a.sessionID, Update: update})
}

func (a *acpAgent) requireSession(sessionID acp.SessionId) error {
	coordinator := a.coordinator()
	if coordinator != a {
		coordinator.stateMu.Lock()
		shuttingDown := coordinator.shuttingDown
		coordinator.stateMu.Unlock()
		if shuttingDown {
			return errors.New("ACP agent is shutting down")
		}
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.shuttingDown {
		return errors.New("ACP agent is shutting down")
	}
	if a.closing || !a.sessionOpen {
		return errors.New("no ACP session is open for this workspace")
	}
	if sessionID != a.sessionID {
		return fmt.Errorf("unknown ACP session %q", sessionID)
	}
	return nil
}

func (a *acpAgent) requireRunning() error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.shuttingDown {
		return errors.New("ACP agent is shutting down")
	}
	return nil
}

func (a *acpAgent) beginActivation() error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.shuttingDown {
		return errors.New("ACP agent is shutting down")
	}
	a.activationWG.Add(1)
	return nil
}

func (a *acpAgent) cancelActiveTurn() {
	a.stateMu.Lock()
	cancel := a.turnCancel
	a.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *acpAgent) shutdown() error {
	a.stateMu.Lock()
	if a.shuttingDown {
		a.stateMu.Unlock()
		return nil
	}
	a.shuttingDown = true
	runtimes := make([]*acpAgent, 0, len(a.sessions))
	for _, runtime := range a.sessions {
		runtimes = append(runtimes, runtime)
	}
	a.sessions = make(map[acp.SessionId]*acpAgent)
	a.stateMu.Unlock()
	for _, runtime := range runtimes {
		runtime.stateMu.Lock()
		runtime.closing = true
		cancel := runtime.turnCancel
		runtime.stateMu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	a.activationWG.Wait()
	var closeErrors []error
	for _, runtime := range runtimes {
		runtime.promptMu.Lock()
		closeErrors = append(closeErrors, runtime.closeRuntime())
		runtime.promptMu.Unlock()
	}
	return errors.Join(closeErrors...)
}

// closeRuntime releases only resources owned by this session runtime. The
// caller must prevent a new prompt from entering (normally by holding
// promptMu or by keeping the runtime unpublished).
func (a *acpAgent) closeRuntime() error {
	a.stateMu.Lock()
	if !a.sessionOpen {
		a.stateMu.Unlock()
		return nil
	}
	cancel := a.turnCancel
	a.closing = true
	a.sessionOpen = false
	a.turnCancel = nil
	a.learningInterrupt = nil
	a.learningPending = false
	a.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.state.stopSessionLearning()
	commitErr := a.closePendingACPCommit()
	questionErr := a.closePendingACPQuestion()
	lock := a.state.workspaceLock
	a.state.workspaceLock = nil
	scope := a.sessionMCPScoped
	a.sessionMCPScoped = nil
	a.sessionMCPConfig = mcpconfig.Config{}
	a.sessionMCPIDs = nil
	a.sessionMCPBase = mcpconfig.Config{}
	a.sessionMCPBaseSet = false
	a.sessionMCPOverride = false
	a.state.releaseConversationState(a.root)
	var lockErr error
	if lock != nil {
		lockErr = lock.Close()
	}
	var scopeErr error
	if scope != nil {
		scopeErr = scope.Close()
	}
	return errors.Join(commitErr, questionErr, scopeErr, lockErr)
}

func (a *acpAgent) launchLearning(command tea.Cmd) {
	if command == nil {
		return
	}
	go func() {
		message := command()
		result, ok := message.(thinkerResultMsg)
		if !ok {
			return
		}

		a.promptMu.Lock()
		next := a.applyPendingLearningLocked()
		matched := result.jobID == a.state.thinkerJobID && result.sessionGeneration == a.state.sessionGeneration
		if matched {
			a.state.thinkerBusy = false
			a.state.thinkerJobID = ""
			if result.result.LogError != "" {
				a.state.archiveFailure("thinker log", errors.New(result.result.LogError))
				_ = a.state.flushArchive()
			}
			if result.err != nil {
				a.state.archiveFailure("learning update failed", result.err)
				_ = a.state.flushArchive()
			} else {
				if a.state.learning != nil {
					if err := a.state.learning.Commit(result.jobID); err != nil {
						a.state.archiveFailure("learning checkpoint failed", err)
					} else {
						if err := a.state.saveWorkspaceSession(); err != nil {
							a.state.archiveFailure("persist learning checkpoint failed", err)
						}
					}
				}
			}
		}
		a.stateMu.Lock()
		open := a.sessionOpen
		a.stateMu.Unlock()
		if next == nil && matched && open {
			next = a.state.startNextLearningSegment()
		}
		a.promptMu.Unlock()
		a.launchLearning(next)
	}()
}

func acpPromptMessage(blocks []acp.ContentBlock, imagesSupported bool) (client.Message, bool, error) {
	message := client.Message{Role: client.RoleUser}
	textParts := make([]string, 0, len(blocks))
	contentParts := make([]client.MessageContentPart, 0, len(blocks))
	hasNonText := false
	appendText := func(value string) {
		textParts = append(textParts, value)
		contentParts = append(contentParts, client.MessageContentPart{"type": "text", "text": value})
	}
	for _, block := range blocks {
		switch {
		case block.Text != nil:
			appendText(block.Text.Text)
		case block.ResourceLink != nil || block.Resource != nil:
			part, text, err := acpPromptResourcePart(block, imagesSupported)
			if err != nil {
				return client.Message{}, false, err
			}
			if text != "" {
				textParts = append(textParts, text)
			}
			contentParts = append(contentParts, part)
			hasNonText = true
		case block.Image != nil:
			if !imagesSupported {
				return client.Message{}, false, errors.New("the active model route does not support ACP image prompts")
			}
			dataURI, err := acpImageDataURI(block.Image.Data, block.Image.MimeType)
			if err != nil {
				return client.Message{}, false, err
			}
			part := client.MessageContentPart{
				"type":      "image_url",
				"image_url": map[string]any{"url": dataURI},
			}
			storeACPContentBlock(part, block)
			contentParts = append(contentParts, part)
			hasNonText = true
		case block.Audio != nil:
			return client.Message{}, false, errors.New("audio prompt content is not supported")
		default:
			return client.Message{}, false, errors.New("unsupported ACP prompt content")
		}
	}
	message.Content = strings.Join(textParts, "\n\n")
	if hasNonText {
		message.ContentParts = contentParts
	}
	return message, hasNonText, nil
}

func acpImageDataURI(data, mimeType string) (string, error) {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if !strings.HasPrefix(mimeType, "image/") {
		return "", fmt.Errorf("invalid ACP image MIME type %q", mimeType)
	}
	const maximumImageBytes = 20 << 20
	if base64.StdEncoding.DecodedLen(len(data)) > maximumImageBytes {
		return "", fmt.Errorf("ACP image exceeds %d bytes", maximumImageBytes)
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", fmt.Errorf("decode ACP image: %w", err)
	}
	if len(decoded) > maximumImageBytes {
		return "", fmt.Errorf("ACP image exceeds %d bytes", maximumImageBytes)
	}
	return "data:" + mimeType + ";base64," + data, nil
}

func replayACPUserMessage(message client.Message) []acp.SessionUpdate {
	if len(message.ContentParts) == 0 {
		if message.Content == "" {
			return nil
		}
		return []acp.SessionUpdate{acp.UpdateUserMessageText(message.Content)}
	}
	updates := make([]acp.SessionUpdate, 0, len(message.ContentParts))
	for _, part := range message.ContentParts {
		if block, ok := storedACPContentBlock(part); ok {
			updates = append(updates, acp.UpdateUserMessage(block))
			continue
		}
		switch part["type"] {
		case "text", "input_text", "output_text":
			if value, ok := part["text"].(string); ok && value != "" {
				updates = append(updates, acp.UpdateUserMessageText(value))
			}
		case "image_url":
			image, ok := part["image_url"].(map[string]any)
			if !ok {
				continue
			}
			dataURI, _ := image["url"].(string)
			prefix, data, found := strings.Cut(dataURI, ",")
			if !found || !strings.HasPrefix(prefix, "data:image/") || !strings.HasSuffix(prefix, ";base64") {
				continue
			}
			mimeType := strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64")
			updates = append(updates, acp.UpdateUserMessage(acp.ImageBlock(data, mimeType)))
		}
	}
	return updates
}

func (m model) supportsACPImages() bool {
	var supports func(string, map[string]bool) bool
	supports = func(modelID string, seen map[string]bool) bool {
		if name, grouped := strings.CutPrefix(modelID, "group/"); grouped {
			if seen[name] {
				return false
			}
			seen[name] = true
			defer delete(seen, name)
			group, found := m.activeConfig().ModelGroups[name]
			if !found || len(group.Candidates) == 0 {
				return false
			}
			for _, candidate := range group.Candidates {
				if !supports(candidate.Model, seen) {
					return false
				}
			}
			return true
		}
		providerIndex, _, found := gatewayModelLocation(m.gatewayConfig, modelID)
		if !found {
			return false
		}
		switch m.gatewayConfig.Providers[providerIndex].Type {
		case "openai-compatible", "openrouter", "xai", "grok":
			return true
		default:
			return false
		}
	}
	return supports(m.activeModel(), make(map[string]bool))
}

func classifyACPTool(name string) acp.ToolKind {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "delete"), strings.Contains(lower, "remove"):
		return acp.ToolKindDelete
	case strings.Contains(lower, "move"), strings.Contains(lower, "rename"):
		return acp.ToolKindMove
	case strings.Contains(lower, "write"), strings.Contains(lower, "edit"), strings.Contains(lower, "patch"), strings.Contains(lower, "create"):
		return acp.ToolKindEdit
	case strings.Contains(lower, "search"), strings.Contains(lower, "find"), strings.Contains(lower, "grep"):
		return acp.ToolKindSearch
	case strings.Contains(lower, "run"), strings.Contains(lower, "exec"), strings.Contains(lower, "command"), strings.Contains(lower, "wait"):
		return acp.ToolKindExecute
	case strings.Contains(lower, "think"), strings.Contains(lower, "plan"):
		return acp.ToolKindThink
	case strings.Contains(lower, "read"), strings.Contains(lower, "list"), strings.Contains(lower, "load"):
		return acp.ToolKindRead
	default:
		return acp.ToolKindOther
	}
}

func toolLocations(root string, call client.ToolCall) []acp.ToolCallLocation {
	var arguments map[string]any
	if json.Unmarshal([]byte(call.Function.Arguments), &arguments) != nil {
		return nil
	}
	var locations []acp.ToolCallLocation
	for _, key := range []string{"path", "file", "source", "destination"} {
		value, ok := arguments[key].(string)
		if !ok {
			continue
		}
		if location, ok := toolLocation(root, value); ok {
			locations = append(locations, location)
		}
	}
	if values, ok := arguments["paths"].([]any); ok {
		for _, raw := range values {
			value, ok := raw.(string)
			if !ok {
				continue
			}
			if location, ok := toolLocation(root, value); ok {
				locations = append(locations, location)
			}
		}
	}
	return locations
}

func toolLocation(root, path string) (acp.ToolCallLocation, bool) {
	if path == "" {
		return acp.ToolCallLocation{}, false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return acp.ToolCallLocation{}, false
	}
	return acp.ToolCallLocation{Path: path}, true
}

func decodeJSONValue(value string) any {
	var decoded any
	if json.Unmarshal([]byte(value), &decoded) == nil {
		return decoded
	}
	return value
}

func canonicalWorkspaceRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root %q: %w", absolute, err)
	}
	canonical = filepath.Clean(canonical)
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect workspace root %q: %w", canonical, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace root %q is not a directory", canonical)
	}
	return canonical, nil
}

func sameWorkspaceRoot(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}
