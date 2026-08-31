package library

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/worklock"
)

type leader struct {
	health       Health
	server       *http.Server
	archive      *sessionstore.Store
	propositions *propositionService
	queue        *propositionQueue
	judge        *modelPropositionJudge
	lock         *worklock.Lock
	cancel       context.CancelFunc
	done         chan struct{}

	closeOnce sync.Once
	errMu     sync.Mutex
	err       error
}

func startLeader(
	parent context.Context,
	dir string,
	vector sessionstore.VectorConfig,
	configuredJudge PropositionJudge,
	listener net.Listener,
	lock *worklock.Lock,
) (*leader, error) {
	root := filepath.Join(dir, "library")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("library: create store root: %w", err)
	}
	archive, err := sessionstore.OpenWithOptions(dir, sessionstore.OpenOptions{
		WorkspaceLock: lock, Directory: "library", Vector: vector,
	})
	if err != nil {
		return nil, fmt.Errorf("library: open global Store: %w", err)
	}
	queue, err := openPropositionQueue(dir)
	if err != nil {
		_ = archive.Close()
		return nil, err
	}
	skills, err := newSkillService(parent, dir, archive)
	if err != nil {
		_ = queue.close()
		_ = archive.Close()
		return nil, err
	}
	storeID, err := loadOrCreateID(filepath.Join(root, "store.id"))
	if err != nil {
		_ = queue.close()
		_ = archive.Close()
		return nil, err
	}
	generation, err := randomID()
	if err != nil {
		_ = queue.close()
		_ = archive.Close()
		return nil, err
	}
	health := Health{
		Service: ServiceName, ProtocolVersion: ProtocolVersion,
		Implementation: Implementation, StoreID: storeID,
		Generation: generation, Ready: true,
	}

	ctx, cancel := context.WithCancel(parent)
	judge := configuredJudge
	var modelJudge *modelPropositionJudge
	var judgeErr error
	if judge == nil {
		modelJudge, judgeErr = newConfiguredPropositionJudge(ctx, dir)
		if modelJudge != nil {
			judge = modelJudge
		}
	}
	propositions := newPropositionService(ctx, archive, queue, judge, judgeErr)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, health)
	})
	mux.HandleFunc("GET /v1/status", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, health)
	})
	registerSkillRoutes(mux, skills)
	registerPropositionRoutes(mux, propositions)
	l := &leader{
		health: health, archive: archive, propositions: propositions, queue: queue, judge: modelJudge,
		lock: lock, cancel: cancel, done: make(chan struct{}),
		server: &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second},
	}
	go func() {
		serveErr := l.server.Serve(listener)
		cancel()
		l.propositions.close()
		judgeErr := l.judge.Close()
		queueErr := l.queue.close()
		archiveErr := l.archive.Close()
		lockErr := l.lock.Close()
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		l.errMu.Lock()
		l.err = errors.Join(serveErr, judgeErr, queueErr, archiveErr, lockErr)
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
