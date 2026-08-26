package workspacememory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/worklock"
)

const maximumRequestBody = 256 << 20

var (
	ErrWorkspaceClosed = errors.New("workspacememory: workspace handle is closed")
	ErrLeaseExpired    = errors.New("workspacememory: workspace lease expired")
	ErrUnauthorized    = errors.New("workspacememory: authentication failed")
	ErrVectorConflict  = errors.New("workspacememory: workspace is open with a different vector configuration")
)

type invalidRequestError struct{ err error }

func (e *invalidRequestError) Error() string { return e.err.Error() }
func (e *invalidRequestError) Unwrap() error { return e.err }

type openWorkspaceRequest struct {
	Root    string                    `json:"root"`
	Vector  sessionstore.VectorConfig `json:"vector"`
	LeaseID string                    `json:"lease_id,omitempty"`
}

type OpenWorkspaceResponse struct {
	WorkspaceID string                    `json:"workspace_id"`
	LeaseID     string                    `json:"lease_id"`
	Root        string                    `json:"root"`
	Vector      sessionstore.VectorConfig `json:"vector"`
	LeaseTTLMS  int64                     `json:"lease_ttl_ms"`
}

type saveRequest struct {
	Record sessionstore.Record `json:"record"`
}

type saveResponse struct {
	Record sessionstore.Record `json:"record"`
	Error  *storeError         `json:"error,omitempty"`
}

type saveBatchRequest struct {
	Records []sessionstore.Record `json:"records"`
}

type saveBatchResponse struct {
	Records []sessionstore.Record `json:"records"`
	Error   *storeError           `json:"error,omitempty"`
}

type storeError struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	RecordID string `json:"record_id,omitempty"`
}

type recordIDRequest struct {
	ID string `json:"id"`
}

type getResponse struct {
	Record sessionstore.Record `json:"record"`
}

type searchRequest struct {
	Options sessionstore.SearchOptions `json:"options"`
}

type searchResponse struct {
	Result sessionstore.SearchResult `json:"result"`
}

type configureVectorRequest struct {
	Vector sessionstore.VectorConfig `json:"vector"`
}

type configureVectorResponse struct {
	Vector sessionstore.VectorConfig `json:"vector"`
}

type workspaceLease struct {
	expiresAt time.Time
}

type workspaceEntry struct {
	id        string
	key       string
	root      string
	store     workspaceStore
	lock      *worklock.Lock
	leases    map[string]workspaceLease
	operation sync.RWMutex
}

type workspaceStore interface {
	Save(sessionstore.Record) (sessionstore.Record, error)
	SaveBatch([]sessionstore.Record) ([]sessionstore.Record, error)
	Get(string) (sessionstore.Record, error)
	Delete(string) error
	Search(context.Context, sessionstore.SearchOptions) (sessionstore.SearchResult, error)
	ConfigureVector(sessionstore.VectorConfig) error
	VectorConfig() sessionstore.VectorConfig
	Close() error
}

type workspaceManager struct {
	mu       sync.Mutex
	entries  map[string]*workspaceEntry
	byKey    map[string]*workspaceEntry
	leaseTTL time.Duration
	closed   bool
}

func newWorkspaceManager(leaseTTL time.Duration) *workspaceManager {
	return &workspaceManager{
		entries: make(map[string]*workspaceEntry), byKey: make(map[string]*workspaceEntry),
		leaseTTL: leaseTTL,
	}
}

func (m *workspaceManager) open(request openWorkspaceRequest) (OpenWorkspaceResponse, error) {
	root, key, err := canonicalWorkspaceRoot(request.Root)
	if err != nil {
		return OpenWorkspaceResponse{}, &invalidRequestError{err: err}
	}
	leaseID := strings.TrimSpace(request.LeaseID)
	if leaseID == "" {
		leaseID, err = randomHexID()
		if err != nil {
			return OpenWorkspaceResponse{}, err
		}
	} else if !validHexID(leaseID) {
		return OpenWorkspaceResponse{}, &invalidRequestError{err: errors.New("workspacememory: lease ID must be 32 lowercase hexadecimal characters")}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return OpenWorkspaceResponse{}, errors.New("workspacememory: service is closed")
	}
	now := time.Now()
	existing := m.byKey[key]
	if existing == nil {
		for _, candidate := range m.entries {
			if sameWorkspaceRoot(candidate.root, root) {
				existing = candidate
				key = candidate.key
				break
			}
		}
	}
	if existing != nil {
		current := existing.store.VectorConfig()
		if !compatibleVectorConfig(current, request.Vector) {
			return OpenWorkspaceResponse{}, ErrVectorConflict
		}
		existing.leases[leaseID] = workspaceLease{expiresAt: now.Add(m.leaseTTL)}
		return existing.response(leaseID, m.leaseTTL), nil
	}

	id := workspaceID(key)
	if collision := m.entries[id]; collision != nil && collision.key != key {
		return OpenWorkspaceResponse{}, errors.New("workspacememory: workspace identifier collision")
	}
	lock, err := worklock.AcquireFile(root, WorkspaceLockFileName, "q workspace memory")
	if err != nil {
		return OpenWorkspaceResponse{}, fmt.Errorf("workspacememory: acquire workspace memory ownership: %w", err)
	}
	store, err := sessionstore.OpenWithOptions(root, sessionstore.OpenOptions{
		WorkspaceLock: lock,
		Vector:        request.Vector,
	})
	if err != nil {
		_ = lock.Close()
		return OpenWorkspaceResponse{}, fmt.Errorf("workspacememory: open workspace Store: %w", err)
	}
	entry := &workspaceEntry{
		id: id, key: key, root: root, store: store, lock: lock,
		leases: map[string]workspaceLease{leaseID: {expiresAt: now.Add(m.leaseTTL)}},
	}
	m.entries[id] = entry
	m.byKey[key] = entry
	return entry.response(leaseID, m.leaseTTL), nil
}

func (e *workspaceEntry) response(leaseID string, ttl time.Duration) OpenWorkspaceResponse {
	return OpenWorkspaceResponse{
		WorkspaceID: e.id, LeaseID: leaseID, Root: e.root,
		Vector: e.store.VectorConfig(), LeaseTTLMS: ttl.Milliseconds(),
	}
}

func (m *workspaceManager) acquire(id, leaseID string) (*workspaceEntry, func(), error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, errors.New("workspacememory: service is closed")
	}
	entry := m.entries[strings.TrimSpace(id)]
	if entry == nil {
		m.mu.Unlock()
		return nil, nil, ErrWorkspaceClosed
	}
	lease, ok := entry.leases[strings.TrimSpace(leaseID)]
	if !ok || !lease.expiresAt.After(time.Now()) {
		if ok {
			delete(entry.leases, strings.TrimSpace(leaseID))
		}
		if len(entry.leases) == 0 {
			m.removeAndCloseLocked(entry)
		}
		m.mu.Unlock()
		return nil, nil, ErrLeaseExpired
	}
	lease.expiresAt = time.Now().Add(m.leaseTTL)
	entry.leases[strings.TrimSpace(leaseID)] = lease
	entry.operation.RLock()
	m.mu.Unlock()
	return entry, entry.operation.RUnlock, nil
}

func (m *workspaceManager) renew(id, leaseID string) error {
	entry, release, err := m.acquire(id, leaseID)
	if err != nil {
		return err
	}
	_ = entry
	release()
	return nil
}

func (m *workspaceManager) configureVector(id, leaseID string, vector sessionstore.VectorConfig) (sessionstore.VectorConfig, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return sessionstore.VectorConfig{}, errors.New("workspacememory: service is closed")
	}
	entry := m.entries[strings.TrimSpace(id)]
	if entry == nil {
		m.mu.Unlock()
		return sessionstore.VectorConfig{}, ErrWorkspaceClosed
	}
	lease, ok := entry.leases[strings.TrimSpace(leaseID)]
	if !ok || !lease.expiresAt.After(time.Now()) {
		if ok {
			delete(entry.leases, strings.TrimSpace(leaseID))
		}
		if len(entry.leases) == 0 {
			_ = m.removeAndCloseLocked(entry)
		}
		m.mu.Unlock()
		return sessionstore.VectorConfig{}, ErrLeaseExpired
	}
	current := entry.store.VectorConfig()
	if len(entry.leases) > 1 && !compatibleVectorConfig(current, vector) {
		m.mu.Unlock()
		return sessionstore.VectorConfig{}, ErrVectorConflict
	}
	lease.expiresAt = time.Now().Add(m.leaseTTL)
	entry.leases[strings.TrimSpace(leaseID)] = lease
	entry.operation.RLock()
	m.mu.Unlock()
	defer entry.operation.RUnlock()
	if err := entry.store.ConfigureVector(vector); err != nil {
		return sessionstore.VectorConfig{}, err
	}
	return entry.store.VectorConfig(), nil
}

func (m *workspaceManager) release(id, leaseID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[strings.TrimSpace(id)]
	if entry == nil {
		return nil
	}
	delete(entry.leases, strings.TrimSpace(leaseID))
	if len(entry.leases) != 0 {
		return nil
	}
	return m.removeAndCloseLocked(entry)
}

func (m *workspaceManager) sweepExpired(now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result error
	for _, entry := range m.entries {
		for leaseID, lease := range entry.leases {
			if !lease.expiresAt.After(now) {
				delete(entry.leases, leaseID)
			}
		}
		if len(entry.leases) == 0 {
			result = errors.Join(result, m.removeAndCloseLocked(entry))
		}
	}
	return result
}

func (m *workspaceManager) removeAndCloseLocked(entry *workspaceEntry) error {
	delete(m.entries, entry.id)
	delete(m.byKey, entry.key)
	entry.operation.Lock()
	err := errors.Join(entry.store.Close(), entry.lock.Close())
	entry.operation.Unlock()
	return err
}

func (m *workspaceManager) close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	entries := make([]*workspaceEntry, 0, len(m.entries))
	for _, entry := range m.entries {
		entries = append(entries, entry)
	}
	m.entries = make(map[string]*workspaceEntry)
	m.byKey = make(map[string]*workspaceEntry)
	m.mu.Unlock()
	var result error
	for _, entry := range entries {
		entry.operation.Lock()
		result = errors.Join(result, entry.store.Close(), entry.lock.Close())
		entry.operation.Unlock()
	}
	return result
}

func (m *workspaceManager) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

func compatibleVectorConfig(current, requested sessionstore.VectorConfig) bool {
	if current.Model != requested.Model || current.Dimensions != requested.Dimensions {
		return false
	}
	return (requested.M == 0 || current.M == requested.M) &&
		(requested.EfConstruction == 0 || current.EfConstruction == requested.EfConstruction) &&
		(requested.EfSearch == 0 || current.EfSearch == requested.EfSearch)
}

func canonicalWorkspaceRoot(root string) (string, string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", "", errors.New("workspacememory: workspace root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("workspacememory: resolve workspace root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", "", fmt.Errorf("workspacememory: canonicalize workspace root: %w", err)
	}
	canonical = filepath.Clean(canonical)
	info, err := os.Stat(canonical)
	if err != nil {
		return "", "", fmt.Errorf("workspacememory: inspect workspace root: %w", err)
	}
	if !info.IsDir() {
		return "", "", errors.New("workspacememory: workspace root is not a directory")
	}
	return canonical, canonical, nil
}

func sameWorkspaceRoot(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func workspaceID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

func randomHexID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("workspacememory: generate ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func validHexID(value string) bool {
	if len(value) != 32 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

type Status struct {
	Health
	OpenWorkspaces int `json:"open_workspaces"`
}

func newHandler(health Health, credential string, manager *workspaceManager) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, health)
	})
	authenticated := func(handler http.HandlerFunc) http.Handler {
		return authenticate(credential, handler)
	}
	mux.Handle("GET /v1/status", authenticated(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, Status{Health: health, OpenWorkspaces: manager.count()})
	}))
	mux.Handle("POST /v1/workspaces/open", authenticated(func(writer http.ResponseWriter, request *http.Request) {
		var input openWorkspaceRequest
		if err := decodeJSON(writer, request, &input); err != nil {
			writeServiceError(writer, err)
			return
		}
		output, err := manager.open(input)
		if err != nil {
			writeServiceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, output)
	}))
	mux.Handle("POST /v1/workspaces/{workspace}/leases/{lease}/renew", authenticated(func(writer http.ResponseWriter, request *http.Request) {
		if err := manager.renew(request.PathValue("workspace"), request.PathValue("lease")); err != nil {
			writeServiceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, struct{}{})
	}))
	mux.Handle("DELETE /v1/workspaces/{workspace}/leases/{lease}", authenticated(func(writer http.ResponseWriter, request *http.Request) {
		if err := manager.release(request.PathValue("workspace"), request.PathValue("lease")); err != nil {
			writeServiceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, struct{}{})
	}))

	withWorkspace := func(handler func(http.ResponseWriter, *http.Request, *workspaceEntry)) http.Handler {
		return authenticated(func(writer http.ResponseWriter, request *http.Request) {
			entry, release, err := manager.acquire(request.PathValue("workspace"), request.Header.Get(leaseHeader))
			if err != nil {
				writeServiceError(writer, err)
				return
			}
			defer release()
			handler(writer, request, entry)
		})
	}
	mux.Handle("POST /v1/workspaces/{workspace}/records/save", withWorkspace(func(writer http.ResponseWriter, request *http.Request, entry *workspaceEntry) {
		var input saveRequest
		if err := decodeJSON(writer, request, &input); err != nil {
			writeServiceError(writer, err)
			return
		}
		record, err := entry.store.Save(input.Record)
		if err != nil {
			// Save may cross the source-record durability boundary before a
			// derived index fails. Always return the prepared record with the
			// structured error so a Writer can retry its stable server-assigned ID.
			writeJSON(writer, http.StatusOK, saveResponse{Record: record, Error: encodeStoreError(err)})
			return
		}
		writeJSON(writer, http.StatusOK, saveResponse{Record: record})
	}))
	mux.Handle("POST /v1/workspaces/{workspace}/records/save-batch", withWorkspace(func(writer http.ResponseWriter, request *http.Request, entry *workspaceEntry) {
		var input saveBatchRequest
		if err := decodeJSON(writer, request, &input); err != nil {
			writeServiceError(writer, err)
			return
		}
		records, err := entry.store.SaveBatch(input.Records)
		if err != nil {
			writeJSON(writer, http.StatusOK, saveBatchResponse{Records: records, Error: encodeStoreError(err)})
			return
		}
		writeJSON(writer, http.StatusOK, saveBatchResponse{Records: records})
	}))
	mux.Handle("POST /v1/workspaces/{workspace}/records/get", withWorkspace(func(writer http.ResponseWriter, request *http.Request, entry *workspaceEntry) {
		var input recordIDRequest
		if err := decodeJSON(writer, request, &input); err != nil {
			writeServiceError(writer, err)
			return
		}
		record, err := entry.store.Get(input.ID)
		if err != nil {
			writeServiceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, getResponse{Record: record})
	}))
	mux.Handle("POST /v1/workspaces/{workspace}/records/delete", withWorkspace(func(writer http.ResponseWriter, request *http.Request, entry *workspaceEntry) {
		var input recordIDRequest
		if err := decodeJSON(writer, request, &input); err != nil {
			writeServiceError(writer, err)
			return
		}
		if err := entry.store.Delete(input.ID); err != nil {
			writeServiceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, struct{}{})
	}))
	mux.Handle("POST /v1/workspaces/{workspace}/search", withWorkspace(func(writer http.ResponseWriter, request *http.Request, entry *workspaceEntry) {
		var input searchRequest
		if err := decodeJSON(writer, request, &input); err != nil {
			writeServiceError(writer, err)
			return
		}
		result, err := entry.store.Search(request.Context(), input.Options)
		if err != nil {
			writeServiceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, searchResponse{Result: result})
	}))
	mux.Handle("POST /v1/workspaces/{workspace}/vector/configure", authenticated(func(writer http.ResponseWriter, request *http.Request) {
		var input configureVectorRequest
		if err := decodeJSON(writer, request, &input); err != nil {
			writeServiceError(writer, err)
			return
		}
		vector, err := manager.configureVector(
			request.PathValue("workspace"), request.Header.Get(leaseHeader), input.Vector,
		)
		if err != nil {
			writeServiceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, configureVectorResponse{Vector: vector})
	}))
	mux.Handle("POST /v1/workspaces/{workspace}/flush", withWorkspace(func(writer http.ResponseWriter, _ *http.Request, _ *workspaceEntry) {
		// Store writes are synchronous; this endpoint is a durability barrier for
		// remote Writer implementations and intentionally has no extra work.
		writeJSON(writer, http.StatusOK, struct{}{})
	}))
	return mux
}

func authenticate(credential string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		scheme, presented, found := strings.Cut(request.Header.Get("Authorization"), " ")
		valid := found && strings.EqualFold(scheme, "Bearer") &&
			subtle.ConstantTimeCompare([]byte(presented), []byte(credential)) == 1
		if !valid {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writeError(writer, http.StatusUnauthorized, "unauthorized", ErrUnauthorized.Error())
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, output any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumRequestBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return &invalidRequestError{err: fmt.Errorf("workspacememory: decode request: %w", err)}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return &invalidRequestError{err: errors.New("workspacememory: request contains multiple JSON values")}
		}
		return &invalidRequestError{err: fmt.Errorf("workspacememory: decode request: %w", err)}
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeServiceError(writer http.ResponseWriter, err error) {
	var invalidRequest *invalidRequestError
	switch {
	case errors.Is(err, sessionstore.ErrNotFound):
		writeError(writer, http.StatusNotFound, "record_not_found", err.Error())
	case errors.Is(err, ErrWorkspaceClosed):
		writeError(writer, http.StatusNotFound, "workspace_closed", err.Error())
	case errors.Is(err, ErrLeaseExpired):
		writeError(writer, http.StatusGone, "lease_expired", err.Error())
	case errors.Is(err, ErrVectorConflict):
		writeError(writer, http.StatusConflict, "vector_conflict", err.Error())
	case errors.Is(err, sessionstore.ErrIndexLocked):
		writeError(writer, http.StatusLocked, "index_locked", err.Error())
	case errors.Is(err, worklock.ErrLocked):
		writeError(writer, http.StatusLocked, "workspace_locked", err.Error())
	case errors.As(err, &invalidRequest):
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		// Request decoding and validation failures have no useful typed marker.
		// Session Store indexing failures remain server errors because the source
		// record may already have crossed its durability boundary.
		var indexing *sessionstore.IndexingError
		if errors.As(err, &indexing) {
			writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": encodeStoreError(err)})
			return
		}
		writeError(writer, http.StatusInternalServerError, "store_error", err.Error())
	}
}

func encodeStoreError(err error) *storeError {
	if err == nil {
		return nil
	}
	var indexing *sessionstore.IndexingError
	if errors.As(err, &indexing) {
		message := indexing.Error()
		if indexing.Err != nil {
			message = indexing.Err.Error()
		}
		return &storeError{Code: "indexing_error", Message: message, RecordID: indexing.RecordID}
	}
	return &storeError{Code: "store_error", Message: err.Error()}
}

type leader struct {
	health       Health
	server       *http.Server
	manager      *workspaceManager
	lock         *worklock.Lock
	cancel       context.CancelFunc
	done         chan struct{}
	shutdownDone chan struct{}

	closeOnce sync.Once
	errMu     sync.Mutex
	err       error
}

func startLeader(parent context.Context, options EnsureOptions, listener net.Listener, serviceLock *worklock.Lock, credential string) (*leader, error) {
	generation, err := randomHexID()
	if err != nil {
		return nil, err
	}
	health := Health{
		Service: ServiceName, ProtocolVersion: ProtocolVersion,
		Implementation: Implementation, Generation: generation, Ready: true,
	}
	manager := newWorkspaceManager(options.effectiveLeaseTTL())
	ctx, cancel := context.WithCancel(parent)
	leader := &leader{
		health: health, manager: manager, lock: serviceLock, cancel: cancel,
		done: make(chan struct{}), shutdownDone: make(chan struct{}),
		server: &http.Server{Handler: newHandler(health, credential, manager), ReadHeaderTimeout: 10 * time.Second},
	}
	sweepInterval := options.effectiveSweepInterval()
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				_ = manager.sweepExpired(now)
			}
		}
	}()
	go func() {
		serveErr := leader.server.Serve(listener)
		cancel()
		<-sweepDone
		<-leader.shutdownDone
		managerErr := manager.close()
		lockErr := leader.lock.Close()
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		leader.errMu.Lock()
		leader.err = errors.Join(leader.err, serveErr, managerErr, lockErr)
		leader.errMu.Unlock()
		close(leader.done)
	}()
	go func() {
		defer close(leader.shutdownDone)
		<-ctx.Done()
		shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if shutdownErr := leader.server.Shutdown(shutdown); shutdownErr != nil {
			forceErr := leader.server.Close()
			leader.errMu.Lock()
			leader.err = errors.Join(leader.err, fmt.Errorf("workspacememory: graceful shutdown: %w", shutdownErr), forceErr)
			leader.errMu.Unlock()
		}
	}()
	return leader, nil
}

func (l *leader) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() { l.cancel() })
	<-l.done
	l.errMu.Lock()
	defer l.errMu.Unlock()
	return l.err
}

func (l *leader) Done() <-chan struct{} { return l.done }
