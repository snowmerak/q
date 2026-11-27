package library

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/snowmerak/q/internal/closeutil"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/worklock"
)

type leader struct {
	health     Health
	server     *http.Server
	archive    sessionstore.SearchIndex
	workspaces workitems.Repository
	knowledge  *knowledgeIndex
	lock       *worklock.Lock
	closers    []io.Closer
	cancel     context.CancelFunc
	done       chan struct{}

	closeOnce sync.Once
	errMu     sync.Mutex
	err       error
}

func startLeader(
	parent context.Context,
	dir string,
	vector sessionstore.VectorConfig,
	backends Backends,
	listener net.Listener,
	lock *worklock.Lock,
) (*leader, error) {
	directory, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	openWorkItems := backends.OpenWorkItems
	if openWorkItems == nil {
		openWorkItems = func(ctx context.Context, configDir string) (workitems.Repository, error) {
			return workitems.Open(ctx, configDir)
		}
	}
	catalog, err := openWorkItems(parent, dir)
	if err != nil {
		return nil, fmt.Errorf("library: open workspace catalog: %w", err)
	}
	if catalog == nil {
		return nil, errors.New("library: workitems repository opener returned nil")
	}
	keepCatalog := false
	defer func() {
		if !keepCatalog {
			_ = catalog.Close()
		}
	}()
	root := filepath.Join(dir, "library")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("library: create store root: %w", err)
	}
	defaultSearch := localSearchBackend{dir: dir, lock: lock}
	skillsBackend := backends.skillsBackend(defaultSearch)
	knowledgeBackend := backends.knowledgeBackend(defaultSearch)
	openedArchive, err := skillsBackend.Open(parent, SearchIndexSpec{
		Purpose:   SearchPurposeSkills,
		Namespace: "global",
		Vector:    vector,
	})
	if err != nil {
		return nil, fmt.Errorf("library: open global Store: %w", err)
	}
	if openedArchive.Index == nil {
		return nil, errors.New("library: search backend returned nil global archive")
	}
	archive := openedArchive.Index
	keepArchive := false
	defer func() {
		if !keepArchive {
			_ = archive.Close()
		}
	}()
	skills, err := newSkillService(parent, dir, archive)
	if err != nil {
		return nil, err
	}
	storeID, err := loadOrCreateID(filepath.Join(root, "store.id"))
	if err != nil {
		_ = archive.Close()
		return nil, err
	}
	generation, err := randomID()
	if err != nil {
		_ = archive.Close()
		return nil, err
	}
	health := Health{
		ConfigDirectory: directory,
		Service:         ServiceName, ProtocolVersion: ProtocolVersion,
		Implementation: Implementation, StoreID: storeID,
		Generation: generation, Ready: true,
		BundledSkills: true,
	}

	ctx, cancel := context.WithCancel(parent)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, skills.bundleHealth(health))
	})
	mux.HandleFunc("GET /v1/status", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, skills.bundleHealth(health))
	})
	registerSkillRoutes(mux, skills)
	registerWorkspaceRoutes(mux, catalog)
	registerItemRoutes(mux, catalog)
	registerWorkAssetRoutes(mux, catalog)
	registerCoordinatorRoutes(mux, catalog)
	registerWorkerRoute(mux, catalog, dir)
	knowledge := newKnowledgeIndex(ctx, dir, catalog, knowledgeBackend, vector)
	registerKnowledgeRoutes(mux, knowledge)
	l := &leader{
		health: health, archive: archive,
		workspaces: catalog,
		knowledge:  knowledge,
		lock:       lock,
		closers:    closeutil.Deduplicate(backends.Closers),
		cancel:     cancel, done: make(chan struct{}),
		server: &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second},
	}
	keepArchive = true
	keepCatalog = true
	go func() {
		serveErr := l.server.Serve(listener)
		cancel()
		knowledgeErr := l.knowledge.Close()
		archiveErr := l.archive.Close()
		catalogErr := l.workspaces.Close()
		closersErr := closeutil.CloseAll(l.closers)
		lockErr := l.lock.Close()
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		l.errMu.Lock()
		l.err = errors.Join(serveErr, archiveErr, catalogErr, knowledgeErr, closersErr, lockErr)
		l.errMu.Unlock()
		close(l.done)
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		_ = l.server.Shutdown(shutdown)
	}()
	return l, nil
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

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func loadOrCreateID(path string) (string, error) {
	if body, err := os.ReadFile(path); err == nil {
		value := string(body)
		if len(value) == 33 && value[32] == '\n' {
			value = value[:32]
		}
		if len(value) == 32 {
			if _, err := hex.DecodeString(value); err == nil {
				return value, nil
			}
		}
		return "", errors.New("library: invalid store ID")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("library: read store ID: %w", err)
	}
	value, err := randomID()
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return loadOrCreateID(path)
	}
	if err != nil {
		return "", fmt.Errorf("library: create store ID: %w", err)
	}
	if _, err := file.WriteString(value + "\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("library: write store ID: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("library: close store ID: %w", err)
	}
	return value, nil
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("library: generate ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}
