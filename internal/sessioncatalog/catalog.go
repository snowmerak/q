package sessioncatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/worklock"
	"github.com/snowmerak/q/workspace"
)

const (
	version     = 1
	fileName    = "studio-sessions.json"
	maximumSize = 1 << 20
)

type Entry struct {
	ID            string    `json:"id"`
	WorkspaceRoot string    `json:"workspace_root"`
	SessionID     string    `json:"session_id"`
	RegisteredAt  time.Time `json:"registered_at"`
}

type document struct {
	Version int     `json:"version"`
	Items   []Entry `json:"items"`
}

// Catalog is a user-level catalog. Workspace session files remain the
// authority for transcripts, execution state, and delegation children.
type Catalog struct {
	path string
	mu   sync.Mutex
}

func New(configDirectory string) *Catalog {
	return &Catalog{path: filepath.Join(configDirectory, fileName)}
}

func (registry *Catalog) List() ([]Entry, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	value, err := registry.loadLocked()
	if err != nil {
		return nil, err
	}
	return append([]Entry(nil), value.Items...), nil
}

func (registry *Catalog) Register(root, sessionID string) (Entry, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	guard, err := registry.mutationLock()
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = guard.Close() }()

	value, err := registry.loadLocked()
	if err != nil {
		return Entry{}, err
	}
	for _, item := range value.Items {
		if item.WorkspaceRoot == root && item.SessionID == sessionID {
			return item, nil
		}
	}
	id, err := workspace.NewSessionID()
	if err != nil {
		return Entry{}, err
	}
	item := Entry{ID: id, WorkspaceRoot: root, SessionID: sessionID, RegisteredAt: time.Now().UTC()}
	value.Items = append(value.Items, item)
	if err := registry.saveLocked(value); err != nil {
		return Entry{}, err
	}
	return item, nil
}

func (registry *Catalog) Unregister(id string) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	guard, err := registry.mutationLock()
	if err != nil {
		return err
	}
	defer func() { _ = guard.Close() }()

	value, err := registry.loadLocked()
	if err != nil {
		return err
	}
	for index, item := range value.Items {
		if item.ID != id {
			continue
		}
		if err := (workspace.Store{Root: item.WorkspaceRoot, SessionID: item.SessionID}).RequireDeletable(); err != nil {
			return err
		}
		value.Items = append(value.Items[:index], value.Items[index+1:]...)
		return registry.saveLocked(value)
	}
	return os.ErrNotExist
}

func (registry *Catalog) loadLocked() (document, error) {
	file, err := os.Open(registry.path)
	if errors.Is(err, os.ErrNotExist) {
		return document{Version: version, Items: []Entry{}}, nil
	}
	if err != nil {
		return document{}, fmt.Errorf("open Studio session registry: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return document{}, fmt.Errorf("inspect Studio session registry: %w", err)
	}
	if info.Size() > maximumSize {
		return document{}, errors.New("studio session registry is too large")
	}
	var value document
	decoder := json.NewDecoder(io.LimitReader(file, maximumSize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return document{}, fmt.Errorf("decode Studio session registry: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return document{}, errors.New("decode Studio session registry: multiple JSON values")
	}
	if err := validate(value); err != nil {
		return document{}, err
	}
	return value, nil
}

func validate(value document) error {
	if value.Version != version {
		return fmt.Errorf("unsupported Studio session registry version %d", value.Version)
	}
	ids := make(map[string]bool, len(value.Items))
	sessions := make(map[string]bool, len(value.Items))
	for _, item := range value.Items {
		if _, err := (workspace.Store{Root: item.WorkspaceRoot}).ForSession(item.ID); err != nil {
			return errors.New("invalid Studio registration ID")
		}
		if _, err := (workspace.Store{Root: item.WorkspaceRoot}).ForSession(item.SessionID); err != nil {
			return errors.New("invalid registered session ID")
		}
		if !filepath.IsAbs(item.WorkspaceRoot) || filepath.Clean(item.WorkspaceRoot) != item.WorkspaceRoot || item.RegisteredAt.IsZero() {
			return errors.New("invalid registered session")
		}
		key := item.WorkspaceRoot + "\x00" + item.SessionID
		if ids[item.ID] || sessions[key] {
			return errors.New("duplicate registered session")
		}
		ids[item.ID], sessions[key] = true, true
	}
	return nil
}

func (registry *Catalog) saveLocked(value document) error {
	value.Version = version
	if err := validate(value); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > maximumSize {
		return errors.New("studio session registry is too large")
	}
	directory := filepath.Dir(registry.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Studio config directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure Studio config directory: %w", err)
	}
	if err := fsreplace.WriteFile(registry.path, body, 0o600); err != nil {
		return fmt.Errorf("save Studio session registry: %w", err)
	}
	return nil
}

// mutationLock serializes catalog updates across all workflows and Studio processes.
func (registry *Catalog) mutationLock() (*worklock.Lock, error) {
	directory := filepath.Dir(registry.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		lock, err := worklock.AcquireFile(directory, "studio-sessions.lock", "q session catalog")
		if !errors.Is(err, worklock.ErrLocked) || attempt >= 100 {
			return lock, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}
