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

type sessionCompactor interface {
	Compact(context.Context, workspace.Store, string) (string, error)
}

type sessionsService struct {
	runner sessionRunner
	runs   *sessionRunService
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

type learningUpdateRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
	Disabled      bool   `json:"disabled"`
}

type learningResponse struct {
	WorkspaceRoot string `json:"workspace_root"`
	Enabled       bool   `json:"enabled"`
}

type sessionOperationResponse struct {
	Status string `json:"status"`
}

type sessionRunRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
	Content       string `json:"content"`
}

func newSessionsService(parent context.Context, runner sessionRunner) *sessionsService {
	return &sessionsService{runner: runner, runs: newSessionRunService(parent, runner)}
}

func (service *sessionsService) Close() error {
	if service == nil {
		return nil
	}
	return service.runs.Close()
}

func (service *sessionsService) serveDirectoryListing(writer http.ResponseWriter, request *http.Request) {
	listing, err := browseDirectories(request.URL.Query().Get("path"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, listing)
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
	if request.Method == http.MethodDelete {
		service.serveDelete(writer, request)
		return
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", "GET, DELETE")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
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

func (service *sessionsService) serveDelete(writer http.ResponseWriter, request *http.Request) {
	root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if err := workspace.DeleteSession(root, request.PathValue("session"), "q studio session delete"); err != nil {
		writeSessionError(writer, err)
		return
	}
	if err := service.runs.forget(root, request.PathValue("session")); err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (service *sessionsService) serveClear(writer http.ResponseWriter, request *http.Request) {
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
	value, err := workspace.ResetSession(root, request.PathValue("session"), "q studio session clear")
	if err != nil {
		writeSessionError(writer, err)
		return
	}
	store, err := (workspace.Store{Root: root}).ForSession(request.PathValue("session"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if err := clearStudioRunArtifacts(store); err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if err := service.runs.forget(root, store.SessionID); err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, detailFromSession(root, store, value))
}

func (service *sessionsService) serveCompact(writer http.ResponseWriter, request *http.Request) {
	compactor, ok := service.runner.(sessionCompactor)
	if !ok {
		writeAPIError(writer, http.StatusServiceUnavailable, app.ErrSessionRuntimeUnavailable)
		return
	}
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
	status, err := compactor.Compact(request.Context(), workspace.Store{Root: root}, request.PathValue("session"))
	if err != nil {
		writeSessionError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, sessionOperationResponse{Status: status})
}

func (service *sessionsService) serveLearning(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		value, err := (workspace.Store{Root: root}).LoadLearningConfig()
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, learningResponse{WorkspaceRoot: root, Enabled: !value.Disabled})
	case http.MethodPut:
		var input learningUpdateRequest
		if err := decodeSessionRequest(writer, request, &input); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		value := workspace.LearningConfig{Version: workspace.LearningConfigVersion, Disabled: input.Disabled}
		if err := (workspace.Store{Root: root}).SaveLearningConfig(value); err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, learningResponse{WorkspaceRoot: root, Enabled: !value.Disabled})
	default:
		writer.Header().Set("Allow", "GET, PUT")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
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
	run, err := service.runs.start(root, request.PathValue("session"), input.Content)
	if err != nil {
		if strings.Contains(err.Error(), "already has a running turn") {
			writeAPIError(writer, http.StatusConflict, err)
		} else {
			writeSessionError(writer, err)
		}
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusAccepted, run.snapshotCopy())
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
