package workspacememory

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/archiveembed"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/worklock"
)

var (
	_ sessionstore.WriterStore = (*Workspace)(nil)
	_ archiveembed.Store       = (*Workspace)(nil)
	_ agentskills.RecordStore  = (*Workspace)(nil)
)

func TestWorkspaceServiceMultiplexesCanonicalRootsAndLeases(t *testing.T) {
	configDir := t.TempDir()
	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(firstRoot, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	options := testEnsureOptions(t, configDir)
	service, err := EnsureWithOptions(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if !service.IsLeader() {
		t.Fatal("first runtime did not become leader")
	}

	follower, err := EnsureWithOptions(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()
	if follower.IsLeader() {
		t.Fatal("second runtime unexpectedly became leader")
	}
	leaderHealth, err := service.Client().Health(context.Background())
	if err != nil || !leaderHealth.Compatible() {
		t.Fatalf("health = %#v, err = %v", leaderHealth, err)
	}
	followerHealth, err := follower.Client().Health(context.Background())
	if err != nil || followerHealth.Generation != leaderHealth.Generation {
		t.Fatalf("follower health = %#v, err = %v", followerHealth, err)
	}
	if _, err := NewClient(service.Endpoint(), "wrong", time.Second).Status(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong credential error = %v", err)
	}

	first, err := service.Client().OpenWorkspace(context.Background(), firstRoot, sessionstore.VectorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	alias := filepath.Join(firstRoot, "nested", "..")
	secondLease, err := follower.Client().OpenWorkspace(context.Background(), alias, sessionstore.VectorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer secondLease.Close()
	if first.ID() != secondLease.ID() || first.Root() != secondLease.Root() {
		t.Fatalf("canonical handles differ: %#v %#v", first, secondLease)
	}
	other, err := service.Client().OpenWorkspace(context.Background(), secondRoot, sessionstore.VectorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.ID() == first.ID() {
		t.Fatal("distinct roots received the same workspace ID")
	}
	status, err := service.Client().Status(context.Background())
	if err != nil || status.OpenWorkspaces != 2 {
		t.Fatalf("status = %#v, err = %v", status, err)
	}

	saved, err := first.Save(sessionstore.Record{Kind: sessionstore.KindMessage, RunID: "run-1", Content: "shared archive token"})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := secondLease.Get(saved.ID)
	if err != nil || loaded.Content != saved.Content {
		t.Fatalf("loaded = %#v, err = %v", loaded, err)
	}
	search, err := secondLease.Search(context.Background(), sessionstore.SearchOptions{Text: "shared archive token", Limit: 5})
	if err != nil || len(search.Hits) != 1 || search.Hits[0].Record.ID != saved.ID {
		t.Fatalf("search = %#v, err = %v", search, err)
	}
	if _, err := first.Get("missing"); !errors.Is(err, sessionstore.ErrNotFound) {
		t.Fatalf("missing record error = %v", err)
	}
	if err := first.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second close = %v", err)
	}
	if _, err := first.Get(saved.ID); !errors.Is(err, ErrWorkspaceClosed) {
		t.Fatalf("closed handle get error = %v", err)
	}
	if _, err := secondLease.Get(saved.ID); err != nil {
		t.Fatalf("closing one lease closed the shared Store: %v", err)
	}
	if err := secondLease.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := worklock.AcquireFile(firstRoot, WorkspaceLockFileName, "workspace memory test")
	if err != nil {
		t.Fatalf("final lease did not release workspace lock: %v", err)
	}
	_ = lock.Close()
}

func TestWorkspaceVectorConfigurationAndSearch(t *testing.T) {
	options := testEnsureOptions(t, t.TempDir())
	service, err := EnsureWithOptions(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	root := t.TempDir()
	config := sessionstore.VectorConfig{Model: "test-embedding", Dimensions: 2}
	workspace, err := service.Client().OpenWorkspace(context.Background(), root, config)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	if !workspace.VectorConfig().Enabled() {
		t.Fatalf("vector config = %#v", workspace.VectorConfig())
	}
	shared, err := service.Client().OpenWorkspace(context.Background(), root, config)
	if err != nil {
		t.Fatalf("same vector configuration was not idempotent: %v", err)
	}
	defer shared.Close()
	if _, err := service.Client().OpenWorkspace(context.Background(), root, sessionstore.VectorConfig{Model: "other", Dimensions: 2}); !errors.Is(err, ErrVectorConflict) {
		t.Fatalf("vector conflict error = %v", err)
	}

	records, err := workspace.SaveBatch([]sessionstore.Record{
		{Kind: sessionstore.KindResult, Content: "east", Embedding: &sessionstore.Embedding{Model: config.Model, Dimensions: 2, Vector: []float32{1, 0}}},
		{Kind: sessionstore.KindResult, Content: "north", Embedding: &sessionstore.Embedding{Model: config.Model, Dimensions: 2, Vector: []float32{0, 1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := shared.Search(context.Background(), sessionstore.SearchOptions{
		Vector: &sessionstore.VectorQuery{Embedding: []float32{1, 0}}, Limit: 1,
	})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Record.ID != records[0].ID {
		t.Fatalf("vector search = %#v, err = %v", result, err)
	}
	if err := workspace.ConfigureVector(sessionstore.VectorConfig{}); !errors.Is(err, ErrVectorConflict) {
		t.Fatalf("ConfigureVector with multiple leases error = %v", err)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	if err := workspace.ConfigureVector(sessionstore.VectorConfig{}); err != nil {
		t.Fatal(err)
	}
	if workspace.VectorConfig().Enabled() {
		t.Fatalf("disabled vector config = %#v", workspace.VectorConfig())
	}
}

func TestWorkspaceManagerExpiresAbandonedLease(t *testing.T) {
	root := t.TempDir()
	manager := newWorkspaceManager(20 * time.Millisecond)
	response, err := manager.open(openWorkspaceRequest{
		Root: root, LeaseID: "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if err := manager.sweepExpired(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.acquire(response.WorkspaceID, response.LeaseID); !errors.Is(err, ErrWorkspaceClosed) {
		t.Fatalf("expired workspace error = %v", err)
	}
	lock, err := worklock.AcquireFile(root, WorkspaceLockFileName, "expired lease test")
	if err != nil {
		t.Fatalf("expired lease did not release lock: %v", err)
	}
	_ = lock.Close()
	if err := manager.close(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceOpenPreservesLegacyIndexLockError(t *testing.T) {
	root := t.TempDir()
	direct, err := sessionstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	service, err := EnsureWithOptions(context.Background(), testEnsureOptions(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, err := service.Client().OpenWorkspace(context.Background(), root, sessionstore.VectorConfig{}); !errors.Is(err, sessionstore.ErrIndexLocked) {
		t.Fatalf("OpenWorkspace error = %v, want ErrIndexLocked", err)
	}
}

func TestWriterDrainsBeforeServiceShutdown(t *testing.T) {
	ctx := context.Background()
	options := testEnsureOptions(t, t.TempDir())
	root := t.TempDir()
	service, err := EnsureWithOptions(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := service.Client().OpenWorkspace(ctx, root, sessionstore.VectorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	writer := sessionstore.NewWriterWithOptions(workspace, sessionstore.WriterOptions{Buffer: 4, BatchSize: 4})
	if err := writer.Append(sessionstore.Record{Kind: sessionstore.KindMessage, Content: "drained before shutdown"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := EnsureWithOptions(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	reopened, err := restarted.Client().OpenWorkspace(ctx, root, sessionstore.VectorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	result, err := reopened.Search(ctx, sessionstore.SearchOptions{Text: "drained before shutdown", Limit: 5})
	if err != nil || len(result.Hits) != 1 {
		t.Fatalf("restarted search = %#v, err = %v", result, err)
	}
}

func TestWorkspaceReconnectsAfterLeaderHandoff(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	options := testEnsureOptions(t, configDir)
	if err := (ConfigStore{Dir: configDir}).Save(options.Config); err != nil {
		t.Fatal(err)
	}
	leader, err := EnsureWithOptions(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	follower, err := EnsureWithOptions(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()
	workspace, err := follower.Client().OpenWorkspace(ctx, t.TempDir(), sessionstore.VectorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := workspace.Save(sessionstore.Record{Kind: sessionstore.KindMessage, Content: "survives leader handoff"})
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- Run(runContext, configDir, nil) }()
	if err := leader.Close(); err != nil {
		t.Fatal(err)
	}
	openedDuringHandoff, err := follower.Client().OpenWorkspace(ctx, t.TempDir(), sessionstore.VectorConfig{})
	if err != nil {
		t.Fatalf("OpenWorkspace during leader handoff: %v", err)
	}
	defer openedDuringHandoff.Close()
	loaded, err := workspace.Get(saved.ID)
	if err != nil || loaded.Content != saved.Content {
		t.Fatalf("Get after leader handoff = (%#v, %v)", loaded, err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	cancelRun()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
}

type loseFirstResponse struct {
	next   http.RoundTripper
	suffix string
	lost   atomic.Bool
}

func (transport *loseFirstResponse) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.next.RoundTrip(request)
	if err != nil || !strings.HasSuffix(request.URL.Path, transport.suffix) ||
		!transport.lost.CompareAndSwap(false, true) {
		return response, err
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return nil, &net.OpError{Op: "read", Net: "tcp", Err: io.ErrUnexpectedEOF}
}

func TestSaveBatchResponseLossDoesNotDuplicateRecords(t *testing.T) {
	ctx := context.Background()
	service, err := EnsureWithOptions(ctx, testEnsureOptions(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	client := service.Client()
	client.http.Transport = &loseFirstResponse{next: http.DefaultTransport, suffix: "/records/save-batch"}
	workspace, err := client.OpenWorkspace(ctx, t.TempDir(), sessionstore.VectorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	records, err := workspace.SaveBatch([]sessionstore.Record{{Kind: sessionstore.KindMessage, Content: "exactly once archive write"}})
	if err != nil || len(records) != 1 || records[0].ID == "" {
		t.Fatalf("SaveBatch = (%#v, %v)", records, err)
	}
	result, err := workspace.Search(ctx, sessionstore.SearchOptions{Text: "exactly once archive write", Limit: 10})
	if err != nil || result.Total != 1 || len(result.Hits) != 1 || result.Hits[0].Record.ID != records[0].ID {
		t.Fatalf("Search after response loss = (%#v, %v)", result, err)
	}
}

func TestOpenWorkspaceResponseLossReusesLease(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	service, err := EnsureWithOptions(ctx, testEnsureOptions(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	client := service.Client()
	client.http.Transport = &loseFirstResponse{next: http.DefaultTransport, suffix: "/workspaces/open"}
	workspace, err := client.OpenWorkspace(ctx, root, sessionstore.VectorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := worklock.AcquireFile(root, WorkspaceLockFileName, "open response loss test")
	if err != nil {
		t.Fatalf("retried open created an extra lease: %v", err)
	}
	_ = lock.Close()
}

func TestDeleteResponseLossRemainsIdempotent(t *testing.T) {
	ctx := context.Background()
	service, err := EnsureWithOptions(ctx, testEnsureOptions(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	client := service.Client()
	workspace, err := client.OpenWorkspace(ctx, t.TempDir(), sessionstore.VectorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	record, err := workspace.Save(sessionstore.Record{Kind: sessionstore.KindMessage, Content: "delete exactly once"})
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = &loseFirstResponse{next: http.DefaultTransport, suffix: "/records/delete"}
	if err := workspace.Delete(record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Get(record.ID); !errors.Is(err, sessionstore.ErrNotFound) {
		t.Fatalf("Get after delete error = %v", err)
	}
}

func TestSaveProtocolPreservesPreparedRecordsAndIndexingError(t *testing.T) {
	prepared := sessionstore.Record{ID: "server-assigned-id", Kind: sessionstore.KindMessage, Version: sessionstore.RecordVersion}
	indexFailure := errors.New("derived index unavailable")
	store := &failingWorkspaceStore{
		saveRecords:  []sessionstore.Record{prepared},
		saveErr:      &sessionstore.IndexingError{RecordID: prepared.ID, Err: indexFailure},
		configureErr: &sessionstore.IndexingError{Err: indexFailure},
	}
	manager := newWorkspaceManager(time.Minute)
	entry := &workspaceEntry{
		id: "workspace-id", key: "workspace-key", root: t.TempDir(), store: store,
		leases: map[string]workspaceLease{"lease-id": {expiresAt: time.Now().Add(time.Minute)}},
	}
	manager.entries[entry.id] = entry
	manager.byKey[entry.key] = entry
	defer manager.close()
	health := Health{Service: ServiceName, ProtocolVersion: ProtocolVersion, Implementation: Implementation, Ready: true}
	server := httptest.NewServer(newHandler(health, "test-secret", manager))
	defer server.Close()
	client := NewClient(server.URL+"/v1", "test-secret", time.Second)
	workspace := &Workspace{client: client, id: entry.id, leaseID: "lease-id", root: entry.root}

	record, err := workspace.Save(sessionstore.Record{Kind: sessionstore.KindMessage})
	if record.ID != prepared.ID {
		t.Fatalf("Save record = %#v", record)
	}
	var indexing *sessionstore.IndexingError
	if !errors.As(err, &indexing) || indexing.RecordID != prepared.ID || indexing.Err.Error() != indexFailure.Error() {
		t.Fatalf("Save error = %#v", err)
	}
	records, err := workspace.SaveBatch([]sessionstore.Record{{Kind: sessionstore.KindMessage}})
	if len(records) != 1 || records[0].ID != prepared.ID {
		t.Fatalf("SaveBatch records = %#v", records)
	}
	indexing = nil
	if !errors.As(err, &indexing) || indexing.RecordID != prepared.ID {
		t.Fatalf("SaveBatch error = %#v", err)
	}
	if err := workspace.ConfigureVector(sessionstore.VectorConfig{Model: "test", Dimensions: 2}); !errors.As(err, &indexing) {
		t.Fatalf("ConfigureVector error = %#v", err)
	}
	if err := (&HTTPError{Code: "index_locked"}); !errors.Is(err, sessionstore.ErrIndexLocked) {
		t.Fatalf("index-locked HTTP error = %#v", err)
	}
}

func TestConfigAndCredentialRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := ConfigStore{Dir: dir}
	config := testConfig(t)
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(config); err != nil {
		t.Fatalf("replace config: %v", err)
	}
	loaded, err := store.LoadOrDefault()
	if err != nil || loaded != config.Effective() {
		t.Fatalf("loaded = %#v, err = %v", loaded, err)
	}
	first, err := store.EnsureCredential()
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnsureCredential()
	if err != nil || second != first || len(first) != 64 {
		t.Fatalf("credentials = %q %q, err = %v", first, second, err)
	}
}

func TestEnsureCredentialIsConcurrentAndAtomic(t *testing.T) {
	store := ConfigStore{Dir: t.TempDir()}
	const callers = 16
	credentials := make(chan string, callers)
	errorsFound := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			credential, err := store.EnsureCredential()
			credentials <- credential
			errorsFound <- err
		}()
	}
	group.Wait()
	close(credentials)
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	var expected string
	for credential := range credentials {
		if expected == "" {
			expected = credential
		}
		if credential != expected {
			t.Fatalf("credentials differ: %q != %q", credential, expected)
		}
	}
	if loaded, err := store.LoadCredential(); err != nil || loaded != expected {
		t.Fatalf("LoadCredential = (%q, %v), want %q", loaded, err, expected)
	}
}

func testEnsureOptions(t *testing.T, dir string) EnsureOptions {
	t.Helper()
	return EnsureOptions{
		Dir: dir, Config: testConfig(t), ProbeTimeout: time.Second,
		StartupTimeout: 3 * time.Second, RequestTimeout: 10 * time.Second,
		LeaseTTL: 2 * time.Second, SweepInterval: 100 * time.Millisecond,
	}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return Config{Version: ConfigVersion, Host: "127.0.0.1", Port: port}
}

type failingWorkspaceStore struct {
	saveRecords  []sessionstore.Record
	saveErr      error
	configureErr error
}

func (s *failingWorkspaceStore) Save(sessionstore.Record) (sessionstore.Record, error) {
	return s.saveRecords[0], s.saveErr
}

func (s *failingWorkspaceStore) SaveBatch([]sessionstore.Record) ([]sessionstore.Record, error) {
	return append([]sessionstore.Record(nil), s.saveRecords...), s.saveErr
}

func (s *failingWorkspaceStore) Get(string) (sessionstore.Record, error) {
	return sessionstore.Record{}, sessionstore.ErrNotFound
}

func (s *failingWorkspaceStore) Delete(string) error { return nil }

func (s *failingWorkspaceStore) Search(context.Context, sessionstore.SearchOptions) (sessionstore.SearchResult, error) {
	return sessionstore.SearchResult{}, nil
}

func (s *failingWorkspaceStore) ConfigureVector(sessionstore.VectorConfig) error {
	return s.configureErr
}

func (s *failingWorkspaceStore) VectorConfig() sessionstore.VectorConfig {
	return sessionstore.VectorConfig{}
}

func (s *failingWorkspaceStore) Close() error { return nil }
