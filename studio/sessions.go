package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

const maximumSessionRequestSize = 64 << 10

type sessionRunner interface {
	Run(context.Context, workspace.Store, string, string, app.SessionEventSink) error
}

type sessionsService struct {
	runner    sessionRunner
	admission chan struct{}
}

type sessionSummary struct {
	SessionID string    `json:"session_id"`
	RunID     string    `json:"run_id,omitempty"`
	Title     string    `json:"title,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type sessionMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	Name       string            `json:"name,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolCalls  []client.ToolCall `json:"tool_calls,omitempty"`
}

type sessionDetail struct {
	WorkspaceRoot string                `json:"workspace_root"`
	Session       sessionSummary        `json:"session"`
	Transcript    []sessionMessage      `json:"transcript"`
	ActiveTask    *workspace.ActiveTask `json:"active_task,omitempty"`
}

type sessionListResponse struct {
	WorkspaceRoot string           `json:"workspace_root"`
	Sessions      []sessionSummary `json:"sessions"`
}

type workspaceRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
}

type sessionRunRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
	Content       string `json:"content"`
}

func newSessionsService(runner sessionRunner) *sessionsService {
	return &sessionsService{runner: runner, admission: make(chan struct{}, 1)}
}

func (service *sessionsService) serveCollection(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		service.serveList(writer, request)
	case http.MethodPost:
		service.serveCreate(writer, request)
	default:
		writer.Header().Set("Allow", "GET, POST")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (service *sessionsService) serveList(writer http.ResponseWriter, request *http.Request) {
	root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	base := workspace.Store{Root: root}
	if err := base.MigrateLegacySession(); err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	entries, err := base.ListSessions()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	sessions := make([]sessionSummary, 0, len(entries))
	for _, entry := range entries {
		sessions = append(sessions, summarizeSession(entry))
	}
	writeJSON(writer, http.StatusOK, sessionListResponse{WorkspaceRoot: root, Sessions: sessions})
}

func (service *sessionsService) serveCreate(writer http.ResponseWriter, request *http.Request) {
	var input workspaceRequest
	if err := decodeSessionRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if err := (workspace.Store{Root: root}).MigrateLegacySession(); err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	store, lock, err := workspace.CreateSession(root, "q studio")
	if err != nil {
		writeSessionError(writer, err)
		return
	}
	if err := lock.Close(); err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	value, err := store.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusCreated, detailFromSession(root, store, value))
}

func (service *sessionsService) serveDetail(writer http.ResponseWriter, request *http.Request) {
	root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	store, err := (workspace.Store{Root: root}).ForSession(request.PathValue("session"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	value, err := store.Load()
	if err != nil {
		writeSessionError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, detailFromSession(root, store, value))
}

func (service *sessionsService) serveMessage(writer http.ResponseWriter, request *http.Request) {
	if service.runner == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, app.ErrSessionRuntimeUnavailable)
		return
	}
	var input sessionRunRequest
	if err := decodeSessionRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" {
		writeAPIError(writer, http.StatusBadRequest, errors.New("content is required"))
		return
	}
	if len(input.Content) > subagent.MaximumDelegatePromptBytes {
		writeAPIError(writer, http.StatusRequestEntityTooLarge, fmt.Errorf("content must not exceed %d bytes", subagent.MaximumDelegatePromptBytes))
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

	select {
	case service.admission <- struct{}{}:
		defer func() { <-service.admission }()
	default:
		writeAPIError(writer, http.StatusTooManyRequests, errors.New("another Studio turn is already running"))
		return
	}

	flusher, canFlush := writer.(http.Flusher)
	started := false
	terminalWritten := false
	emit := func(event app.SessionEvent) error {
		if !started {
			if event.Type != "session" {
				return errors.New("session turn did not begin with a session event")
			}
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("Content-Type", "application/x-ndjson")
			writer.Header().Set("X-Q-Session-ID", event.SessionID)
			writer.WriteHeader(http.StatusOK)
			started = true
		}
		if err := json.NewEncoder(writer).Encode(event); err != nil {
			return err
		}
		terminalWritten = event.Type == "result" || event.Type == "cancelled" || event.Type == "error"
		if canFlush {
			flusher.Flush()
		}
		return nil
	}

	runErr := service.runner.Run(request.Context(), store, store.SessionID, input.Content, emit)
	if runErr == nil || terminalWritten {
		return
	}
	if !started {
		writeSessionError(writer, runErr)
		return
	}
	if request.Context().Err() != nil {
		return
	}
	_ = json.NewEncoder(writer).Encode(app.SessionEvent{Type: "error", Detail: runErr.Error()})
	if canFlush {
		flusher.Flush()
	}
}

func summarizeSession(entry workspace.SessionEntry) sessionSummary {
	return sessionSummary{
		SessionID: entry.Store.SessionID, RunID: entry.RunID,
		Title: entry.Title, UpdatedAt: entry.UpdatedAt,
	}
}

func detailFromSession(root string, store workspace.Store, value workspace.Session) sessionDetail {
	updatedAt := time.Time{}
	if value.UpdatedAt != nil {
		updatedAt = value.UpdatedAt.UTC()
	}
	transcript := make([]sessionMessage, 0, len(value.Transcript))
	for _, message := range value.Transcript {
		transcript = append(transcript, sessionMessage{
			Role: string(message.Role), Content: message.TextContent(), Name: message.Name,
			ToolCallID: message.ToolCallID, ToolCalls: message.ToolCalls,
		})
	}
	return sessionDetail{
		WorkspaceRoot: root,
		Session: sessionSummary{
			SessionID: store.SessionID, RunID: value.RunID,
			Title: value.Title, UpdatedAt: updatedAt,
		},
		Transcript: transcript, ActiveTask: value.ActiveTask,
	}
}

func canonicalWorkspaceDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("workspace_root is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve workspace_root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		return "", fmt.Errorf("resolve workspace_root: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", errors.New("workspace_root must identify an accessible directory")
	}
	if err := workspace.RejectHomeDirectory(canonical); err != nil {
		return "", err
	}
	return canonical, nil
}

func decodeSessionRequest(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumSessionRequestSize)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode request: multiple JSON values")
		}
		return fmt.Errorf("decode request: %w", err)
	}
	return nil
}

func writeSessionError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workspace.ErrLocked):
		writeAPIError(writer, http.StatusConflict, errors.New("the selected session is already in use"))
	case errors.Is(err, workspace.ErrNotFound):
		writeAPIError(writer, http.StatusNotFound, errors.New("the selected session does not exist"))
	case errors.Is(err, app.ErrSessionRuntimeUnavailable):
		writeAPIError(writer, http.StatusServiceUnavailable, err)
	case errors.Is(err, context.Canceled):
		writeAPIError(writer, 499, errors.New("the request was cancelled"))
	default:
		writeAPIError(writer, http.StatusInternalServerError, err)
	}
}
