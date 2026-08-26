// Package workspace persists active q state for the directory where q is run.
package workspace

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/thinker"
)

const (
	CurrentVersion = 1
	DirectoryName  = ".q"
	SessionsName   = "sessions"
	FileName       = "session.json"
	maximumSize    = 64 << 20
)

var ErrNotFound = errors.New("q workspace session not found")

type Session struct {
	Version    int                   `json:"version"`
	ID         string                `json:"id,omitempty"`
	RunID      string                `json:"run_id,omitempty"`
	Title      string                `json:"title,omitempty"`
	UpdatedAt  *time.Time            `json:"updated_at,omitempty"`
	Transcript []client.Message      `json:"transcript,omitempty"`
	Context    []client.Message      `json:"context,omitempty"`
	Learning   thinker.LearningState `json:"learning,omitempty"`
	ActiveTask *ActiveTask           `json:"active_task,omitempty"`
}

// ActiveTask is an explicit task lifecycle that survived beyond the turn in
// which task_start was called. It remains present until task_complete succeeds
// or the conversation is cleared.
type ActiveTask struct {
	Objective          string    `json:"objective"`
	CompletionCriteria []string  `json:"completion_criteria,omitempty"`
	StartedAt          time.Time `json:"started_at"`
}

type Store struct {
	Root      string
	SessionID string
}

// SessionEntry describes one persisted session projection. Sessions are
// ordered newest-first by ListSessions.
type SessionEntry struct {
	Store     Store
	RunID     string
	Title     string
	UpdatedAt time.Time
}

func DefaultStore() (Store, error) {
	root, err := os.Getwd()
	if err != nil {
		return Store{}, fmt.Errorf("workspace: find current directory: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return Store{}, fmt.Errorf("workspace: resolve current directory: %w", err)
	}
	return Store{Root: root}, nil
}

func (s Store) Dir() string {
	return filepath.Join(s.Root, DirectoryName)
}

func (s Store) SessionsDir() string {
	return filepath.Join(s.Dir(), SessionsName)
}

func (s Store) SessionDir() string {
	if s.SessionID == "" {
		return s.Dir()
	}
	return filepath.Join(s.SessionsDir(), s.SessionID)
}

func (s Store) Path() string {
	return filepath.Join(s.SessionDir(), FileName)
}

// ForSession returns a store scoped to one persisted session.
func (s Store) ForSession(sessionID string) (Store, error) {
	sessionID = strings.TrimSpace(sessionID)
	if err := validateSessionID(sessionID); err != nil {
		return Store{}, err
	}
	return Store{Root: s.Root, SessionID: sessionID}, nil
}

// NewSessionID returns an RFC 4122 version 4 UUID for a new session.
func NewSessionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("workspace: generate session ID: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	), nil
}

func (s Store) Load() (Session, error) {
	file, err := os.Open(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("workspace: open %s: %w", s.Path(), err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Session{}, fmt.Errorf("workspace: inspect %s: %w", s.Path(), err)
	}
	if info.Size() > maximumSize {
		return Session{}, fmt.Errorf("workspace: session exceeds %d bytes", maximumSize)
	}

	decoder := json.NewDecoder(io.LimitReader(file, maximumSize))
	decoder.DisallowUnknownFields()
	var session Session
	if err := decoder.Decode(&session); err != nil {
		return Session{}, fmt.Errorf("workspace: decode %s: %w", s.Path(), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Session{}, fmt.Errorf("workspace: decode %s: multiple JSON values", s.Path())
		}
		return Session{}, fmt.Errorf("workspace: decode %s: %w", s.Path(), err)
	}
	if session.Version != CurrentVersion {
		return Session{}, fmt.Errorf("workspace: unsupported session version %d", session.Version)
	}
	if s.SessionID != "" && session.ID != "" && session.ID != s.SessionID {
		return Session{}, fmt.Errorf("workspace: session ID %q does not match directory %q", session.ID, s.SessionID)
	}
	return cloneSession(session), nil
}

func (s Store) Save(session Session) error {
	session = cloneSession(session)
	session.Version = CurrentVersion
	if s.SessionID != "" {
		session.ID = s.SessionID
	}
	body, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("workspace: encode session: %w", err)
	}
	if len(body) > maximumSize {
		return fmt.Errorf("workspace: session exceeds %d bytes", maximumSize)
	}
	body = append(body, '\n')
	return withLoomRootMutation(s.Root, func() error {
		if err := os.MkdirAll(s.SessionDir(), 0o700); err != nil {
			return fmt.Errorf("workspace: create %s: %w", s.SessionDir(), err)
		}
		if err := os.Chmod(s.SessionDir(), 0o700); err != nil {
			return fmt.Errorf("workspace: secure %s: %w", s.SessionDir(), err)
		}
		file, err := os.CreateTemp(s.SessionDir(), ".session-*.json")
		if err != nil {
			return fmt.Errorf("workspace: create temporary session: %w", err)
		}
		temporaryPath := file.Name()
		keep := false
		defer func() {
			if !keep {
				_ = os.Remove(temporaryPath)
			}
		}()
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: secure temporary session: %w", err)
		}
		if _, err := file.Write(body); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: write temporary session: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: sync temporary session: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("workspace: close temporary session: %w", err)
		}
		if err := replaceFile(temporaryPath, s.Path()); err != nil {
			return fmt.Errorf("workspace: replace %s: %w", s.Path(), err)
		}
		keep = true
		return nil
	})
}

func (s Store) Clear() error {
	return withLoomRootMutation(s.Root, func() error {
		err := os.Remove(s.Path())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("workspace: remove %s: %w", s.Path(), err)
		}
		return nil
	})
}

// ListSessions returns all valid session projections below .q/sessions.
func (s Store) ListSessions() ([]SessionEntry, error) {
	entries, err := os.ReadDir(s.SessionsDir())
	if errors.Is(err, os.ErrNotExist) {
		return []SessionEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace: list %s: %w", s.SessionsDir(), err)
	}
	result := make([]SessionEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || validateSessionID(entry.Name()) != nil {
			continue
		}
		store := Store{Root: s.Root, SessionID: entry.Name()}
		session, loadErr := store.Load()
		if errors.Is(loadErr, ErrNotFound) {
			continue
		}
		if loadErr != nil {
			return nil, loadErr
		}
		updatedAt := time.Time{}
		if session.UpdatedAt != nil {
			updatedAt = session.UpdatedAt.UTC()
		}
		if updatedAt.IsZero() {
			info, statErr := os.Stat(store.Path())
			if statErr != nil {
				return nil, fmt.Errorf("workspace: inspect %s: %w", store.Path(), statErr)
			}
			updatedAt = info.ModTime().UTC()
		}
		result = append(result, SessionEntry{
			Store: store, RunID: session.RunID, Title: session.Title, UpdatedAt: updatedAt,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].Store.SessionID < result[j].Store.SessionID
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result, nil
}

// ClearSession removes the persisted conversation and execution projection for
// one session. Its lock file is intentionally retained as reusable metadata.
func (s Store) ClearSession() error {
	if s.SessionID == "" {
		return errors.New("workspace: a session ID is required")
	}
	// Remove the execution checkpoint first so a partial failure keeps the
	// session discoverable and a later delete can safely retry both removals.
	if err := s.ClearExecution(); err != nil {
		return err
	}
	return s.Clear()
}

func validateSessionID(value string) error {
	if value == "" {
		return errors.New("workspace: session ID is required")
	}
	if len(value) > 128 {
		return errors.New("workspace: session ID exceeds 128 bytes")
	}
	if value == "." || value == ".." || filepath.Base(value) != value {
		return errors.New("workspace: invalid session ID")
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return errors.New("workspace: invalid session ID")
	}
	return nil
}

func cloneSession(session Session) Session {
	session.Transcript = append([]client.Message(nil), session.Transcript...)
	session.Context = append([]client.Message(nil), session.Context...)
	session.Learning = session.Learning.Clone()
	if session.UpdatedAt != nil {
		updatedAt := *session.UpdatedAt
		session.UpdatedAt = &updatedAt
	}
	if session.ActiveTask != nil {
		active := *session.ActiveTask
		active.CompletionCriteria = append([]string(nil), active.CompletionCriteria...)
		session.ActiveTask = &active
	}
	return session
}
