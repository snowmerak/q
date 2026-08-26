package app

import (
	"context"
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
	"github.com/coder/acp-go-sdk"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workspace"
)

// RunACPDefault serves ACP over stdin/stdout for a single workspace.
func RunACPDefault(ctx context.Context, root string, input io.Reader, output, logOutput io.Writer) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	return RunACP(ctx, store, root, input, output, logOutput)
}

// RunACP serves ACP over the supplied streams. A server process owns exactly one
// workspace projection and exposes at most one active ACP session at a time.
func RunACP(ctx context.Context, store config.Store, root string, input io.Reader, output, logOutput io.Writer) (runErr error) {
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
	connection := acp.NewAgentSideConnection(agent, output, input)
	connection.SetLogger(logger)
	agent.setConnection(connection)
	agent.launchLearning(host.model.startNextLearningSegment())

	select {
	case <-ctx.Done():
	case <-connection.Done():
	}
	agent.cancelActiveTurn()
	agent.promptMu.Lock()
	agent.promptMu.Unlock()
	return nil
}

type acpHost struct {
	model       model
	lifecycle   *startupLifecycle
	manager     *providerhost.Manager
	client      chatClient
	lock        *workspace.Lock
	cancel      context.CancelFunc
	libraryDone <-chan error
	closeOnce   sync.Once
	closeErr    error
}

func openACPHost(parent context.Context, store config.Store, root string) (*acpHost, error) {
	runtimeContext, cancel := context.WithCancel(parent)
	host := &acpHost{cancel: cancel}
	fail := func(err error) (*acpHost, error) {
		return nil, errors.Join(err, host.Close())
	}

	lock, err := workspace.AcquireLock(root, "q acp")
	if err != nil {
		return fail(err)
	}
	host.lock = lock

	libraryDone := make(chan error, 1)
	host.libraryDone = libraryDone
	go func() {
		libraryDone <- qlibrary.Run(runtimeContext, store.Dir, io.Discard)
	}()

	loaded, loadErr := store.Load()
	if loadErr != nil && !errors.Is(loadErr, config.ErrNotFound) {
		return fail(loadErr)
	}
	manager, managerErr := providerhost.NewManager(runtimeContext, providerhost.Store{Dir: store.Dir})
	if managerErr != nil {
		return fail(managerErr)
	}
	host.manager = manager
	lifecycle := newStartupLifecycle()
	host.lifecycle = lifecycle
	initialized := (startupRequest{
		ctx:            runtimeContext,
		loaded:         loaded,
		configErr:      loadErr,
		store:          store,
		manager:        manager,
		workspaceStore: workspace.Store{Root: root},
		workspaceLock:  lock,
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
	host.model.workspaceStore = &workspace.Store{Root: root}
	host.model.workspaceLock = lock
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
		if h.cancel != nil {
			h.cancel()
		}
		var closeErrors []error
		if h.libraryDone != nil {
			closeErrors = append(closeErrors, <-h.libraryDone)
		}
		if h.lifecycle != nil {
			h.lifecycle.waitIfStarted()
		}

		if h.client != nil {
			closeErrors = append(closeErrors, h.client.Close())
		}
		if h.lifecycle != nil {
			closeErrors = append(closeErrors, h.lifecycle.closeResources())
		}
		if h.manager != nil {
			closeErrors = append(closeErrors, h.manager.Close())
		}
		if h.lock != nil {
			closeErrors = append(closeErrors, h.lock.Close())
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
	state  *model
	root   string
	logger *slog.Logger

	stateMu            sync.Mutex
	promptMu           sync.Mutex
	connection         acpSessionConnection
	clientCapabilities acp.ClientCapabilities
	sessionID          acp.SessionId
	sessionOpen        bool
	turnCancel         context.CancelFunc
}

var (
	_ acp.Agent       = (*acpAgent)(nil)
	_ acp.AgentLoader = (*acpAgent)(nil)
)

func newACPAgent(state *model, root string, logger *slog.Logger) *acpAgent {
	state.ensureRunID()
	return &acpAgent{
		state:     state,
		root:      root,
		logger:    logger,
		sessionID: acpSessionID(state.runID),
	}
}

func acpSessionID(runID string) acp.SessionId {
	return acp.SessionId("q-" + strings.TrimPrefix(runID, "run-"))
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
				Image: a.state.supportsACPImages(),
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
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	if err := a.validateSessionWorkspace(request.Cwd, request.AdditionalDirectories); err != nil {
		return acp.NewSessionResponse{}, err
	}
	if err := a.activateSession(ctx, a.sessionID, request.McpServers); err != nil {
		return acp.NewSessionResponse{}, err
	}
	return acp.NewSessionResponse{SessionId: a.sessionID}, nil
}

func (a *acpAgent) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	a.stateMu.Lock()
	if !a.sessionOpen {
		a.stateMu.Unlock()
		return acp.CloseSessionResponse{}, errors.New("no ACP session is open for this workspace")
	}
	if request.SessionId != a.sessionID {
		a.stateMu.Unlock()
		return acp.CloseSessionResponse{}, fmt.Errorf("unknown ACP session %q", request.SessionId)
	}
	cancel := a.turnCancel
	a.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	a.stateMu.Lock()
	if !a.sessionOpen || request.SessionId != a.sessionID {
		a.stateMu.Unlock()
		return acp.CloseSessionResponse{}, errors.New("ACP session closed while waiting for its active turn")
	}
	a.sessionOpen = false
	a.stateMu.Unlock()
	if err := a.restoreWorkspaceMCP(ctx); err != nil {
		return acp.CloseSessionResponse{}, err
	}
	return acp.CloseSessionResponse{}, nil
}

func (a *acpAgent) Cancel(_ context.Context, notification acp.CancelNotification) error {
	if err := a.requireSession(notification.SessionId); err != nil {
		return err
	}
	a.cancelActiveTurn()
	return nil
}

func (a *acpAgent) ListSessions(_ context.Context, request acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	response := acp.ListSessionsResponse{Sessions: []acp.SessionInfo{}}
	if a.state.workspaceStore == nil {
		return response, errors.New("workspace session storage is unavailable")
	}
	if request.Cursor != nil && *request.Cursor != "" {
		return response, errors.New("this single-workspace agent does not use session list cursors")
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
	session, err := a.state.workspaceStore.Load()
	if errors.Is(err, workspace.ErrNotFound) {
		return response, nil
	}
	if err != nil {
		return response, err
	}
	runID := session.RunID
	if runID == "" {
		runID = a.state.runID
	}
	info := acp.SessionInfo{SessionId: acpSessionID(runID), Cwd: a.root}
	if title := strings.TrimSpace(session.Title); title != "" {
		info.Title = acp.Ptr(title)
	} else if title := sessionTitleFromMessages(session.Transcript); title != "" {
		info.Title = acp.Ptr(title)
	}
	updatedAt := time.Time{}
	if session.UpdatedAt != nil {
		updatedAt = *session.UpdatedAt
	}
	if updatedAt.IsZero() {
		if fileInfo, statErr := os.Stat(a.state.workspaceStore.Path()); statErr == nil {
			updatedAt = fileInfo.ModTime().UTC()
		}
	}
	if !updatedAt.IsZero() {
		formatted := updatedAt.UTC().Format(time.RFC3339Nano)
		info.UpdatedAt = &formatted
	}
	response.Sessions = append(response.Sessions, info)
	return response, nil
}

// UnstableDeleteSession implements the SDK's current name for ACP session/delete.
func (a *acpAgent) UnstableDeleteSession(
	ctx context.Context,
	request acp.UnstableDeleteSessionRequest,
) (acp.UnstableDeleteSessionResponse, error) {
	a.stateMu.Lock()
	known := request.SessionId == a.sessionID
	cancel := a.turnCancel
	a.stateMu.Unlock()
	if !known {
		return acp.UnstableDeleteSessionResponse{}, nil
	}
	if cancel != nil {
		cancel()
	}
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	a.stateMu.Lock()
	if request.SessionId != a.sessionID {
		a.stateMu.Unlock()
		return acp.UnstableDeleteSessionResponse{}, nil
	}
	a.stateMu.Unlock()
	a.state.resetConversation()
	a.stateMu.Lock()
	a.sessionID = acpSessionID(a.state.runID)
	a.sessionOpen = false
	a.turnCancel = nil
	a.stateMu.Unlock()
	if err := a.restoreWorkspaceMCP(ctx); err != nil {
		return acp.UnstableDeleteSessionResponse{}, err
	}
	return acp.UnstableDeleteSessionResponse{}, nil
}

func (a *acpAgent) ResumeSession(ctx context.Context, request acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	if err := a.validateSessionWorkspace(request.Cwd, request.AdditionalDirectories); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	if err := a.activateSession(ctx, request.SessionId, request.McpServers); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	return acp.ResumeSessionResponse{}, nil
}

func (a *acpAgent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	if err := a.validateSessionWorkspace(request.Cwd, request.AdditionalDirectories); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if err := a.activateSession(ctx, request.SessionId, request.McpServers); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if err := a.replayWorkspaceSession(ctx); err != nil {
		a.deactivateSession()
		_ = a.restoreWorkspaceMCP(ctx)
		return acp.LoadSessionResponse{}, err
	}
	return acp.LoadSessionResponse{}, nil
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

func (a *acpAgent) activateSession(ctx context.Context, sessionID acp.SessionId, mcpServers []acp.McpServer) error {
	a.stateMu.Lock()
	if sessionID != a.sessionID {
		a.stateMu.Unlock()
		return fmt.Errorf("unknown ACP session %q", sessionID)
	}
	if a.sessionOpen {
		a.stateMu.Unlock()
		return errors.New("this workspace already has an active ACP session")
	}
	a.sessionOpen = true
	a.stateMu.Unlock()
	if err := a.configureSessionMCP(ctx, mcpServers); err != nil {
		a.stateMu.Lock()
		a.sessionOpen = false
		a.stateMu.Unlock()
		return err
	}
	return nil
}

func (a *acpAgent) configureSessionMCP(ctx context.Context, servers []acp.McpServer) error {
	if len(servers) == 0 {
		return nil
	}
	configurer, ok := a.state.toolRuntime.(externalMCPConfigurer)
	if !ok || configurer == nil {
		return errors.New("ACP-provided MCP servers require q's external MCP runtime")
	}
	base, err := a.state.mcpSettingsStore.LoadOrDefault()
	if err != nil {
		return err
	}
	merged, sessionIDs, err := mergeACPMCPServers(base, servers)
	if err != nil {
		return err
	}
	statuses := configurer.ConfigureExternal(ctx, a.root, merged)
	for _, status := range statuses {
		if _, supplied := sessionIDs[status.ID]; supplied && status.Error != "" {
			_ = a.restoreWorkspaceMCP(ctx)
			return fmt.Errorf("connect ACP MCP server %s: %s", status.ID, status.Error)
		}
	}
	return nil
}

func (a *acpAgent) restoreWorkspaceMCP(ctx context.Context) error {
	configurer, ok := a.state.toolRuntime.(externalMCPConfigurer)
	if !ok || configurer == nil {
		return nil
	}
	value, err := a.state.mcpSettingsStore.LoadOrDefault()
	if err != nil {
		return err
	}
	statuses := configurer.ConfigureExternal(ctx, a.root, value)
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
	a.stateMu.Lock()
	a.sessionOpen = false
	a.stateMu.Unlock()
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
	if err := a.emitAvailableCommandsContext(ctx); err != nil {
		return err
	}
	return nil
}

func (a *acpAgent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetMode)
}

func (a *acpAgent) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetConfigOption)
}

func (a *acpAgent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	if err := a.requireSession(request.SessionId); err != nil {
		return acp.PromptResponse{}, err
	}
	userMessage, hasNonText, err := acpPromptMessage(request.Prompt, a.state.supportsACPImages())
	if err != nil {
		return acp.PromptResponse{}, err
	}
	text := userMessage.TextContent()
	if strings.TrimSpace(text) == "" && !hasNonText {
		return acp.PromptResponse{}, errors.New("prompt contains no supported content")
	}

	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	if err := a.requireSession(request.SessionId); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.emitAvailableCommandsContext(ctx); err != nil {
		return acp.PromptResponse{}, err
	}

	turnContext, cancel := context.WithCancel(ctx)
	a.stateMu.Lock()
	a.turnCancel = cancel
	a.stateMu.Unlock()
	defer func() {
		cancel()
		a.stateMu.Lock()
		a.turnCancel = nil
		a.stateMu.Unlock()
	}()
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

	events := make(chan agentEvent)
	go streamAgentLoop(
		ctx,
		a.state.client,
		scopeTools(a.state.toolRuntime, mcpconfig.RoleDefault),
		a.state.activeModel(),
		a.state.memory.Messages(),
		a.state.conversationID,
		a.state.activeTask,
		a.state.streamsActiveChat(),
		modelNeedsSystemInstructionCoalescing(
			a.state.gatewayConfig, a.state.activeConfig().ModelGroups, a.state.activeModel(), nil,
		),
		events,
	)

	streamedResponse := ""
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
				streamedResponse = ""
			}
			if event.streamDelta.Kind == chatStreamThinking {
				if err := a.update(acp.UpdateAgentThoughtText(event.streamDelta.Content)); err != nil {
					return acp.PromptResponse{}, err
				}
			} else {
				streamedResponse += event.streamDelta.Content
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
			event.answer <- a.elicitAnswer(ctx, *event.question)
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
				streamedResponse = ""
			} else if message.Role == client.RoleAssistant {
				a.state.archiveMessage(message, sessionstore.StatusSucceeded, false)
				if err := a.emitMissingAssistantText(message.Content, &streamedResponse); err != nil {
					return acp.PromptResponse{}, err
				}
			}
			if err := a.state.saveWorkspaceSession(); err != nil {
				return acp.PromptResponse{}, err
			}
		}
		if event.err != nil {
			if errors.Is(event.err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return a.finishCancelledPrompt(), nil
			}
			a.state.archiveFailure("ACP agent turn failed", event.err)
			_ = a.state.flushArchive()
			return acp.PromptResponse{}, event.err
		}
		if event.response != nil {
			return a.finishPrompt(*event.response, event.requestEstimate, &streamedResponse)
		}
	}

	if errors.Is(ctx.Err(), context.Canceled) {
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
	a.stateMu.Lock()
	connection := a.connection
	sessionID := a.sessionID
	supportsForm := a.clientCapabilities.Elicitation != nil && a.clientCapabilities.Elicitation.Form != nil
	a.stateMu.Unlock()
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

func (a *acpAgent) emitAvailableCommandsContext(ctx context.Context) error {
	commands := []acp.AvailableCommand{
		{
			Name: "plan", Description: "Plan an implementation, request approval, then execute and review it.",
			Input: &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: "work to plan"}},
		},
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
	var output string
	switch {
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
	case command == "/help":
		output = "Available ACP commands:\n- /plan <work to plan>\n- /learn [on|off|status]\n- /clear\n- /help"
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

func (a *acpAgent) clearACPConversation(ctx context.Context) error {
	runID := a.state.runID
	a.state.resetConversation()
	// An ACP slash command cannot replace the active client-side session ID.
	// Keep its backing run stable while clearing only q's conversation projection.
	a.state.runID = runID
	a.state.sessionUpdatedAt = time.Now().UTC()
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
	a.stateMu.Lock()
	supportsForm := a.clientCapabilities.Elicitation != nil && a.clientCapabilities.Elicitation.Form != nil
	a.stateMu.Unlock()
	if !supportsForm {
		message := "The ACP client must support form elicitation to answer /plan questions and approve execution."
		if err := a.updateContext(ctx, acp.UpdateAgentMessageText(message)); err != nil {
			return acp.PromptResponse{}, err
		}
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}

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
	events := make(chan agentEvent)
	go streamPlanWorkflow(
		ctx, a.state.client, a.state.toolRuntime, a.state.activeConfig(), a.state.runID, a.state.archive,
		workingDirectory, objective, planContext(a.state.memory.Messages()), executionStore, events,
	)

	for event := range events {
		if event.activity != nil {
			progress := strings.TrimSpace(strings.Join([]string{event.activity.Agent, event.activity.Action, event.activity.Detail}, " · "))
			if progress != "" {
				if err := a.updateContext(ctx, acp.UpdateAgentThoughtText(progress+"\n")); err != nil {
					return acp.PromptResponse{}, err
				}
			}
		}
		if event.question != nil {
			event.answer <- a.elicitAnswer(ctx, *event.question)
		}
		if event.learningName != "" {
			a.launchLearning(a.state.enqueueLearningSpecial(event.learningName, event.learningPayload))
		}
		if event.err != nil {
			_ = a.emitTaskPlanContext(a.state.ctx, objective, acp.PlanEntryStatusCompleted)
			if errors.Is(event.err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return a.finishCancelledPrompt(), nil
			}
			a.state.archiveFailure("ACP plan failed", event.err)
			_ = a.state.flushArchive()
			return acp.PromptResponse{}, event.err
		}
		if event.response != nil {
			if err := a.emitTaskPlanContext(ctx, objective, acp.PlanEntryStatusCompleted); err != nil {
				return acp.PromptResponse{}, err
			}
			streamedResponse := ""
			return a.finishPrompt(*event.response, event.requestEstimate, &streamedResponse)
		}
	}

	if errors.Is(ctx.Err(), context.Canceled) {
		_ = a.emitTaskPlanContext(a.state.ctx, objective, acp.PlanEntryStatusCompleted)
		return a.finishCancelledPrompt(), nil
	}
	return acp.PromptResponse{}, errors.New("plan workflow ended without a response")
}

func (a *acpAgent) update(update acp.SessionUpdate) error {
	return a.updateContext(a.state.ctx, update)
}

func (a *acpAgent) updateContext(ctx context.Context, update acp.SessionUpdate) error {
	a.stateMu.Lock()
	connection := a.connection
	a.stateMu.Unlock()
	if connection == nil {
		return errors.New("ACP connection is not initialized")
	}
	return connection.SessionUpdate(ctx, acp.SessionNotification{SessionId: a.sessionID, Update: update})
}

func (a *acpAgent) requireSession(sessionID acp.SessionId) error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if !a.sessionOpen {
		return errors.New("no ACP session is open for this workspace")
	}
	if sessionID != a.sessionID {
		return fmt.Errorf("unknown ACP session %q", sessionID)
	}
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
		if result.jobID == a.state.thinkerJobID {
			a.state.thinkerBusy = false
			a.state.thinkerJobID = ""
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
		next := a.state.startNextLearningSegment()
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
		case block.ResourceLink != nil:
			appendText(fmt.Sprintf("Resource: %s (%s)", block.ResourceLink.Name, block.ResourceLink.Uri))
		case block.Resource != nil && block.Resource.Resource.TextResourceContents != nil:
			appendText(block.Resource.Resource.TextResourceContents.Text)
		case block.Image != nil:
			if !imagesSupported {
				return client.Message{}, false, errors.New("the active model route does not support ACP image prompts")
			}
			dataURI, err := acpImageDataURI(block.Image.Data, block.Image.MimeType)
			if err != nil {
				return client.Message{}, false, err
			}
			contentParts = append(contentParts, client.MessageContentPart{
				"type":      "image_url",
				"image_url": map[string]any{"url": dataURI},
			})
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
