package remoteapi

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/remoteconfig"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

const maximumRequestBytes = 256 << 10

//go:embed openapi.json
var openAPIDocument []byte

type Handler struct {
	host          Host
	authenticator *remoteconfig.Authenticator
	admission     chan struct{}
}

type Host interface {
	ListSubagents(string) ([]app.RemoteSubagentInfo, []app.RemoteSubagentIssue, error)
	Run(context.Context, workspace.Store, string, string, string, app.RemoteEventSink) error
}

func NewHandler(host Host, authenticator *remoteconfig.Authenticator, maximumParallel int) http.Handler {
	if maximumParallel <= 0 {
		maximumParallel = 1
	}
	return &Handler{host: host, authenticator: authenticator, admission: make(chan struct{}, maximumParallel)}
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/v1/health" {
		h.serveHealth(writer, request)
		return
	}
	if request.URL.Path == "/openapi.json" {
		h.serveOpenAPI(writer, request)
		return
	}
	if h.authenticator != nil && !h.authenticator.Authorized(request.Header.Get("Authorization")) {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(writer, http.StatusUnauthorized, "invalid_api_key", "a valid Remote API key is required")
		return
	}
	switch request.URL.Path {
	case "/v1/sessions":
		h.serveSessions(writer, request)
	case "/v1/subagents":
		h.serveSubagents(writer, request)
	case "/v1/subagent-runs":
		h.serveRun(writer, request)
	default:
		writeProblem(writer, http.StatusNotFound, "not_found", "the requested endpoint was not found")
	}
}

func (h *Handler) serveHealth(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"service": "q-remote", "version": 1, "accepting_runs": len(h.admission) < cap(h.admission),
	})
}

func (h *Handler) serveOpenAPI(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(openAPIDocument)
}

func (h *Handler) serveSessions(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	root, err := canonicalDirectory(request.URL.Query().Get("working_directory"))
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "working_directory must identify an accessible directory")
		return
	}
	entries, err := (workspace.Store{Root: root}).ListSessions()
	if err != nil {
		writeProblem(writer, http.StatusInternalServerError, "internal_error", "could not list workspace sessions")
		return
	}
	type sessionResponse struct {
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id,omitempty"`
		Title     string `json:"title,omitempty"`
		UpdatedAt string `json:"updated_at"`
	}
	sessions := make([]sessionResponse, 0, len(entries))
	for _, entry := range entries {
		sessions = append(sessions, sessionResponse{
			SessionID: entry.Store.SessionID, RunID: entry.RunID, Title: entry.Title,
			UpdatedAt: entry.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"working_directory": root, "sessions": sessions})
}

func (h *Handler) serveSubagents(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	root, err := canonicalDirectory(request.URL.Query().Get("working_directory"))
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "working_directory must identify an accessible directory")
		return
	}
	subagents, issues, err := h.host.ListSubagents(root)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "subagent_unavailable", "subagent discovery is unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"working_directory": root, "subagents": subagents, "issues": issues})
}

type runRequest struct {
	WorkingDirectory string `json:"working_directory"`
	SessionID        string `json:"session_id,omitempty"`
	Subagent         string `json:"subagent,omitempty"`
	Prompt           string `json:"prompt"`
}

func (h *Handler) serveRun(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "Content-Type must be application/json")
		return
	}
	select {
	case h.admission <- struct{}{}:
		defer func() { <-h.admission }()
	default:
		writeProblem(writer, http.StatusTooManyRequests, "remote_capacity", "q remote is at its active run limit")
		return
	}

	var input runRequest
	request.Body = http.MaxBytesReader(writer, request.Body, maximumRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(writer, http.StatusRequestEntityTooLarge, "request_too_large", "the request body is too large")
		} else {
			writeProblem(writer, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
		}
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "the request body must contain one JSON value")
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.Subagent = strings.TrimSpace(input.Subagent)
	input.SessionID = strings.TrimSpace(input.SessionID)
	if input.Prompt == "" || input.WorkingDirectory == "" {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "working_directory and prompt are required")
		return
	}
	if len(input.Prompt) > subagent.MaximumDelegatePromptBytes {
		writeProblem(writer, http.StatusRequestEntityTooLarge, "request_too_large", fmt.Sprintf("prompt must not exceed %d bytes", subagent.MaximumDelegatePromptBytes))
		return
	}
	root, err := canonicalDirectory(input.WorkingDirectory)
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "working_directory must identify an accessible directory")
		return
	}
	if input.SessionID != "" {
		if _, err := (workspace.Store{Root: root}).ForSession(input.SessionID); err != nil {
			writeProblem(writer, http.StatusBadRequest, "invalid_request", "session_id is invalid")
			return
		}
	}

	flusher, canFlush := writer.(http.Flusher)
	started := false
	terminalWritten := false
	emit := func(event app.RemoteEvent) error {
		if !started {
			if event.Type != "session" {
				return errors.New("remote run did not begin with a session event")
			}
			writer.Header().Set("Content-Type", "application/x-ndjson")
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("X-Q-Session-ID", event.SessionID)
			writer.WriteHeader(http.StatusOK)
			started = true
		}
		if err := json.NewEncoder(writer).Encode(event); err != nil {
			return err
		}
		if event.Type == "result" || event.Type == "cancelled" || event.Type == "error" {
			terminalWritten = true
		}
		if canFlush {
			flusher.Flush()
		}
		return nil
	}

	runErr := h.host.Run(request.Context(), workspace.Store{Root: root}, input.SessionID, input.Subagent, input.Prompt, emit)
	if runErr == nil {
		return
	}
	if terminalWritten {
		return
	}
	if !started {
		status, code, message := classifyPreStreamError(runErr)
		writeProblem(writer, status, code, message)
		return
	}
	if request.Context().Err() != nil {
		return
	}
	terminal := map[string]any{"type": "error", "error": map[string]any{"code": "internal_error", "message": "the remote run failed"}}
	if errors.Is(runErr, context.Canceled) {
		terminal = map[string]any{"type": "cancelled"}
	}
	_ = json.NewEncoder(writer).Encode(terminal)
	if canFlush {
		flusher.Flush()
	}
}

func canonicalDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("working directory is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", errors.New("working directory is not a directory")
	}
	return canonical, nil
}

func classifyPreStreamError(err error) (int, string, string) {
	switch {
	case errors.Is(err, workspace.ErrLocked):
		return http.StatusConflict, "session_busy", "the selected session is already in use"
	case errors.Is(err, workspace.ErrNotFound):
		return http.StatusNotFound, "session_not_found", "the selected session does not exist"
	}
	var remoteErr *app.RemoteRunError
	if errors.As(err, &remoteErr) {
		switch remoteErr.Kind {
		case app.RemoteSubagentNotFound:
			return http.StatusNotFound, "subagent_not_found", "the selected subagent does not exist"
		case app.RemoteUnavailable:
			return http.StatusServiceUnavailable, "subagent_unavailable", "the requested agent runtime is unavailable"
		}
	}
	if errors.Is(err, context.Canceled) {
		return 499, "cancelled", "the request was cancelled"
	}
	return http.StatusInternalServerError, "internal_error", "the remote request failed"
}

func methodNotAllowed(writer http.ResponseWriter, allowed string) {
	writer.Header().Set("Allow", allowed)
	writeProblem(writer, http.StatusMethodNotAllowed, "method_not_allowed", "the request method is not allowed")
}

func writeProblem(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
