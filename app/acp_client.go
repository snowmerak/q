package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

type acpAgentCommand struct {
	name       string
	args       []string
	env        map[string]string
	display    string
	installTip string
}

type acpRemoteConnection interface {
	NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error)
	LoadSession(context.Context, acp.LoadSessionRequest) (acp.LoadSessionResponse, error)
	ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error)
	Prompt(context.Context, acp.PromptRequest) (acp.PromptResponse, error)
	CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error)
	UnstableDeleteSession(context.Context, acp.UnstableDeleteSessionRequest) (acp.UnstableDeleteSessionResponse, error)
}

type acpPermissionMode uint8

const (
	acpPermissionInteractive acpPermissionMode = iota
	acpPermissionReadOnly
)

type acpRemoteClient struct {
	mu           sync.Mutex
	connection   acpRemoteConnection
	sessionID    acp.SessionId
	capabilities acp.AgentCapabilities
	root         string
	agent        string
	display      string
	command      string
	title        string
	turn         *acpRemoteTurn
	permissions  acpPermissionMode

	slashCommandsBySession map[acp.SessionId][]slashCommand

	stdin       io.WriteCloser
	cancel      context.CancelFunc
	processDone <-chan error
	stderr      *synchronizedTailBuffer
	closeOnce   sync.Once
	closeErr    error
}

type acpRemoteTurn struct {
	ctx             context.Context
	events          chan<- agentEvent
	display         string
	mu              sync.Mutex
	response        strings.Builder
	contentParts    []client.MessageContentPart
	structured      bool
	responseStarted bool
	thoughtStarted  bool
	toolTitles      map[acp.ToolCallId]string
	toolCalls       map[acp.ToolCallId]struct{}
}

type acpSessionResetMsg struct {
	sessionID acp.SessionId
	err       error
}

var _ chatClient = (*acpRemoteClient)(nil)
var _ acp.Client = (*acpRemoteClient)(nil)

func resolveACPAgentCommand(agent string, lookPath func(string) (string, error)) (acpAgentCommand, error) {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "codex":
		if name, err := lookPath("codex-acp"); err == nil {
			return acpAgentCommand{name: name, display: "Codex"}, nil
		}
		name, err := lookPath("npx")
		if err != nil {
			return acpAgentCommand{}, errors.New("Codex ACP requires codex-acp or npx; install @agentclientprotocol/codex-acp")
		}
		return acpAgentCommand{
			name: name, args: []string{"-y", "@agentclientprotocol/codex-acp"}, display: "Codex",
			installTip: "npm install -g @agentclientprotocol/codex-acp",
		}, nil
	case "grok":
		name, err := lookPath("grok")
		if err != nil {
			return acpAgentCommand{}, errors.New("Grok ACP requires the Grok Build CLI; install it from https://x.ai/cli")
		}
		return acpAgentCommand{name: name, args: []string{"agent", "stdio"}, display: "Grok"}, nil
	default:
		return acpAgentCommand{}, fmt.Errorf("unsupported ACP agent %q; use codex or grok", agent)
	}
}

func resolveConfiguredACPAgentCommand(connection config.AgentConnectionConfig, lookPath func(string) (string, error)) (acpAgentCommand, error) {
	if connection.Preset != "" {
		command, err := resolveACPAgentCommand(connection.Preset, lookPath)
		if err != nil {
			return acpAgentCommand{}, err
		}
		command.env = cloneStringMap(connection.Env)
		command.args = append(command.args, connection.Args...)
		return command, nil
	}
	name, err := lookPath(connection.Command)
	if err != nil {
		return acpAgentCommand{}, fmt.Errorf("locate ACP agent command %q: %w", connection.Command, err)
	}
	return acpAgentCommand{
		name: name, args: append([]string(nil), connection.Args...), env: cloneStringMap(connection.Env),
		display: filepath.Base(connection.Command),
	}, nil
}

func startACPRemoteClient(
	ctx context.Context,
	command acpAgentCommand,
	root, sessionID, authMethod string,
	logOutput io.Writer,
) (*acpRemoteClient, error) {
	_ = logOutput // Keep agent diagnostics out of Bubble Tea's alternate screen.
	processContext, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(processContext, command.name, command.args...)
	cmd.Dir = root
	cmd.Env = mergedProcessEnv(os.Environ(), command.env)
	stderr := newSynchronizedTailBuffer(64 << 10)
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open ACP agent stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open ACP agent stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		tip := ""
		if command.installTip != "" {
			tip = "; try " + command.installTip
		}
		return nil, fmt.Errorf("start %s ACP agent: %w%s", command.display, err, tip)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	remote := &acpRemoteClient{
		root: root, agent: strings.ToLower(command.display), display: command.display,
		command: strings.Join(append([]string{filepath.Base(command.name)}, command.args...), " "),
		stdin:   stdin, cancel: cancel, processDone: wait, stderr: stderr,
	}
	connection := acp.NewClientSideConnection(remote, stdin, stdout)
	connection.SetLogger(slog.New(slog.NewTextHandler(stderr, nil)))
	remote.connection = connection

	initialize, err := connection.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientInfo: &acp.Implementation{
			Name: "q", Title: acp.Ptr("q ACP Client"), Version: "dev",
		},
		ClientCapabilities: acp.ClientCapabilities{
			Fs: acp.FileSystemCapabilities{}, Terminal: false,
			PlanCapabilities: &acp.PlanCapabilities{},
		},
	})
	if err != nil {
		_ = remote.Close()
		return nil, remote.decorateError("initialize ACP agent", err)
	}
	if initialize.ProtocolVersion != acp.ProtocolVersionNumber {
		_ = remote.Close()
		return nil, fmt.Errorf("ACP agent selected unsupported protocol version %v", initialize.ProtocolVersion)
	}
	remote.capabilities = initialize.AgentCapabilities
	if initialize.AgentInfo != nil {
		if initialize.AgentInfo.Title != nil && strings.TrimSpace(*initialize.AgentInfo.Title) != "" {
			remote.display = strings.TrimSpace(*initialize.AgentInfo.Title)
		} else if strings.TrimSpace(initialize.AgentInfo.Name) != "" {
			remote.display = strings.TrimSpace(initialize.AgentInfo.Name)
		}
	}
	if authMethod != "" {
		if !containsACPAuthMethod(initialize.AuthMethods, authMethod) {
			_ = remote.Close()
			return nil, fmt.Errorf("ACP auth method %q is not offered; available: %s", authMethod, formatACPAuthMethods(initialize.AuthMethods))
		}
		if _, err := connection.Authenticate(ctx, acp.AuthenticateRequest{MethodId: authMethod}); err != nil {
			_ = remote.Close()
			return nil, remote.decorateError("authenticate ACP agent", err)
		}
	}

	if sessionID == "" {
		_, err = remote.newSession(ctx)
	} else {
		remote.sessionID = acp.SessionId(sessionID)
		switch {
		case initialize.AgentCapabilities.SessionCapabilities.Resume != nil:
			_, err = connection.ResumeSession(ctx, acp.ResumeSessionRequest{
				SessionId: remote.sessionID, Cwd: root, McpServers: []acp.McpServer{},
			})
		case initialize.AgentCapabilities.LoadSession:
			_, err = connection.LoadSession(ctx, acp.LoadSessionRequest{
				SessionId: remote.sessionID, Cwd: root, McpServers: []acp.McpServer{},
			})
		default:
			err = errors.New("agent does not support session/load or session/resume")
		}
	}
	if err != nil {
		available := formatACPAuthMethods(initialize.AuthMethods)
		_ = remote.Close()
		if requestError, ok := err.(*acp.RequestError); ok && requestError.Code == -32000 && available != "none" {
			return nil, fmt.Errorf("ACP agent requires authentication; configure auth_method for this connection (available: %s)", available)
		}
		return nil, remote.decorateError("open ACP session", err)
	}
	return remote, nil
}

func (r *acpRemoteClient) Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error) {
	return nil, errors.New("ACP remote chat must run through q's event stream")
}

func (r *acpRemoteClient) ListModels(context.Context) ([]client.Model, error) {
	return []client.Model{{ID: "acp/" + r.agent, OwnedBy: r.display}}, nil
}

func (r *acpRemoteClient) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		connection := r.connection
		sessionID := r.sessionID
		canClose := r.capabilities.SessionCapabilities.Close != nil
		r.mu.Unlock()
		if connection != nil && sessionID != "" && canClose {
			closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
			_, r.closeErr = connection.CloseSession(closeContext, acp.CloseSessionRequest{SessionId: sessionID})
			cancel()
		}
		if r.cancel != nil {
			r.cancel()
		}
		if r.stdin != nil {
			_ = r.stdin.Close()
		}
		if r.processDone != nil {
			select {
			case <-r.processDone:
			case <-time.After(2 * time.Second):
			}
		}
	})
	return r.closeErr
}

func (r *acpRemoteClient) runPrompt(ctx context.Context, content string, events chan<- agentEvent) {
	defer close(events)
	result, toolCalls, err := r.prompt(ctx, content, events)
	if err != nil {
		emitAgentEvent(ctx, events, agentEvent{err: err})
		return
	}
	emitAgentEvent(ctx, events, agentEvent{response: result, toolCalls: toolCalls})
}

func (r *acpRemoteClient) prompt(ctx context.Context, content string, events chan<- agentEvent) (*client.ChatResponse, int, error) {
	turn := &acpRemoteTurn{
		ctx: ctx, events: events, display: r.display,
		toolTitles: make(map[acp.ToolCallId]string), toolCalls: make(map[acp.ToolCallId]struct{}),
	}
	r.mu.Lock()
	if r.turn != nil {
		r.mu.Unlock()
		return nil, 0, errors.New("another ACP turn is already active")
	}
	r.turn = turn
	connection := r.connection
	sessionID := r.sessionID
	root := r.root
	embeddedContext := r.capabilities.PromptCapabilities.EmbeddedContext
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.turn == turn {
			r.turn = nil
		}
		r.mu.Unlock()
	}()

	promptBlocks, err := acpPromptBlocksForText(root, content, embeddedContext)
	if err != nil {
		return nil, 0, err
	}
	response, err := connection.Prompt(ctx, acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    promptBlocks,
	})
	if err != nil {
		return nil, 0, r.decorateError("ACP prompt", err)
	}
	message, toolCalls := turn.result()
	if strings.TrimSpace(message.TextContent()) == "" {
		message.Content = "Agent turn completed without a textual response."
		if response.StopReason != "" && response.StopReason != acp.StopReasonEndTurn {
			message.Content = "Agent stopped: " + string(response.StopReason)
		}
	}
	result := &client.ChatResponse{Choices: []client.Choice{{Message: message}}}
	return result, toolCalls, nil
}

func (r *acpRemoteClient) promptText(ctx context.Context, content string) (string, error) {
	response, _, err := r.prompt(ctx, content, nil)
	if err != nil {
		return "", err
	}
	if response == nil || len(response.Choices) == 0 {
		return "", errors.New("ACP agent returned no response")
	}
	return response.Choices[0].Message.TextContent(), nil
}

func (r *acpRemoteClient) resetSession(ctx context.Context) (acp.SessionId, error) {
	r.mu.Lock()
	if r.turn != nil {
		r.mu.Unlock()
		return "", errors.New("cannot reset an ACP session while a turn is active")
	}
	oldSessionID := r.sessionID
	connection := r.connection
	canClose := r.capabilities.SessionCapabilities.Close != nil
	r.mu.Unlock()
	if canClose && oldSessionID != "" {
		if _, err := connection.CloseSession(ctx, acp.CloseSessionRequest{SessionId: oldSessionID}); err != nil {
			return "", err
		}
		r.mu.Lock()
		if r.sessionID == oldSessionID {
			r.sessionID = ""
			r.title = ""
		}
		r.mu.Unlock()
	}
	return r.newSession(ctx)
}

func (r *acpRemoteClient) newSession(ctx context.Context) (acp.SessionId, error) {
	response, err := r.connection.NewSession(ctx, acp.NewSessionRequest{
		Cwd: r.root, McpServers: []acp.McpServer{},
	})
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	r.sessionID = response.SessionId
	r.title = ""
	for sessionID := range r.slashCommandsBySession {
		if sessionID != response.SessionId {
			delete(r.slashCommandsBySession, sessionID)
		}
	}
	r.mu.Unlock()
	return response.SessionId, nil
}

func (r *acpRemoteClient) resetSessionCommand(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		sessionID, err := r.resetSession(ctx)
		return acpSessionResetMsg{sessionID: sessionID, err: err}
	}
}

func (r *acpRemoteClient) SessionUpdate(ctx context.Context, notification acp.SessionNotification) error {
	r.mu.Lock()
	if update := notification.Update.AvailableCommandsUpdate; update != nil {
		if r.slashCommandsBySession == nil {
			r.slashCommandsBySession = make(map[acp.SessionId][]slashCommand)
		}
		r.slashCommandsBySession[notification.SessionId] = acpSlashCommands(update.AvailableCommands)
	}
	if notification.Update.SessionInfoUpdate != nil && notification.Update.SessionInfoUpdate.Title != nil {
		r.title = strings.TrimSpace(*notification.Update.SessionInfoUpdate.Title)
	}
	turn := r.turn
	sessionID := r.sessionID
	r.mu.Unlock()
	if notification.SessionId != sessionID || turn == nil {
		return nil
	}
	return turn.update(ctx, notification.Update)
}

func (r *acpRemoteClient) RequestPermission(ctx context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	r.mu.Lock()
	turn := r.turn
	permissions := r.permissions
	r.mu.Unlock()
	if turn == nil {
		return cancelledPermission(), nil
	}
	if permissions == acpPermissionReadOnly {
		return readOnlyPermission(request), nil
	}
	title := "Allow this tool call?"
	if request.ToolCall.Title != nil && strings.TrimSpace(*request.ToolCall.Title) != "" {
		title = strings.TrimSpace(*request.ToolCall.Title)
	}
	question := askToUserInput{Question: title, ChoiceOnly: true}
	if request.ToolCall.RawInput != nil {
		if body, err := jsonMarshalIndent(request.ToolCall.RawInput); err == nil {
			question.Context = body
		}
	}
	for _, option := range request.Options {
		question.Choices = append(question.Choices, askToUserChoice{
			ID: string(option.OptionId), Label: option.Name, Description: string(option.Kind),
		})
	}
	answer := make(chan askToUserOutput, 1)
	if !turn.emit(agentEvent{question: &question, answer: answer}) {
		return cancelledPermission(), nil
	}
	select {
	case selected := <-answer:
		for _, option := range request.Options {
			if selected.SelectedChoiceID == string(option.OptionId) {
				return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
					Selected: &acp.RequestPermissionOutcomeSelected{OptionId: option.OptionId},
				}}, nil
			}
		}
		return cancelledPermission(), nil
	case <-ctx.Done():
		return cancelledPermission(), nil
	case <-turn.ctx.Done():
		return cancelledPermission(), nil
	}
}

func readOnlyPermission(request acp.RequestPermissionRequest) acp.RequestPermissionResponse {
	if request.ToolCall.Kind == nil {
		return cancelledPermission()
	}
	switch *request.ToolCall.Kind {
	case acp.ToolKindRead, acp.ToolKindSearch, acp.ToolKindFetch, acp.ToolKindThink:
		for _, option := range request.Options {
			if option.Kind == acp.PermissionOptionKindAllowOnce {
				return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
					Selected: &acp.RequestPermissionOutcomeSelected{OptionId: option.OptionId},
				}}
			}
		}
	}
	return cancelledPermission()
}

func cancelledPermission() acp.RequestPermissionResponse {
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
		Cancelled: &acp.RequestPermissionOutcomeCancelled{},
	}}
}

func (r *acpRemoteClient) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errors.New("q does not advertise ACP client filesystem access")
}

func (r *acpRemoteClient) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errors.New("q does not advertise ACP client filesystem access")
}

func (r *acpRemoteClient) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errors.New("q does not advertise ACP client terminal access")
}

func (r *acpRemoteClient) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errors.New("q does not advertise ACP client terminal access")
}

func (r *acpRemoteClient) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errors.New("q does not advertise ACP client terminal access")
}

func (r *acpRemoteClient) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errors.New("q does not advertise ACP client terminal access")
}

func (r *acpRemoteClient) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errors.New("q does not advertise ACP client terminal access")
}

func (t *acpRemoteTurn) update(ctx context.Context, update acp.SessionUpdate) error {
	switch {
	case update.AgentMessageChunk != nil:
		part, content, structured, err := acpDisplayContentPart(update.AgentMessageChunk.Content)
		if err != nil {
			return err
		}
		if content == "" {
			return nil
		}
		t.mu.Lock()
		start := !t.responseStarted
		t.responseStarted = true
		t.response.WriteString(content)
		t.contentParts = append(t.contentParts, part)
		t.structured = t.structured || structured
		t.mu.Unlock()
		if !t.emit(agentEvent{streamDelta: &chatStreamDelta{Kind: chatStreamResponse, Start: start, Content: content}}) {
			return ctx.Err()
		}
	case update.AgentThoughtChunk != nil:
		content := acpContentText(update.AgentThoughtChunk.Content)
		if content == "" {
			return nil
		}
		t.mu.Lock()
		start := !t.thoughtStarted
		t.thoughtStarted = true
		t.mu.Unlock()
		if !t.emit(agentEvent{streamDelta: &chatStreamDelta{Kind: chatStreamThinking, Start: start, Content: content}}) {
			return ctx.Err()
		}
	case update.ToolCall != nil:
		call := update.ToolCall
		t.mu.Lock()
		t.toolTitles[call.ToolCallId] = call.Title
		t.toolCalls[call.ToolCallId] = struct{}{}
		t.mu.Unlock()
		if !t.emit(agentEvent{activity: &agentActivity{
			Agent: t.display, TaskID: string(call.ToolCallId), Action: acpActivityAction(call.Status), Detail: call.Title,
		}}) {
			return ctx.Err()
		}
	case update.ToolCallUpdate != nil:
		call := update.ToolCallUpdate
		t.mu.Lock()
		title := t.toolTitles[call.ToolCallId]
		if call.Title != nil && strings.TrimSpace(*call.Title) != "" {
			title = strings.TrimSpace(*call.Title)
			t.toolTitles[call.ToolCallId] = title
		}
		t.toolCalls[call.ToolCallId] = struct{}{}
		t.mu.Unlock()
		status := acp.ToolCallStatusInProgress
		if call.Status != nil {
			status = *call.Status
		}
		if !t.emit(agentEvent{activity: &agentActivity{
			Agent: t.display, TaskID: string(call.ToolCallId), Action: acpActivityAction(status), Detail: title,
		}}) {
			return ctx.Err()
		}
	case update.Plan != nil:
		for index, entry := range update.Plan.Entries {
			if !t.emit(agentEvent{activity: &agentActivity{
				Agent: "plan", TaskID: fmt.Sprintf("%d", index+1), Action: string(entry.Status), Detail: entry.Content,
			}}) {
				return ctx.Err()
			}
		}
	}
	return nil
}

func (t *acpRemoteTurn) emit(event agentEvent) bool {
	if t.events == nil {
		return true
	}
	return emitAgentEvent(t.ctx, t.events, event)
}

func (t *acpRemoteTurn) result() (client.Message, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	message := client.Message{Role: client.RoleAssistant, Content: t.response.String()}
	if t.structured {
		message.ContentParts = append([]client.MessageContentPart(nil), t.contentParts...)
	}
	return message, len(t.toolCalls)
}

func acpContentText(block acp.ContentBlock) string {
	_, text, _, err := acpDisplayContentPart(block)
	if err != nil {
		return "\n[invalid ACP content: " + err.Error() + "]\n"
	}
	return text
}

func acpActivityAction(status acp.ToolCallStatus) string {
	switch status {
	case acp.ToolCallStatusCompleted:
		return "completed"
	case acp.ToolCallStatusFailed:
		return "failed"
	case acp.ToolCallStatusPending:
		return "pending"
	default:
		return "running"
	}
}

func (r *acpRemoteClient) decorateError(action string, err error) error {
	if err == nil {
		return nil
	}
	detail := ""
	if r.stderr != nil {
		detail = strings.TrimSpace(r.stderr.String())
	}
	if detail == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w\nagent stderr:\n%s", action, err, detail)
}

func containsACPAuthMethod(methods []acp.AuthMethod, id string) bool {
	for _, method := range methods {
		methodID, _ := acpAuthMethodLabel(method)
		if methodID == id {
			return true
		}
	}
	return false
}

func formatACPAuthMethods(methods []acp.AuthMethod) string {
	var labels []string
	for _, method := range methods {
		id, name := acpAuthMethodLabel(method)
		if id == "" {
			continue
		}
		if name == "" || name == id {
			labels = append(labels, id)
		} else {
			labels = append(labels, id+" ("+name+")")
		}
	}
	if len(labels) == 0 {
		return "none"
	}
	return strings.Join(labels, ", ")
}

func acpAuthMethodLabel(method acp.AuthMethod) (string, string) {
	switch {
	case method.Agent != nil:
		return method.Agent.Id, method.Agent.Name
	case method.EnvVar != nil:
		return method.EnvVar.Id, method.EnvVar.Name
	case method.Terminal != nil:
		return method.Terminal.Id, method.Terminal.Name
	default:
		return "", ""
	}
}

func jsonMarshalIndent(value any) (string, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	return string(body), err
}

type synchronizedTailBuffer struct {
	mu      sync.Mutex
	maximum int
	body    []byte
}

func newSynchronizedTailBuffer(maximum int) *synchronizedTailBuffer {
	return &synchronizedTailBuffer{maximum: maximum}
}

func (b *synchronizedTailBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.body = append(b.body, value...)
	if len(b.body) > b.maximum {
		b.body = append([]byte(nil), b.body[len(b.body)-b.maximum:]...)
	}
	return len(value), nil
}

func (b *synchronizedTailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(bytes.Clone(b.body))
}

func mergedProcessEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	result := append([]string(nil), base...)
	for name, value := range overrides {
		replaced := false
		for index, current := range result {
			currentName, _, found := strings.Cut(current, "=")
			if found && environmentNameEqual(currentName, name) {
				result[index] = name + "=" + value
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, name+"="+value)
		}
	}
	return result
}

func environmentNameEqual(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
