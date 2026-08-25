// Package workspace persists active q state for the directory where q is run.
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/thinker"
)

const (
	CurrentVersion = 1
	DirectoryName  = ".q"
	FileName       = "session.json"
	maximumSize    = 64 << 20
)

var ErrNotFound = errors.New("q workspace session not found")

type Session struct {
	Version    int                   `json:"version"`
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
	Root string
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

func (s Store) Path() string {
	return filepath.Join(s.Dir(), FileName)
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
	return cloneSession(session), nil
}

func (s Store) Save(session Session) error {
	session = cloneSession(session)
	session.Version = CurrentVersion
	body, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("workspace: encode session: %w", err)
	}
	if len(body) > maximumSize {
		return fmt.Errorf("workspace: session exceeds %d bytes", maximumSize)
	}
	body = append(body, '\n')
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return fmt.Errorf("workspace: create %s: %w", s.Dir(), err)
	}
	if err := os.Chmod(s.Dir(), 0o700); err != nil {
		return fmt.Errorf("workspace: secure %s: %w", s.Dir(), err)
	}
	file, err := os.CreateTemp(s.Dir(), ".session-*.json")
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
}

func (s Store) Clear() error {
	err := os.Remove(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace: remove %s: %w", s.Path(), err)
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
