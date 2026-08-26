package workspacememory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/worklock"
)

const (
	ServiceName     = "q-workspace-memory"
	ProtocolVersion = 1
	Implementation  = "0.1.0"
	leaseHeader     = "X-Q-Workspace-Lease"
)

type Health struct {
	Service         string `json:"service"`
	ProtocolVersion int    `json:"protocol_version"`
	Implementation  string `json:"implementation"`
	Generation      string `json:"generation"`
	Ready           bool   `json:"ready"`
}

func (h Health) Compatible() bool {
	return h.Service == ServiceName && h.ProtocolVersion == ProtocolVersion && h.Ready
}

// HTTPError reports a structured Workspace Memory API failure. Unwrap maps
// protocol error codes back to local sentinels used by existing q stores.
type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
	RecordID   string
}

func (e *HTTPError) Error() string {
	if e == nil {
		return "workspacememory: HTTP request failed"
	}
	if e.Message != "" {
		return fmt.Sprintf("workspacememory: HTTP %d %s: %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("workspacememory: HTTP %d %s", e.StatusCode, e.Code)
}

func (e *HTTPError) Unwrap() error {
	if e == nil {
		return nil
	}
	switch e.Code {
	case "record_not_found":
		return sessionstore.ErrNotFound
	case "workspace_closed":
		return ErrWorkspaceClosed
	case "lease_expired":
		return ErrLeaseExpired
	case "unauthorized":
		return ErrUnauthorized
	case "vector_conflict":
		return ErrVectorConflict
	case "workspace_locked":
		return worklock.ErrLocked
	case "index_locked":
		return sessionstore.ErrIndexLocked
	case "indexing_error":
		return &sessionstore.IndexingError{RecordID: e.RecordID, Err: errors.New(e.Message)}
	default:
		return nil
	}
}

type Client struct {
	endpoint       string
	credential     string
	http           *http.Client
	requestTimeout time.Duration
}

func NewClient(endpoint, credential string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"), credential: strings.TrimSpace(credential),
		http: &http.Client{Timeout: timeout}, requestTimeout: timeout,
	}
}

func (c *Client) Endpoint() string {
	if c == nil {
		return ""
	}
	return c.endpoint
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	var output Health
	if err := c.doJSON(ctx, http.MethodGet, "/health", nil, &output, "", false); err != nil {
		return Health{}, err
	}
	return output, nil
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var output Status
	if err := c.doJSON(ctx, http.MethodGet, "/status", nil, &output, "", true); err != nil {
		return Status{}, err
	}
	return output, nil
}

// OpenWorkspace obtains an idempotency-keyed lease on a canonical workspace
// Store. Repeated transport attempts reuse the generated lease ID, while two
// explicit calls receive independent leases on the same underlying Store.
func (c *Client) OpenWorkspace(ctx context.Context, root string, vector sessionstore.VectorConfig) (*Workspace, error) {
	if c == nil {
		return nil, errors.New("workspacememory: client is nil")
	}
	if ctx == nil {
		return nil, errors.New("workspacememory: request context is nil")
	}
	leaseID, err := randomHexID()
	if err != nil {
		return nil, err
	}
	input := openWorkspaceRequest{Root: root, Vector: vector, LeaseID: leaseID}
	var output OpenWorkspaceResponse
	if err := c.openWorkspace(ctx, input, &output); err != nil {
		return nil, err
	}
	if output.WorkspaceID == "" || output.LeaseID != leaseID || output.Root == "" || output.LeaseTTLMS <= 0 {
		return nil, errors.New("workspacememory: server returned an invalid workspace lease")
	}
	heartbeatContext, cancel := context.WithCancel(context.Background())
	workspace := &Workspace{
		client: c, id: output.WorkspaceID, leaseID: output.LeaseID, root: output.Root,
		vector: output.Vector, leaseTTL: time.Duration(output.LeaseTTLMS) * time.Millisecond,
		heartbeatCancel: cancel, heartbeatDone: make(chan struct{}), closeDone: make(chan struct{}),
	}
	go workspace.heartbeat(heartbeatContext)
	return workspace, nil
}

func (c *Client) openWorkspace(ctx context.Context, input openWorkspaceRequest, output *OpenWorkspaceResponse) error {
	openContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	delay := 20 * time.Millisecond
	for {
		err := c.doJSON(openContext, http.MethodPost, "/workspaces/open", input, output, "", true)
		if err == nil {
			return nil
		}
		if !reconnectableWorkspaceError(err) {
			return err
		}
		select {
		case <-openContext.Done():
			return errors.Join(err, openContext.Err())
		case <-time.After(delay):
		}
		delay = min(delay*2, 500*time.Millisecond)
	}
}

// Open is a concise alias for OpenWorkspace.
func (c *Client) Open(ctx context.Context, root string, vector sessionstore.VectorConfig) (*Workspace, error) {
	return c.OpenWorkspace(ctx, root, vector)
}

func (c *Client) doJSON(ctx context.Context, method, path string, input, output any, leaseID string, authenticated bool) error {
	if c == nil {
		return errors.New("workspacememory: client is nil")
	}
	if ctx == nil {
		return errors.New("workspacememory: request context is nil")
	}
	var body *bytes.Reader
	if input == nil {
		body = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("workspacememory: encode %s: %w", path, err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+c.credential)
	}
	if leaseID != "" {
		request.Header.Set(leaseHeader, leaseID)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code     string `json:"code"`
				Message  string `json:"message"`
				RecordID string `json:"record_id,omitempty"`
			} `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&envelope)
		return &HTTPError{
			StatusCode: response.StatusCode, Code: envelope.Error.Code,
			Message: envelope.Error.Message, RecordID: envelope.Error.RecordID,
		}
	}
	if output == nil {
		return nil
	}
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("workspacememory: decode %s: %w", path, err)
	}
	return nil
}

// Workspace is a leased remote Store. Its local-signature methods satisfy
// sessionstore.WriterStore, archiveembed.Store, and agentskills.RecordStore.
type Workspace struct {
	client   *Client
	id       string
	leaseID  string
	root     string
	leaseTTL time.Duration

	operation sync.RWMutex
	closed    bool
	vectorMu  sync.RWMutex
	vector    sessionstore.VectorConfig

	heartbeatCancel context.CancelFunc
	heartbeatDone   chan struct{}
	reconnect       sync.Mutex
	closeOnce       sync.Once
	closeDone       chan struct{}
	closeErr        error
}

func (w *Workspace) ID() string {
	if w == nil {
		return ""
	}
	return w.id
}

func (w *Workspace) Root() string {
	if w == nil {
		return ""
	}
	return w.root
}

func (w *Workspace) VectorConfig() sessionstore.VectorConfig {
	if w == nil {
		return sessionstore.VectorConfig{}
	}
	w.vectorMu.RLock()
	defer w.vectorMu.RUnlock()
	return w.vector
}

func (w *Workspace) Save(record sessionstore.Record) (sessionstore.Record, error) {
	return w.SaveContext(context.Background(), record)
}

func (w *Workspace) SaveContext(ctx context.Context, record sessionstore.Record) (sessionstore.Record, error) {
	stable, err := withStableRecordID(record)
	if err != nil {
		return sessionstore.Record{}, err
	}
	var output saveResponse
	if err := w.call(ctx, http.MethodPost, "/records/save", saveRequest{Record: stable}, &output); err != nil {
		return stable, err
	}
	return output.Record, decodeStoreError(output.Error)
}

func (w *Workspace) SaveBatch(records []sessionstore.Record) ([]sessionstore.Record, error) {
	return w.SaveBatchContext(context.Background(), records)
}

func (w *Workspace) SaveBatchContext(ctx context.Context, records []sessionstore.Record) ([]sessionstore.Record, error) {
	stable := make([]sessionstore.Record, len(records))
	for index, record := range records {
		prepared, err := withStableRecordID(record)
		if err != nil {
			return stable[:index], err
		}
		stable[index] = prepared
	}
	var output saveBatchResponse
	if err := w.call(ctx, http.MethodPost, "/records/save-batch", saveBatchRequest{Records: stable}, &output); err != nil {
		return stable, err
	}
	if output.Records == nil {
		output.Records = []sessionstore.Record{}
	}
	return output.Records, decodeStoreError(output.Error)
}

func withStableRecordID(record sessionstore.Record) (sessionstore.Record, error) {
	if strings.TrimSpace(record.ID) != "" {
		return record, nil
	}
	id, err := sessionstore.NewID()
	if err != nil {
		return sessionstore.Record{}, err
	}
	record.ID = id
	return record, nil
}

func decodeStoreError(value *storeError) error {
	if value == nil {
		return nil
	}
	if value.Code == "indexing_error" {
		return &sessionstore.IndexingError{RecordID: value.RecordID, Err: errors.New(value.Message)}
	}
	if value.Message == "" {
		return errors.New("workspacememory: remote Store operation failed")
	}
	return errors.New(value.Message)
}

func (w *Workspace) Get(id string) (sessionstore.Record, error) {
	return w.GetContext(context.Background(), id)
}

func (w *Workspace) GetContext(ctx context.Context, id string) (sessionstore.Record, error) {
	var output getResponse
	if err := w.call(ctx, http.MethodPost, "/records/get", recordIDRequest{ID: id}, &output); err != nil {
		return sessionstore.Record{}, err
	}
	return output.Record, nil
}

func (w *Workspace) Delete(id string) error {
	return w.DeleteContext(context.Background(), id)
}

func (w *Workspace) DeleteContext(ctx context.Context, id string) error {
	err := w.call(ctx, http.MethodPost, "/records/delete", recordIDRequest{ID: id}, &struct{}{})
	if errors.Is(err, sessionstore.ErrNotFound) {
		return nil
	}
	return err
}

func (w *Workspace) Search(ctx context.Context, options sessionstore.SearchOptions) (sessionstore.SearchResult, error) {
	var output searchResponse
	if err := w.call(ctx, http.MethodPost, "/search", searchRequest{Options: options}, &output); err != nil {
		return sessionstore.SearchResult{}, err
	}
	return output.Result, nil
}

func (w *Workspace) ConfigureVector(vector sessionstore.VectorConfig) error {
	return w.ConfigureVectorContext(context.Background(), vector)
}

func (w *Workspace) ConfigureVectorContext(ctx context.Context, vector sessionstore.VectorConfig) error {
	var output configureVectorResponse
	if err := w.callWithoutReconnect(ctx, http.MethodPost, "/vector/configure", configureVectorRequest{Vector: vector}, &output); err != nil {
		return err
	}
	w.vectorMu.Lock()
	w.vector = output.Vector
	w.vectorMu.Unlock()
	return nil
}

func (w *Workspace) callWithoutReconnect(ctx context.Context, method, suffix string, input, output any) error {
	if w == nil || w.client == nil {
		return ErrWorkspaceClosed
	}
	w.operation.RLock()
	defer w.operation.RUnlock()
	if w.closed {
		return ErrWorkspaceClosed
	}
	return w.client.doJSON(ctx, method, "/workspaces/"+w.id+suffix, input, output, w.leaseID, true)
}

func (w *Workspace) Flush() error { return w.FlushContext(context.Background()) }

func (w *Workspace) FlushContext(ctx context.Context) error {
	return w.call(ctx, http.MethodPost, "/flush", struct{}{}, &struct{}{})
}

func (w *Workspace) call(ctx context.Context, method, suffix string, input, output any) error {
	if w == nil || w.client == nil {
		return ErrWorkspaceClosed
	}
	w.operation.RLock()
	defer w.operation.RUnlock()
	if w.closed {
		return ErrWorkspaceClosed
	}
	path := "/workspaces/" + w.id + suffix
	err := w.client.doJSON(ctx, method, path, input, output, w.leaseID, true)
	if err == nil || !reconnectableWorkspaceError(err) {
		return err
	}
	if reopenErr := w.reopen(ctx); reopenErr != nil {
		return errors.Join(err, fmt.Errorf("workspacememory: reopen workspace: %w", reopenErr))
	}
	return w.client.doJSON(ctx, method, path, input, output, w.leaseID, true)
}

func reconnectableWorkspaceError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, ErrWorkspaceClosed) || errors.Is(err, ErrLeaseExpired) {
		return true
	}
	var protocolError *HTTPError
	if errors.As(err, &protocolError) {
		return false
	}
	var networkError net.Error
	return errors.As(err, &networkError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (w *Workspace) reopen(ctx context.Context) error {
	w.reconnect.Lock()
	defer w.reconnect.Unlock()
	reopenContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	delay := 20 * time.Millisecond
	for {
		w.vectorMu.RLock()
		vector := w.vector
		w.vectorMu.RUnlock()
		input := openWorkspaceRequest{Root: w.root, Vector: vector, LeaseID: w.leaseID}
		var output OpenWorkspaceResponse
		err := w.client.doJSON(reopenContext, http.MethodPost, "/workspaces/open", input, &output, "", true)
		if err == nil {
			if output.WorkspaceID != w.id || output.LeaseID != w.leaseID || output.Root == "" {
				return errors.New("workspacememory: reopened workspace identity changed")
			}
			return nil
		}
		var protocolError *HTTPError
		if errors.As(err, &protocolError) {
			return err
		}
		select {
		case <-reopenContext.Done():
			return errors.Join(err, reopenContext.Err())
		case <-time.After(delay):
		}
		delay = min(delay*2, 500*time.Millisecond)
	}
}

func (w *Workspace) heartbeat(ctx context.Context) {
	defer close(w.heartbeatDone)
	interval := w.leaseTTL / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	path := "/workspaces/" + w.id + "/leases/" + w.leaseID + "/renew"
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			requestContext, cancel := context.WithTimeout(ctx, min(w.client.requestTimeout, interval))
			_ = w.client.doJSON(requestContext, http.MethodPost, path, struct{}{}, &struct{}{}, "", true)
			cancel()
		}
	}
}

// Close releases only this handle's lease. The server closes the shared
// Bleve/HNSW Store and its workspace-memory lock after the final lease. Close
// is safe to call repeatedly.
func (w *Workspace) Close() error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		defer close(w.closeDone)
		w.operation.Lock()
		w.closed = true
		w.operation.Unlock()
		w.heartbeatCancel()
		<-w.heartbeatDone
		timeout := w.client.requestTimeout
		if timeout <= 0 || timeout > 10*time.Second {
			timeout = 10 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		path := "/workspaces/" + w.id + "/leases/" + w.leaseID
		w.closeErr = w.client.doJSON(ctx, http.MethodDelete, path, nil, &struct{}{}, "", true)
		if reconnectableWorkspaceError(w.closeErr) {
			// A failed leader has already discarded this lease; a replacement
			// leader has nothing to release. Close remains a local idempotent
			// ownership boundary rather than reopening solely to delete it.
			w.closeErr = nil
		}
	})
	<-w.closeDone
	return w.closeErr
}
