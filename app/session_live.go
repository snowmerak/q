package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/workspace"
)

// A live session owns the same model and tool connections across turns, like
// the TUI. Only its event sink and foreground cancellation belong to a turn.
// Bubble Tea serializes foreground events and background learning updates.
type liveSession struct {
	busy          sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	prepared      *preparedSession
	program       *tea.Program
	done          chan struct{}
	err           error
	configuration [32]byte
}

type liveSessionResult struct {
	status string
	err    error
}

type liveSessionRequest struct {
	ctx     context.Context
	cancel  context.CancelFunc
	prompt  string
	compact bool
	emit    SessionEventSink
	control *SessionRunControl
	result  chan liveSessionResult
}

type liveSessionCancel struct{ request *liveSessionRequest }
type liveSessionReady struct{}

type liveSessionLearning struct {
	value workspace.LearningConfig
	done  chan struct{}
}

type liveSessionModel struct {
	execution sessionExecutionModel
	request   *liveSessionRequest
	created   bool
	warnings  []error
	commands  *liveSessionCommands
}

// Bubble Tea stops its event loop without joining commands already running.
// Drain those commands before closing resources or releasing the workspace
// lock, so a cancelled learner cannot write a checkpoint after clear/delete.
type liveSessionCommands struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func (commands *liveSessionCommands) wrap(command tea.Cmd) tea.Cmd {
	if command == nil {
		return nil
	}
	return func() tea.Msg {
		commands.mu.Lock()
		if commands.closed {
			commands.mu.Unlock()
			return nil
		}
		commands.wg.Add(1)
		commands.mu.Unlock()
		defer commands.wg.Done()
		message := command()
		if batch, ok := message.(tea.BatchMsg); ok {
			for index := range batch {
				batch[index] = commands.wrap(batch[index])
			}
		}
		return message
	}
}

func (commands *liveSessionCommands) close() {
	commands.mu.Lock()
	commands.closed = true
	commands.mu.Unlock()
	commands.wg.Wait()
}

func liveSessionKey(store workspace.Store, sessionID string) string {
	return filepath.Join(store.Root, workspace.DirectoryName, "sessions", sessionID)
}

func (host *SessionHost) sessionConfiguration(store workspace.Store) ([32]byte, error) {
	loaded, err := host.store.Load()
	if err != nil {
		return [32]byte{}, err
	}
	modelConfig, err := store.LoadModelConfig()
	if err != nil {
		return [32]byte{}, err
	}
	lsp, err := store.LoadLSP()
	if err != nil {
		return [32]byte{}, err
	}
	mcp, err := (mcpconfig.Store{Dir: host.store.Dir}).LoadOrDefault()
	if err != nil {
		return [32]byte{}, err
	}
	project, _, err := host.resolveSessionWorkspace(store.Root)
	if err != nil {
		return [32]byte{}, err
	}
	values := []any{loaded, modelConfig, lsp, mcp, project}
	if host.manager != nil {
		values = append(values, host.manager.Config(), host.manager.Endpoint())
	}
	body, err := json.Marshal(values)
	return sha256.Sum256(body), err
}

// acquireLiveSession leases one foreground operation. Configuration changes
// reopen an idle session; ordinary prompts retain its provider and memory state.
func (host *SessionHost) acquireLiveSession(store workspace.Store, sessionID string) (*liveSession, error) {
	host.liveMu.Lock()
	defer host.liveMu.Unlock()
	if host.liveClosed {
		return nil, errors.New("session host is closed")
	}
	if host.liveSessions == nil {
		host.liveSessions = make(map[string]*liveSession)
	}
	if sessionID != "" {
		selected, err := store.ForSession(sessionID)
		if err != nil {
			return nil, err
		}
		key := liveSessionKey(store, sessionID)
		if existing := host.liveSessions[key]; existing != nil {
			if !existing.busy.TryLock() {
				return nil, workspace.ErrLocked
			}
			configuration, err := host.sessionConfiguration(selected)
			if err != nil {
				existing.busy.Unlock()
				return nil, err
			}
			select {
			case <-existing.done:
			default:
				if existing.configuration == configuration {
					return existing, nil
				}
			}
			delete(host.liveSessions, key)
			err = existing.close()
			existing.busy.Unlock()
			if err != nil {
				return nil, err
			}
		}
	}
	parent := host.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(context.WithValue(parent, delegationHostKey{}, host))
	prepared, err := host.prepareSession(ctx, store, sessionID)
	if err != nil {
		cancel()
		return nil, err
	}
	configuration, err := host.sessionConfiguration(prepared.store)
	if err != nil {
		cancel()
		return nil, errors.Join(err, prepared.Close())
	}
	session := newLiveSession(ctx, cancel, prepared)
	session.configuration = configuration
	session.busy.Lock()
	host.liveSessions[liveSessionKey(prepared.store, prepared.store.SessionID)] = session
	return session, nil
}

func newLiveSession(ctx context.Context, cancel context.CancelFunc, prepared *preparedSession) *liveSession {
	session := &liveSession{ctx: ctx, cancel: cancel, prepared: prepared, done: make(chan struct{})}
	commands := &liveSessionCommands{}
	session.program = tea.NewProgram(
		liveSessionModel{execution: sessionExecutionModel{state: prepared.state, keepAlive: true}, created: prepared.created, warnings: prepared.warnings, commands: commands},
		tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard),
		tea.WithoutRenderer(), tea.WithoutSignalHandler(),
	)
	go func() {
		final, err := session.program.Run()
		if result, ok := final.(liveSessionModel); ok {
			session.prepared.state = result.execution.state
		}
		if ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled) {
			err = nil
		}
		prepared.state.stopSessionLearning()
		commands.close()
		session.err = errors.Join(err, prepared.Close())
		close(session.done)
	}()
	return session
}

func (session *liveSession) execute(parent context.Context, prompt string, compact bool, emit SessionEventSink, control *SessionRunControl) (string, error) {
	if parent == nil {
		parent = context.Background()
	}
	if host := session.ctx.Value(delegationHostKey{}); host != nil {
		parent = context.WithValue(parent, delegationHostKey{}, host)
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(session.ctx, cancel)
	defer stop()
	defer cancel()
	request := &liveSessionRequest{ctx: ctx, cancel: cancel, prompt: prompt, compact: compact, emit: emit, control: control, result: make(chan liveSessionResult, 1)}
	session.program.Send(request)
	select {
	case result := <-request.result:
		return result.status, result.err
	case <-session.done:
		return "", errors.Join(session.err, session.ctx.Err())
	case <-ctx.Done():
		session.program.Send(liveSessionCancel{request: request})
		select {
		case result := <-request.result:
			return result.status, errors.Join(result.err, ctx.Err())
		case <-session.done:
			return "", errors.Join(session.err, ctx.Err())
		}
	}
}

func (session *liveSession) close() error {
	session.cancel()
	<-session.done
	return session.err
}

// ReleaseSession closes an idle session before clear/delete or another owner
// takes its workspace lock. An active turn must be stopped separately.
func (host *SessionHost) ReleaseSession(store workspace.Store, sessionID string) error {
	host.liveMu.Lock()
	defer host.liveMu.Unlock()
	key := liveSessionKey(store, sessionID)
	session := host.liveSessions[key]
	if session == nil {
		return nil
	}
	if !session.busy.TryLock() {
		return workspace.ErrLocked
	}
	defer session.busy.Unlock()
	delete(host.liveSessions, key)
	return session.close()
}

func (host *SessionHost) closeLiveSessions() error {
	host.liveMu.Lock()
	host.liveClosed = true
	sessions := host.liveSessions
	host.liveSessions = nil
	host.liveMu.Unlock()
	for _, session := range sessions {
		session.cancel()
	}
	var err error
	for _, session := range sessions {
		err = errors.Join(err, session.close())
	}
	return err
}

// RefreshLearning applies a workspace toggle to live sessions immediately,
// including learners running between foreground turns.
func (host *SessionHost) RefreshLearning(ctx context.Context, store workspace.Store) error {
	value, err := store.LoadLearningConfig()
	if err != nil {
		return err
	}
	host.liveMu.Lock()
	var sessions []*liveSession
	for _, session := range host.liveSessions {
		if session.prepared.store.Root == store.Root {
			sessions = append(sessions, session)
		}
	}
	host.liveMu.Unlock()
	for _, session := range sessions {
		message := liveSessionLearning{value: value, done: make(chan struct{})}
		session.program.Send(message)
		select {
		case <-message.done:
		case <-session.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Embedding transitions need the old archive leases closed before rebuilding.
func (host *SessionHost) releaseIdleSessions() error {
	host.liveMu.Lock()
	defer host.liveMu.Unlock()
	var leased []*liveSession
	defer func() {
		for _, session := range leased {
			session.busy.Unlock()
		}
	}()
	for _, session := range host.liveSessions {
		if !session.busy.TryLock() {
			return workspace.ErrLocked
		}
		leased = append(leased, session)
	}
	var err error
	for key, session := range host.liveSessions {
		delete(host.liveSessions, key)
		err = errors.Join(err, session.close())
	}
	return err
}

func (m liveSessionModel) Init() tea.Cmd  { return func() tea.Msg { return liveSessionReady{} } }
func (m liveSessionModel) View() tea.View { return tea.NewView("") }

func (m *liveSessionModel) finish(err error) {
	if m.request != nil {
		m.request.result <- liveSessionResult{status: m.execution.state.status, err: err}
		m.request = nil
	}
}

func (m liveSessionModel) Update(message tea.Msg) (updatedModel tea.Model, nextCommand tea.Cmd) {
	defer func() { nextCommand = m.commands.wrap(nextCommand) }()
	switch request := message.(type) {
	case liveSessionReady:
		command := m.execution.state.startNextLearningSegment()
		return m, command
	case liveSessionLearning:
		var command tea.Cmd
		if m.execution.state.workspaceLearning.Disabled != request.value.Disabled {
			m.execution.state.workspaceLearning = request.value
			if request.value.Disabled {
				m.execution.state.stopSessionLearning()
			} else {
				m.execution.state.resetSessionLearning()
				command = m.execution.state.startNextLearningSegment()
			}
		}
		close(request.done)
		return m, command
	case *liveSessionRequest:
		if m.request != nil {
			request.result <- liveSessionResult{err: workspace.ErrLocked}
			return m, nil
		}
		m.request = request
		if err := request.ctx.Err(); err != nil {
			m.finish(err)
			return m, nil
		}
		if request.emit != nil {
			store := m.execution.state.workspaceStore
			if err := request.emit(SessionEvent{Type: "session", WorkingDirectory: store.Root, SessionID: store.SessionID, Created: m.created}); err != nil {
				m.finish(err)
				return m, nil
			}
			for _, warning := range m.warnings {
				if err := request.emit(SessionEvent{Type: "status", Detail: "Warning: " + warning.Error()}); err != nil {
					m.finish(err)
					return m, nil
				}
			}
		}
		m.created, m.warnings = false, nil
		// The turn follows the caller's cancellation; learning and tools keep
		// their session context. Commands capture this turn context on start.
		parent := m.execution.state.ctx
		m.execution.state.ctx = request.ctx
		var updated tea.Model
		var command tea.Cmd
		if request.compact {
			updated, command = m.execution.state.startManualCompaction()
		} else {
			updated, command = m.execution.state.startChatTurn(request.prompt, true)
		}
		m.execution.state = updated.(model)
		m.execution.state.ctx = parent
		if !m.execution.state.waiting {
			var err error
			if !request.compact {
				err = fmt.Errorf("%w: %s", ErrSessionRuntimeUnavailable, m.execution.state.status)
			}
			m.finish(err)
			return m, nil
		}
		m.execution.emit, m.execution.control, m.execution.cancel = request.emit, request.control, request.cancel
		m.execution.err = nil
		m.execution.contextUsed, m.execution.contextSize = -1, -1
		// Update below consumes this callback synchronously, before returning
		// the copied model to Bubble Tea.
		m.execution.finished = func(state model, err error) {
			m.execution.state = state
			m.finish(err)
		}
		if !m.execution.emitContextUsage() {
			updated, command := m.execution.failExecution()
			m.execution = updated.(sessionExecutionModel)
			return m, command
		}
		return m, tea.Batch(command, waitSessionRunControl(request.control, parent))
	case liveSessionCancel:
		if m.request == request.request {
			updated, _ := m.execution.state.interruptTurn()
			m.execution.state = updated.(model)
			m.execution.detach()
			m.finish(request.request.ctx.Err())
		}
		return m, nil
	}
	// The callback must close over this Update's copy, not the start message's
	// copy; otherwise later messages would finish an obsolete request.
	if m.request != nil {
		m.execution.finished = func(state model, err error) {
			m.execution.state = state
			m.finish(err)
		}
	}
	updated, command := m.execution.Update(message)
	m.execution = updated.(sessionExecutionModel)
	if result, ok := message.(compactionResultMsg); ok && m.request != nil && m.request.compact && (result.turnID == 0 || result.turnID == m.execution.state.turnID) {
		m.execution.detach()
		err := result.err
		if err == nil && result.checkpoint == "" && (result.response == nil || len(result.response.Choices) == 0) {
			err = errors.New("context compaction returned no response")
		}
		m.finish(err)
	}
	return m, command
}
