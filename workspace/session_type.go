package workspace

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/snowmerak/q/worklock"
)

// SessionType selects the workflow; it never grants tools by itself.
type SessionType string

var ErrWorkflowRequired = errors.New("workspace: use the session's workflow endpoint")

const (
	Interaction SessionType = "interaction"
	Council     SessionType = "council"
	Composer    SessionType = "composer"
)

func (t SessionType) Effective() SessionType {
	if t == "" {
		return Interaction
	}
	return t
}
func (t SessionType) Validate() error {
	switch t.Effective() {
	case Interaction, Council, Composer:
		return nil
	default:
		return fmt.Errorf("workspace: unsupported session type %q", t)
	}
}

// ListInteractionSessions keeps ordinary CLI/ACP entry points from opening a
// typed workflow with the default workspace tool policy.
func (s Store) ListInteractionSessions() ([]SessionEntry, error) {
	entries, err := s.ListSessions()
	if err != nil {
		return nil, err
	}
	result := []SessionEntry{}
	for _, entry := range entries {
		if entry.Type.Effective() == Interaction {
			result = append(result, entry)
		}
	}
	return result, nil
}

// AcquireLock follows the actual projection address, including internal seats.
func (s Store) AcquireLock(command string) (*Lock, error) {
	if s.nestedDir == "" {
		return AcquireSessionLock(s.Root, s.SessionID, command)
	}
	if err := rejectSymlinks(s.Root, s.SessionDir()); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(s.SessionDir()))
	return worklock.AcquireFile(s.Root, filepath.Join(DirectoryName, sessionLocksName, fmt.Sprintf("internal-%x.lock", sum)), command)
}

func (s Store) IsInternal() bool { return s.nestedDir != "" }

func (s Store) RequireInteraction() error {
	if err := validateSessionID(s.SessionID); err != nil {
		return err
	}
	value, err := s.Load()
	if err != nil {
		return err
	}
	if value.Type != Interaction {
		return ErrWorkflowRequired
	}
	return nil
}

// CouncilParticipant returns a host-owned child address inside this Council.
func (s Store) CouncilParticipant(id string) (Store, error) {
	if s.IsInternal() {
		return Store{}, errors.New("workspace: council parent must be a root session")
	}
	if err := validateSessionID(id); err != nil {
		return Store{}, err
	}
	parent, err := s.Load()
	if err != nil {
		return Store{}, err
	}
	if parent.Type != Council {
		return Store{}, errors.New("workspace: parent is not a council")
	}
	path := filepath.Join(s.SessionDir(), "council", "participants", id)
	if err := rejectSymlinks(s.Root, path); err != nil {
		return Store{}, err
	}
	return Store{Root: s.Root, SessionID: id, nestedDir: path}, nil
}

// OpenCouncilDirectory resolves a canonical parent supplied by the Council
// store. API clients cannot supply an arbitrary native context directory.
func OpenCouncilDirectory(directory string) (Store, error) {
	directory = filepath.Clean(directory)
	sessions := filepath.Dir(directory)
	metadata := filepath.Dir(sessions)
	if !filepath.IsAbs(directory) || filepath.Base(sessions) != SessionsName || filepath.Base(metadata) != DirectoryName {
		return Store{}, errors.New("workspace: invalid council session directory")
	}
	store, err := (Store{Root: filepath.Dir(metadata)}).ForSession(filepath.Base(directory))
	if err != nil {
		return Store{}, err
	}
	if err := rejectSymlinks(store.Root, store.SessionDir()); err != nil {
		return Store{}, err
	}
	value, err := store.Load()
	if err != nil {
		return Store{}, err
	}
	if value.Type != Council {
		return Store{}, errors.New("workspace: session is not a council")
	}
	return store, nil
}

// RemoveTypedSessionDirectory requires the caller to hold the workflow lock
// and release all native sessions first. It never removes the workspace root.
func (s Store) RemoveTypedSessionDirectory(kind SessionType) error {
	if kind != Council && kind != Composer {
		return errors.New("workspace: typed deletion requires council or composer")
	}
	value, err := s.Load()
	if err != nil {
		return err
	}
	if value.Type != kind || s.IsInternal() {
		return errors.New("workspace: session type does not match deletion")
	}
	if err := rejectSymlinks(s.Root, s.SessionDir()); err != nil {
		return err
	}
	lock, err := s.AcquireLock("q typed session delete")
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	return os.RemoveAll(s.SessionDir())
}
