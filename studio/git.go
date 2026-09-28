package studio

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/snowmerak/q/changes"
	"github.com/snowmerak/q/commitagent"
	"github.com/snowmerak/q/config"
)

const (
	commitSessionTTL = 30 * time.Minute
)

type changeListResponse struct {
	WorkspaceRoot string           `json:"workspace_root"`
	Snapshot      changes.Snapshot `json:"snapshot"`
}

type changeDetailResponse struct {
	WorkspaceRoot string         `json:"workspace_root"`
	Root          string         `json:"root"`
	File          changes.File   `json:"file"`
	Detail        changes.Detail `json:"detail"`
}

type commitService struct {
	parent   context.Context
	store    config.Store
	mu       sync.Mutex
	sessions map[string]*studioCommitSession
}

type studioCommitSession struct {
	id         string
	root       string
	headless   *commitagent.HeadlessSession
	createdAt  time.Time
	updatedAt  time.Time
	progressMu sync.Mutex
	progress   []commitagent.ProgressEvent
	operation  sync.Mutex
}

type commitPrepareRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
}

type commitProposalUpdate struct {
	Message string `json:"message"`
}

type commitExecuteRequest struct {
	Push bool `json:"push"`
}

type commitSessionResponse struct {
	ID         string                      `json:"id"`
	Root       string                      `json:"root"`
	Proposals  []commitagent.Proposal      `json:"proposals"`
	AutoStaged bool                        `json:"auto_staged"`
	Progress   []commitagent.ProgressEvent `json:"progress"`
	CreatedAt  time.Time                   `json:"created_at"`
	UpdatedAt  time.Time                   `json:"updated_at"`
}

type commitExecutionResponse struct {
	Status    string             `json:"status"`
	Result    commitagent.Result `json:"result"`
	PushError string             `json:"push_error,omitempty"`
}

func newCommitService(parent context.Context, store config.Store) *commitService {
	if parent == nil {
		parent = context.Background()
	}
	return &commitService{parent: parent, store: store, sessions: make(map[string]*studioCommitSession)}
}

func (service *commitService) serveChanges(writer http.ResponseWriter, request *http.Request) {
	root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	snapshot, err := changes.List(ctx, root)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(writer, http.StatusOK, changeListResponse{WorkspaceRoot: root, Snapshot: snapshot})
}

func (service *commitService) serveChangeDetail(writer http.ResponseWriter, request *http.Request) {
	root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	wanted := request.URL.Query().Get("path")
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	snapshot, err := changes.List(ctx, root)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	var selected changes.File
	found := false
	for _, file := range snapshot.Files {
		if file.Path == wanted {
			selected, found = file, true
			break
		}
	}
	if !found {
		writeAPIError(writer, http.StatusNotFound, errors.New("changed file does not exist; refresh the repository snapshot"))
		return
	}
	detail, err := changes.Read(ctx, snapshot.Root, selected)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(writer, http.StatusOK, changeDetailResponse{WorkspaceRoot: root, Root: snapshot.Root, File: selected, Detail: detail})
}

func (service *commitService) serveCommitCollection(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var input commitPrepareRequest
	if err := decodeGitRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	value, err := service.store.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	service.reapExpired()
	lifetime, cancel := context.WithCancel(service.parent)
	entry := &studioCommitSession{createdAt: time.Now(), updatedAt: time.Now()}
	headless, err := commitagent.PrepareHeadlessWithConfig(lifetime, root, nil, value, entry.appendProgress)
	if err != nil {
		cancel()
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if err := request.Context().Err(); err != nil {
		cancel()
		_ = headless.Close()
		return
	}
	id, err := randomCommitSessionID()
	if err != nil {
		cancel()
		_ = headless.Close()
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	entry.id, entry.root, entry.headless = id, headless.Root(), headless
	if entry.root == "" {
		entry.root = root
	}
	service.mu.Lock()
	service.sessions[id] = entry
	service.mu.Unlock()
	writeJSON(writer, http.StatusCreated, entry.snapshot())
}

func (service *commitService) serveCommitDetail(writer http.ResponseWriter, request *http.Request) {
	entry, found := service.session(request.PathValue("commit"))
	if !found {
		writeAPIError(writer, http.StatusNotFound, errors.New("commit review session does not exist or expired"))
		return
	}
	switch request.Method {
	case http.MethodGet:
		writeJSON(writer, http.StatusOK, entry.snapshot())
	case http.MethodDelete:
		service.remove(entry.id)
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.Header().Set("Allow", "GET, DELETE")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (service *commitService) serveCommitProposal(writer http.ResponseWriter, request *http.Request) {
	entry, found := service.session(request.PathValue("commit"))
	if !found {
		writeAPIError(writer, http.StatusNotFound, errors.New("commit review session does not exist or expired"))
		return
	}
	index, err := strconv.Atoi(request.PathValue("index"))
	if err != nil || index < 0 {
		writeAPIError(writer, http.StatusBadRequest, errors.New("proposal index is invalid"))
		return
	}
	var input commitProposalUpdate
	if err := decodeGitRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	entry.operation.Lock()
	defer entry.operation.Unlock()
	if err := entry.headless.UpdateProposal(index, input.Message); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	entry.touch()
	writeJSON(writer, http.StatusOK, entry.snapshot())
}

func (service *commitService) serveCommitRegenerate(writer http.ResponseWriter, request *http.Request) {
	entry, found := service.session(request.PathValue("commit"))
	if !found {
		writeAPIError(writer, http.StatusNotFound, errors.New("commit review session does not exist or expired"))
		return
	}
	entry.operation.Lock()
	defer entry.operation.Unlock()
	if err := entry.headless.Regenerate(request.Context()); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	entry.touch()
	writeJSON(writer, http.StatusOK, entry.snapshot())
}

func (service *commitService) serveCommitExecute(writer http.ResponseWriter, request *http.Request) {
	entry, found := service.session(request.PathValue("commit"))
	if !found {
		writeAPIError(writer, http.StatusNotFound, errors.New("commit review session does not exist or expired"))
		return
	}
	var input commitExecuteRequest
	if err := decodeGitRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	entry.operation.Lock()
	defer entry.operation.Unlock()
	result, err := entry.headless.Commit(request.Context())
	if err != nil {
		writeAPIError(writer, http.StatusConflict, err)
		return
	}
	response := commitExecutionResponse{Status: "committed", Result: result}
	if input.Push {
		if pushErr := entry.headless.Push(request.Context()); pushErr != nil {
			response.PushError = pushErr.Error()
		} else {
			response.Status = "pushed"
		}
	}
	service.remove(entry.id)
	writeJSON(writer, http.StatusOK, response)
}

func (service *commitService) session(id string) (*studioCommitSession, bool) {
	service.reapExpired()
	service.mu.Lock()
	defer service.mu.Unlock()
	entry, found := service.sessions[id]
	if found {
		entry.touch()
	}
	return entry, found
}

func (service *commitService) remove(id string) {
	service.mu.Lock()
	entry := service.sessions[id]
	delete(service.sessions, id)
	service.mu.Unlock()
	if entry != nil && entry.headless != nil {
		_ = entry.headless.Close()
	}
}

func (service *commitService) reapExpired() {
	cutoff := time.Now().Add(-commitSessionTTL)
	service.mu.Lock()
	var expired []*studioCommitSession
	for id, entry := range service.sessions {
		if entry.lastUpdated().Before(cutoff) {
			expired = append(expired, entry)
			delete(service.sessions, id)
		}
	}
	service.mu.Unlock()
	for _, entry := range expired {
		_ = entry.headless.Close()
	}
}

func (service *commitService) Close() error {
	service.mu.Lock()
	entries := make([]*studioCommitSession, 0, len(service.sessions))
	for id, entry := range service.sessions {
		entries = append(entries, entry)
		delete(service.sessions, id)
	}
	service.mu.Unlock()
	var result error
	for _, entry := range entries {
		result = errors.Join(result, entry.headless.Close())
	}
	return result
}

func (entry *studioCommitSession) appendProgress(event commitagent.ProgressEvent) {
	entry.progressMu.Lock()
	entry.progress = append(entry.progress, event)
	if len(entry.progress) > 100 {
		entry.progress = append([]commitagent.ProgressEvent(nil), entry.progress[len(entry.progress)-100:]...)
	}
	entry.updatedAt = time.Now()
	entry.progressMu.Unlock()
}

func (entry *studioCommitSession) touch() {
	entry.progressMu.Lock()
	entry.updatedAt = time.Now()
	entry.progressMu.Unlock()
}

func (entry *studioCommitSession) lastUpdated() time.Time {
	entry.progressMu.Lock()
	defer entry.progressMu.Unlock()
	return entry.updatedAt
}

func (entry *studioCommitSession) snapshot() commitSessionResponse {
	entry.progressMu.Lock()
	progress := append([]commitagent.ProgressEvent(nil), entry.progress...)
	createdAt, updatedAt := entry.createdAt, entry.updatedAt
	entry.progressMu.Unlock()
	return commitSessionResponse{
		ID: entry.id, Root: entry.root, Proposals: entry.headless.Proposals(), AutoStaged: entry.headless.AutoStaged(),
		Progress: progress, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func randomCommitSessionID() (string, error) {
	var body [16]byte
	if _, err := rand.Read(body[:]); err != nil {
		return "", fmt.Errorf("create commit review ID: %w", err)
	}
	return hex.EncodeToString(body[:]), nil
}

func decodeGitRequest(writer http.ResponseWriter, request *http.Request, output any) error {
	return decodeIntegrationRequest(writer, request, output)
}
