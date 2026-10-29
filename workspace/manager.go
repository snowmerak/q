package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/worklock"
)

// ErrRequiredManager rejects removal while allowing ResetSession in place.
var ErrRequiredManager = errors.New("workspace: the project's required Coordinator or Integrator session cannot be deleted or unregistered; clear it instead")

// ManagerBinding is a local protection marker. Library remains the authority
// for the workspace, manager identity and execution directories.
type ManagerBinding struct {
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
}

// ManagerBinding reads the local protection marker for this root session.
func (s Store) ManagerBinding() (*ManagerBinding, error) {
	if s.IsInternal() {
		return nil, nil
	}
	body, err := os.ReadFile(filepath.Join(s.Root, DirectoryName, "manager.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var binding ManagerBinding
	if err := json.Unmarshal(body, &binding); err != nil {
		return nil, fmt.Errorf("workspace: read manager binding: %w", err)
	}
	if validateSessionID(binding.WorkspaceID) != nil || validateSessionID(binding.SessionID) != nil {
		return nil, errors.New("workspace: invalid manager binding")
	}
	if binding.SessionID != s.SessionID {
		// Windows can address the same session through a differently cased ID.
		// Protect the actual directory, including an existing filesystem alias.
		expected, expectedErr := os.Stat(filepath.Join(s.SessionsDir(), binding.SessionID))
		actual, actualErr := os.Stat(s.SessionDir())
		if expectedErr != nil && !errors.Is(expectedErr, os.ErrNotExist) {
			return nil, expectedErr
		}
		if actualErr != nil && !errors.Is(actualErr, os.ErrNotExist) {
			return nil, actualErr
		}
		if expectedErr != nil || actualErr != nil || !os.SameFile(expected, actual) {
			return nil, nil
		}
	}
	return &binding, nil
}

// RequireDeletable guards conversation deletion and catalog detachment.
func (s Store) RequireDeletable() error {
	binding, err := s.ManagerBinding()
	if err != nil {
		return err
	}
	if binding != nil {
		return ErrRequiredManager
	}
	return nil
}

// EnsureManagerSession materializes a Library-reserved identity without
// replacing an existing transcript. A separate lock protects first creation
// across processes; a running manager does not need to release its session lock.
func EnsureManagerSession(ctx context.Context, root, workspaceID, sessionID string, titles ...string) (store Store, returnErr error) {
	if err := validateSessionID(workspaceID); err != nil {
		return Store{}, err
	}
	store, err := (Store{Root: root}).ForSession(sessionID)
	if err != nil {
		return Store{}, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Store{}, err
	}
	var guard *worklock.Lock
	for {
		if err := ctx.Err(); err != nil {
			return Store{}, err
		}
		guard, err = worklock.AcquireFile(root, filepath.Join(DirectoryName, "manager-init.lock"), "q manager creation")
		if !errors.Is(err, worklock.ErrLocked) {
			break
		}
		select {
		case <-ctx.Done():
			return Store{}, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err != nil {
		return Store{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, guard.Close()) }()
	marker := filepath.Join(root, DirectoryName, "manager.json")
	if body, err := os.ReadFile(marker); err == nil {
		var previous ManagerBinding
		if json.Unmarshal(body, &previous) != nil || previous.WorkspaceID != workspaceID || previous.SessionID != sessionID {
			return Store{}, errors.New("workspace: manager binding conflicts with Library")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		body, err := json.Marshal(ManagerBinding{WorkspaceID: workspaceID, SessionID: sessionID})
		if err != nil {
			return Store{}, err
		}
		if err := fsreplace.WriteFile(marker, body, 0o600); err != nil {
			return Store{}, err
		}
	} else {
		return Store{}, err
	}
	if value, err := store.Load(); err == nil {
		if value.Type != Interaction {
			return Store{}, ErrWorkflowRequired
		}
		return store, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Store{}, err
	}
	lock, err := store.AcquireLock("q manager creation")
	if err != nil {
		return Store{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	now := time.Now().UTC()
	title := "Integrator"
	if len(titles) > 0 {
		title = titles[0]
	}
	return store, store.Save(Session{Type: Interaction, ID: sessionID, RunID: "run-" + sessionID, Title: title, UpdatedAt: &now})
}
