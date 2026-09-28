package studio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

const (
	studioRunsDirectory = "studio-runs"
	studioLatestRunFile = "studio-run.json"
	studioRunVersion    = 1
	maximumRunLogSize   = 128 << 20
	maximumRunEventSize = 8 << 20
	maximumRunEventPage = 256
	maximumRunWait      = 30 * time.Second
)

type controlledSessionRunner interface {
	RunControlled(context.Context, workspace.Store, string, string, app.SessionEventSink, *app.SessionRunControl) error
}

type studioRunQuestion struct {
	CallID   string                    `json:"call_id"`
	Question string                    `json:"question"`
	Context  string                    `json:"context,omitempty"`
	Choices  []app.AgentQuestionChoice `json:"choices,omitempty"`
}

type studioRunSnapshot struct {
	Version         int                `json:"version"`
	ID              string             `json:"id"`
	SessionID       string             `json:"session_id"`
	Status          string             `json:"status"`
	Outcome         string             `json:"outcome,omitempty"`
	Error           string             `json:"error,omitempty"`
	PendingQuestion *studioRunQuestion `json:"pending_question,omitempty"`
	Cursor          int64              `json:"cursor"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	FinishedAt      *time.Time         `json:"finished_at,omitempty"`
}

type studioRunEvent struct {
	Cursor int64            `json:"cursor"`
	At     time.Time        `json:"at"`
	Event  app.SessionEvent `json:"event"`
}

type studioRunPage struct {
	Run        studioRunSnapshot `json:"run"`
	Events     []studioRunEvent  `json:"events"`
	NextCursor int64             `json:"next_cursor"`
}

type studioRunCommandRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
	Action        string `json:"action"`
	CallID        string `json:"call_id,omitempty"`
	Answer        string `json:"answer,omitempty"`
	Content       string `json:"content,omitempty"`
}

type studioRun struct {
	root     string
	store    workspace.Store
	control  *app.SessionRunControl
	cancel   context.CancelFunc
	log      *os.File
	mu       sync.Mutex
	snapshot studioRunSnapshot
	events   []studioRunEvent
	notify   chan struct{}
	guidance string
	closed   bool
}

type sessionRunService struct {
	ctx       context.Context
	cancel    context.CancelFunc
	runner    sessionRunner
	mu        sync.Mutex
	runs      map[string]*studioRun
	active    map[string]string
	latestIDs map[string]string
	wg        sync.WaitGroup
}

func newSessionRunService(parent context.Context, runner sessionRunner) *sessionRunService {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &sessionRunService{
		ctx: ctx, cancel: cancel, runner: runner,
		runs: make(map[string]*studioRun), active: make(map[string]string), latestIDs: make(map[string]string),
	}
}

func (service *sessionRunService) Close() error {
	if service == nil {
		return nil
	}
	service.cancel()
	service.wg.Wait()
	service.mu.Lock()
	runs := make([]*studioRun, 0, len(service.runs))
	for _, run := range service.runs {
		runs = append(runs, run)
	}
	service.mu.Unlock()
	var result error
	for _, run := range runs {
		result = errors.Join(result, run.close())
	}
	return result
}

func (service *sessionRunService) stats() (active, resident int) {
	if service == nil {
		return 0, 0
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	return len(service.active), len(service.runs)
}

func sessionRunKey(root, sessionID string) string { return root + "\x00" + sessionID }
func studioRunKey(root, sessionID, runID string) string {
	return sessionRunKey(root, sessionID) + "\x00" + runID
}

func (service *sessionRunService) start(root, sessionID, prompt string) (*studioRun, error) {
	if service == nil || service.runner == nil {
		return nil, app.ErrSessionRuntimeUnavailable
	}
	store, err := (workspace.Store{Root: root}).ForSession(sessionID)
	if err != nil {
		return nil, err
	}
	if _, err := store.Load(); err != nil {
		return nil, err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, errors.New("content is required")
	}
	if len(prompt) > subagent.MaximumDelegatePromptBytes {
		return nil, fmt.Errorf("content must not exceed %d bytes", subagent.MaximumDelegatePromptBytes)
	}

	key := sessionRunKey(root, sessionID)
	service.mu.Lock()
	if activeID := service.active[key]; activeID != "" {
		service.mu.Unlock()
		return nil, errors.New("the selected session already has a running turn")
	}
	select {
	case <-service.ctx.Done():
		service.mu.Unlock()
		return nil, errors.New("Studio is shutting down")
	default:
	}
	runID, err := workspace.NewSessionID()
	if err != nil {
		service.mu.Unlock()
		return nil, err
	}
	run, err := createStudioRun(root, store, runID)
	if err != nil {
		service.mu.Unlock()
		return nil, err
	}
	service.runs[studioRunKey(root, sessionID, runID)] = run
	service.active[key] = runID
	service.latestIDs[key] = runID
	service.wg.Add(1)
	service.mu.Unlock()
	go service.execute(run, prompt)
	return run, nil
}

func (service *sessionRunService) execute(run *studioRun, prompt string) {
	defer service.wg.Done()
	runContext, cancel := context.WithCancel(service.ctx)
	run.mu.Lock()
	run.cancel = cancel
	run.mu.Unlock()
	defer cancel()
	emit := func(event app.SessionEvent) error {
		event.RunID = run.snapshot.ID
		return run.append(event)
	}
	var runErr error
	if controlled, ok := service.runner.(controlledSessionRunner); ok {
		runErr = controlled.RunControlled(runContext, run.store, run.store.SessionID, prompt, emit, run.control)
	} else {
		runErr = service.runner.Run(runContext, run.store, run.store.SessionID, prompt, emit)
	}
	if runErr != nil && !run.terminal() {
		typeName := "error"
		detail := runErr.Error()
		if errors.Is(runErr, context.Canceled) || errors.Is(runContext.Err(), context.Canceled) {
			typeName, detail = "cancelled", "Turn stopped because Studio is shutting down"
		}
		_ = run.append(app.SessionEvent{Type: typeName, RunID: run.snapshot.ID, Detail: detail})
	} else if runErr == nil && !run.terminal() {
		_ = run.append(app.SessionEvent{Type: "result", RunID: run.snapshot.ID, Outcome: "succeeded"})
	}
	guidance := run.takeGuidance()
	service.mu.Lock()
	delete(service.active, sessionRunKey(run.root, run.store.SessionID))
	service.mu.Unlock()
	_ = run.close()
	if guidance == "" || service.ctx.Err() != nil {
		return
	}
	next, err := service.start(run.root, run.store.SessionID, "User guidance for the interrupted task:\n\n"+guidance)
	if err != nil {
		_ = run.reopenAndAppend(app.SessionEvent{Type: "error", RunID: run.snapshot.ID, Detail: "Could not apply guidance: " + err.Error()})
		return
	}
	_ = run.reopenAndAppend(app.SessionEvent{Type: "redirect", RunID: next.snapshot.ID, Detail: "Continued with user guidance"})
}

func (service *sessionRunService) latest(root, sessionID string) (*studioRun, error) {
	service.mu.Lock()
	key := sessionRunKey(root, sessionID)
	if runID := service.active[key]; runID != "" {
		run := service.runs[studioRunKey(root, sessionID, runID)]
		service.mu.Unlock()
		if run != nil {
			return run, nil
		}
	} else if runID := service.latestIDs[key]; runID != "" {
		run := service.runs[studioRunKey(root, sessionID, runID)]
		service.mu.Unlock()
		if run != nil {
			return run, nil
		}
	}
	service.mu.Unlock()
	store, err := (workspace.Store{Root: root}).ForSession(sessionID)
	if err != nil {
		return nil, err
	}
	run, err := loadLatestStudioRun(root, store)
	if err != nil {
		return nil, err
	}
	service.mu.Lock()
	service.runs[studioRunKey(root, sessionID, run.snapshot.ID)] = run
	service.latestIDs[key] = run.snapshot.ID
	service.mu.Unlock()
	return run, nil
}

func (service *sessionRunService) lookup(root, sessionID, runID string) (*studioRun, error) {
	service.mu.Lock()
	run := service.runs[studioRunKey(root, sessionID, runID)]
	service.mu.Unlock()
	if run != nil {
		return run, nil
	}
	latest, err := service.latest(root, sessionID)
	if err != nil {
		return nil, err
	}
	if latest.snapshotCopy().ID != runID {
		return nil, os.ErrNotExist
	}
	return latest, nil
}

func (service *sessionRunService) forget(root, sessionID string) error {
	key := sessionRunKey(root, sessionID)
	service.mu.Lock()
	if service.active[key] != "" {
		service.mu.Unlock()
		return errors.New("the selected session still has a running turn")
	}
	var removed []*studioRun
	for id, run := range service.runs {
		if run.root == root && run.store.SessionID == sessionID {
			removed = append(removed, run)
			delete(service.runs, id)
		}
	}
	delete(service.latestIDs, key)
	service.mu.Unlock()
	var result error
	for _, run := range removed {
		result = errors.Join(result, run.close())
	}
	return result
}

func createStudioRun(root string, store workspace.Store, runID string) (*studioRun, error) {
	directory := filepath.Join(store.SessionDir(), studioRunsDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create Studio run directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("secure Studio run directory: %w", err)
	}
	log, err := os.OpenFile(filepath.Join(directory, runID+".ndjson"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create Studio run log: %w", err)
	}
	now := time.Now().UTC()
	run := &studioRun{
		root: root, store: store, control: app.NewSessionRunControl(), log: log, notify: make(chan struct{}),
		snapshot: studioRunSnapshot{
			Version: studioRunVersion, ID: runID, SessionID: store.SessionID,
			Status: "queued", CreatedAt: now, UpdatedAt: now,
		},
	}
	if err := run.persistSnapshotLocked(); err != nil {
		_ = log.Close()
		return nil, err
	}
	return run, nil
}

func loadLatestStudioRun(root string, store workspace.Store) (*studioRun, error) {
	snapshotPath := filepath.Join(store.SessionDir(), studioLatestRunFile)
	info, err := os.Stat(snapshotPath)
	if err != nil {
		return nil, err
	}
	if info.Size() > maximumSessionRequestSize {
		return nil, errors.New("Studio run snapshot is too large")
	}
	body, err := os.ReadFile(snapshotPath)
	if err != nil {
		return nil, err
	}
	var snapshot studioRunSnapshot
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode Studio run snapshot: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode Studio run snapshot: multiple JSON values")
	}
	if snapshot.Version != studioRunVersion || snapshot.ID == "" || snapshot.SessionID != store.SessionID {
		return nil, errors.New("invalid Studio run snapshot")
	}
	events, err := readStudioRunEvents(filepath.Join(store.SessionDir(), studioRunsDirectory, snapshot.ID+".ndjson"))
	if err != nil {
		return nil, err
	}
	rebuilt := snapshot
	rebuilt.Status, rebuilt.Outcome, rebuilt.Error = "queued", "", ""
	rebuilt.PendingQuestion, rebuilt.FinishedAt, rebuilt.Cursor = nil, nil, 0
	for _, event := range events {
		rebuilt.Cursor, rebuilt.UpdatedAt = event.Cursor, event.At
		placeholder := &studioRun{snapshot: rebuilt}
		placeholder.applyEventLocked(event.Event, event.At)
		rebuilt = placeholder.snapshot
	}
	snapshot = rebuilt
	run := &studioRun{root: root, store: store, snapshot: snapshot, events: events, notify: make(chan struct{}), closed: true}
	if !terminalRunStatus(snapshot.Status) {
		log, openErr := os.OpenFile(filepath.Join(store.SessionDir(), studioRunsDirectory, snapshot.ID+".ndjson"), os.O_APPEND|os.O_WRONLY, 0o600)
		if openErr != nil {
			return nil, openErr
		}
		run.log, run.closed = log, false
		if err := run.append(app.SessionEvent{Type: "recovered", RunID: snapshot.ID, Detail: "Studio restarted while this turn was active; continue the session to recover persisted work"}); err != nil {
			_ = log.Close()
			return nil, err
		}
		_ = run.close()
	}
	return run, nil
}

func readStudioRunEvents(path string) ([]studioRunEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maximumRunLogSize {
		return nil, fmt.Errorf("Studio run log exceeds %d bytes", maximumRunLogSize)
	}
	scanner := bufio.NewScanner(io.LimitReader(file, maximumRunLogSize+1))
	scanner.Buffer(make([]byte, 64<<10), maximumRunEventSize)
	result := make([]studioRunEvent, 0)
	var previous int64
	var pending []byte
	decode := func(body []byte) error {
		var event studioRunEvent
		if err := json.Unmarshal(body, &event); err != nil {
			return fmt.Errorf("decode Studio run event: %w", err)
		}
		if event.Cursor != previous+1 {
			return errors.New("Studio run event cursor is not contiguous")
		}
		previous = event.Cursor
		result = append(result, event)
		return nil
	}
	for scanner.Scan() {
		if pending != nil {
			if err := decode(pending); err != nil {
				return nil, err
			}
		}
		pending = append(pending[:0], scanner.Bytes()...)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if pending != nil {
		var tail [1]byte
		_, readErr := file.ReadAt(tail[:], max(0, info.Size()-1))
		partialTail := readErr == nil && tail[0] != '\n'
		if err := decode(pending); err != nil && !partialTail {
			return nil, err
		}
	}
	return result, nil
}

func (run *studioRun) append(event app.SessionEvent) error {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.closed || run.log == nil {
		return errors.New("Studio run log is closed")
	}
	now := time.Now().UTC()
	record := studioRunEvent{Cursor: run.snapshot.Cursor + 1, At: now, Event: event}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(body) > maximumRunEventSize {
		return fmt.Errorf("Studio run event exceeds %d bytes", maximumRunEventSize)
	}
	body = append(body, '\n')
	if _, err := run.log.Write(body); err != nil {
		return fmt.Errorf("append Studio run event: %w", err)
	}
	run.snapshot.Cursor = record.Cursor
	run.snapshot.UpdatedAt = now
	run.applyEventLocked(event, now)
	run.events = append(run.events, record)
	if event.Type != "stream" || record.Cursor%32 == 0 {
		if err := run.log.Sync(); err != nil {
			return err
		}
		if err := run.persistSnapshotLocked(); err != nil {
			return err
		}
	}
	close(run.notify)
	run.notify = make(chan struct{})
	return nil
}

func (run *studioRun) reopenAndAppend(event app.SessionEvent) error {
	run.mu.Lock()
	if !run.closed {
		run.mu.Unlock()
		return run.append(event)
	}
	log, err := os.OpenFile(filepath.Join(run.store.SessionDir(), studioRunsDirectory, run.snapshot.ID+".ndjson"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		run.mu.Unlock()
		return err
	}
	run.log, run.closed = log, false
	run.mu.Unlock()
	err = run.append(event)
	return errors.Join(err, run.close())
}

func (run *studioRun) applyEventLocked(event app.SessionEvent, now time.Time) {
	switch event.Type {
	case "session":
		run.snapshot.Status = "running"
	case "question":
		run.snapshot.Status = "waiting"
		run.snapshot.PendingQuestion = &studioRunQuestion{
			CallID: event.CallID, Question: event.Question, Context: event.Context,
			Choices: append([]app.AgentQuestionChoice(nil), event.Choices...),
		}
	case "question_answered":
		run.snapshot.PendingQuestion = nil
		run.snapshot.Status = "running"
	case "control":
		if event.Action == "paused" {
			run.snapshot.Status = "paused"
		} else if event.Action == "resumed" {
			run.snapshot.Status = "running"
		}
	case "result":
		run.snapshot.Status, run.snapshot.Outcome = "completed", event.Outcome
		run.snapshot.PendingQuestion = nil
		run.snapshot.FinishedAt = timePointer(now)
	case "cancelled":
		run.snapshot.PendingQuestion = nil
		if run.guidance != "" {
			run.snapshot.Status = "redirecting"
		} else {
			run.snapshot.Status = "cancelled"
			run.snapshot.FinishedAt = timePointer(now)
		}
	case "error":
		run.snapshot.Status, run.snapshot.Error = "failed", event.Detail
		run.snapshot.PendingQuestion = nil
		run.snapshot.FinishedAt = timePointer(now)
	case "recovered":
		run.snapshot.Status = "interrupted"
		run.snapshot.PendingQuestion = nil
		run.snapshot.FinishedAt = timePointer(now)
	case "redirect":
		run.snapshot.Status = "redirected"
		run.snapshot.FinishedAt = timePointer(now)
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func terminalRunStatus(status string) bool {
	switch status {
	case "completed", "cancelled", "failed", "interrupted", "redirected":
		return true
	default:
		return false
	}
}

func (run *studioRun) terminal() bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	return terminalRunStatus(run.snapshot.Status)
}

func (run *studioRun) snapshotCopy() studioRunSnapshot {
	run.mu.Lock()
	defer run.mu.Unlock()
	result := run.snapshot
	if run.snapshot.PendingQuestion != nil {
		question := *run.snapshot.PendingQuestion
		question.Choices = append([]app.AgentQuestionChoice(nil), question.Choices...)
		result.PendingQuestion = &question
	}
	return result
}

func (run *studioRun) page(after int64, limit int) studioRunPage {
	run.mu.Lock()
	defer run.mu.Unlock()
	if limit <= 0 || limit > maximumRunEventPage {
		limit = maximumRunEventPage
	}
	result := studioRunPage{Run: run.snapshot, Events: make([]studioRunEvent, 0, limit), NextCursor: after}
	if run.snapshot.PendingQuestion != nil {
		question := *run.snapshot.PendingQuestion
		question.Choices = append([]app.AgentQuestionChoice(nil), question.Choices...)
		result.Run.PendingQuestion = &question
	}
	for _, event := range run.events {
		if event.Cursor <= after {
			continue
		}
		result.Events = append(result.Events, event)
		result.NextCursor = event.Cursor
		if len(result.Events) == limit {
			break
		}
	}
	return result
}

func (run *studioRun) wait(after int64, duration time.Duration, ctx context.Context) studioRunPage {
	for {
		run.mu.Lock()
		ready := run.snapshot.Cursor > after || terminalRunStatus(run.snapshot.Status)
		notify := run.notify
		run.mu.Unlock()
		if ready || duration <= 0 {
			return run.page(after, maximumRunEventPage)
		}
		timer := time.NewTimer(duration)
		select {
		case <-notify:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			return run.page(after, maximumRunEventPage)
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return run.page(after, maximumRunEventPage)
		}
	}
}

func (run *studioRun) persistSnapshotLocked() error {
	body, err := json.MarshalIndent(run.snapshot, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	path := filepath.Join(run.store.SessionDir(), studioLatestRunFile)
	temporary, err := os.CreateTemp(run.store.SessionDir(), ".studio-run-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := fsreplace.Replace(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}

func (run *studioRun) close() error {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.closed {
		return nil
	}
	run.closed = true
	var result error
	if run.log != nil {
		result = errors.Join(run.log.Sync(), run.log.Close())
		run.log = nil
	}
	return result
}

func (run *studioRun) takeGuidance() string {
	run.mu.Lock()
	defer run.mu.Unlock()
	value := run.guidance
	run.guidance = ""
	return value
}

func (run *studioRun) queueGuidance(content string) error {
	run.mu.Lock()
	defer run.mu.Unlock()
	if terminalRunStatus(run.snapshot.Status) {
		return errors.New("session turn already finished")
	}
	if run.guidance != "" {
		return errors.New("guidance is already queued")
	}
	run.guidance = content
	return nil
}

func (service *sessionsService) serveRunLatest(writer http.ResponseWriter, request *http.Request) {
	root, store, ok := sessionStoreFromQuery(writer, request)
	if !ok {
		return
	}
	run, err := service.runs.latest(root, store.SessionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeAPIError(writer, http.StatusNotFound, errors.New("this session has no Studio run"))
		} else {
			writeSessionError(writer, err)
		}
		return
	}
	writeJSON(writer, http.StatusOK, run.snapshotCopy())
}

func (service *sessionsService) serveRunDetail(writer http.ResponseWriter, request *http.Request) {
	root, store, ok := sessionStoreFromQuery(writer, request)
	if !ok {
		return
	}
	run, err := service.runs.lookup(root, store.SessionID, request.PathValue("run"))
	if err != nil {
		writeAPIError(writer, http.StatusNotFound, errors.New("Studio run does not exist"))
		return
	}
	writeJSON(writer, http.StatusOK, run.snapshotCopy())
}

func (service *sessionsService) serveRunEvents(writer http.ResponseWriter, request *http.Request) {
	root, store, ok := sessionStoreFromQuery(writer, request)
	if !ok {
		return
	}
	run, err := service.runs.lookup(root, store.SessionID, request.PathValue("run"))
	if err != nil {
		writeAPIError(writer, http.StatusNotFound, errors.New("Studio run does not exist"))
		return
	}
	after, err := strconv.ParseInt(defaultString(request.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil || after < 0 {
		writeAPIError(writer, http.StatusBadRequest, errors.New("after must be a non-negative event cursor"))
		return
	}
	waitMilliseconds, err := strconv.Atoi(defaultString(request.URL.Query().Get("wait_ms"), "0"))
	if err != nil || waitMilliseconds < 0 {
		writeAPIError(writer, http.StatusBadRequest, errors.New("wait_ms must be non-negative"))
		return
	}
	wait := min(time.Duration(waitMilliseconds)*time.Millisecond, maximumRunWait)
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, run.wait(after, wait, request.Context()))
}

func (service *sessionsService) serveRunCommand(writer http.ResponseWriter, request *http.Request) {
	var input studioRunCommandRequest
	if err := decodeSessionRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	store, err := (workspace.Store{Root: root}).ForSession(request.PathValue("session"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	run, err := service.runs.lookup(root, store.SessionID, request.PathValue("run"))
	if err != nil {
		writeAPIError(writer, http.StatusNotFound, errors.New("Studio run does not exist"))
		return
	}
	if run.control == nil || terminalRunStatus(run.snapshotCopy().Status) {
		writeAPIError(writer, http.StatusConflict, errors.New("Studio run is no longer active"))
		return
	}
	switch strings.TrimSpace(input.Action) {
	case "answer":
		if strings.TrimSpace(input.CallID) == "" {
			writeAPIError(writer, http.StatusBadRequest, errors.New("call_id is required"))
			return
		}
		snapshot := run.snapshotCopy()
		if snapshot.PendingQuestion == nil || snapshot.PendingQuestion.CallID != input.CallID {
			writeAPIError(writer, http.StatusConflict, errors.New("the question is no longer pending"))
			return
		}
		if err := run.control.Answer(request.Context(), input.Answer); err != nil {
			writeAPIError(writer, http.StatusConflict, err)
			return
		}
	case "pause":
		if err := run.control.Pause(request.Context()); err != nil {
			writeAPIError(writer, http.StatusConflict, err)
			return
		}
	case "resume":
		if err := run.control.Resume(request.Context()); err != nil {
			writeAPIError(writer, http.StatusConflict, err)
			return
		}
	case "cancel":
		if err := run.control.Cancel(request.Context()); err != nil {
			writeAPIError(writer, http.StatusConflict, err)
			return
		}
	case "guidance":
		content := strings.TrimSpace(input.Content)
		if content == "" {
			writeAPIError(writer, http.StatusBadRequest, errors.New("guidance content is required"))
			return
		}
		if len(content) > subagent.MaximumDelegatePromptBytes {
			writeAPIError(writer, http.StatusRequestEntityTooLarge, errors.New("guidance content is too large"))
			return
		}
		if err := run.queueGuidance(content); err != nil {
			writeAPIError(writer, http.StatusConflict, err)
			return
		}
		if err := run.control.Cancel(request.Context()); err != nil {
			_ = run.takeGuidance()
			writeAPIError(writer, http.StatusConflict, err)
			return
		}
	default:
		writeAPIError(writer, http.StatusBadRequest, errors.New("action must be answer, pause, resume, cancel, or guidance"))
		return
	}
	writeJSON(writer, http.StatusOK, run.snapshotCopy())
}

func sessionStoreFromQuery(writer http.ResponseWriter, request *http.Request) (string, workspace.Store, bool) {
	root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return "", workspace.Store{}, false
	}
	store, err := (workspace.Store{Root: root}).ForSession(request.PathValue("session"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return "", workspace.Store{}, false
	}
	if _, err := store.Load(); err != nil {
		writeSessionError(writer, err)
		return "", workspace.Store{}, false
	}
	return root, store, true
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func clearStudioRunArtifacts(store workspace.Store) error {
	var result error
	if err := os.Remove(filepath.Join(store.SessionDir(), studioLatestRunFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(result, err)
	}
	if err := os.RemoveAll(filepath.Join(store.SessionDir(), studioRunsDirectory)); err != nil {
		result = errors.Join(result, err)
	}
	return result
}
