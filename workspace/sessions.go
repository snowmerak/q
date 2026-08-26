package workspace

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/worklock"
)

const (
	sessionLocksName  = "session-locks"
	sessionLockSuffix = ".lock"
	migrationLockName = "session-migration.lock"
)

// AcquireSessionLock takes exclusive ownership of one session projection while
// allowing other sessions in the same workspace to remain open.
func AcquireSessionLock(root, sessionID, command string) (*Lock, error) {
	if err := validateSessionID(strings.TrimSpace(sessionID)); err != nil {
		return nil, err
	}
	relative := filepath.Join(DirectoryName, sessionLocksName, sessionID+sessionLockSuffix)
	return worklock.AcquireFile(root, relative, command)
}

// CreateSession creates, locks, and persists a new empty session.
func CreateSession(root, command string) (Store, *Lock, error) {
	for range 4 {
		sessionID, err := NewSessionID()
		if err != nil {
			return Store{}, nil, err
		}
		store := Store{Root: root, SessionID: sessionID}
		if _, err := os.Stat(store.Path()); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return Store{}, nil, fmt.Errorf("workspace: inspect new session path: %w", err)
		}
		lock, err := AcquireSessionLock(root, sessionID, command)
		if err != nil {
			if errors.Is(err, ErrLocked) {
				continue
			}
			return Store{}, nil, err
		}
		now := time.Now().UTC()
		if err := store.Save(Session{
			ID: sessionID, RunID: "run-" + sessionID, UpdatedAt: &now,
		}); err != nil {
			_ = lock.Close()
			return Store{}, nil, err
		}
		return store, lock, nil
	}
	return Store{}, nil, errors.New("workspace: could not allocate a unique session ID")
}

// OpenLatestSession resumes the newest inactive session, or creates a new one
// when the newest projection is active in another process.
func OpenLatestSession(root, command string) (Store, *Lock, error) {
	base := Store{Root: root}
	if err := base.MigrateLegacySession(); err != nil {
		return Store{}, nil, err
	}
	for range 4 {
		entries, err := base.ListSessions()
		if err != nil {
			return Store{}, nil, err
		}
		if len(entries) == 0 {
			break
		}
		store := entries[0].Store
		lock, lockErr := AcquireSessionLock(root, store.SessionID, command)
		if errors.Is(lockErr, ErrLocked) {
			return CreateSession(root, command)
		}
		if lockErr != nil {
			return Store{}, nil, lockErr
		}
		if _, loadErr := store.Load(); errors.Is(loadErr, ErrNotFound) {
			_ = lock.Close()
			continue
		} else if loadErr != nil {
			_ = lock.Close()
			return Store{}, nil, loadErr
		}
		return store, lock, nil
	}
	return CreateSession(root, command)
}

// DeleteSession removes one inactive session's conversation and execution
// projections. Durable archive records are intentionally preserved.
func DeleteSession(root, sessionID, command string) error {
	store, err := (Store{Root: root}).ForSession(sessionID)
	if err != nil {
		return err
	}
	lock, err := AcquireSessionLock(root, sessionID, command)
	if err != nil {
		return err
	}
	defer lock.Close()
	return store.ClearSession()
}

// MigrateLegacySession moves the former workspace-wide projections into one
// session directory. The short migration lock serializes upgrades without
// becoming a process-lifetime workspace lock.
func (s Store) MigrateLegacySession() (returnErr error) {
	legacy := Store{Root: s.Root}
	// Coordinate with older q builds that still own .q/workspace.lock for their
	// full lifetime, including before they have written their first projection.
	// This makes the layout upgrade a clean one-way handoff.
	var legacyLock *worklock.Lock
	var err error
	for attempt := 0; attempt < 100; attempt++ {
		legacyLock, err = worklock.Acquire(s.Root, "q session migration")
		if err == nil {
			break
		}
		if !errors.Is(err, worklock.ErrLocked) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	if legacyLock == nil {
		return fmt.Errorf("workspace: wait for legacy session owner: %w", err)
	}
	defer legacyLock.Close()

	_, sessionStatErr := os.Stat(legacy.Path())
	_, executionStatErr := os.Stat(legacy.ExecutionPath())
	if errors.Is(sessionStatErr, os.ErrNotExist) && errors.Is(executionStatErr, os.ErrNotExist) {
		return nil
	}
	if sessionStatErr != nil && !errors.Is(sessionStatErr, os.ErrNotExist) {
		return fmt.Errorf("workspace: inspect legacy session: %w", sessionStatErr)
	}
	if executionStatErr != nil && !errors.Is(executionStatErr, os.ErrNotExist) {
		return fmt.Errorf("workspace: inspect legacy plan execution: %w", executionStatErr)
	}

	var migrationLock *worklock.Lock
	for attempt := 0; attempt < 100; attempt++ {
		migrationLock, err = worklock.AcquireFile(
			s.Root, filepath.Join(DirectoryName, migrationLockName), "q session migration",
		)
		if err == nil {
			break
		}
		if !errors.Is(err, worklock.ErrLocked) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	if migrationLock == nil {
		return fmt.Errorf("workspace: wait for session migration: %w", err)
	}
	defer migrationLock.Close()

	session, sessionErr := legacy.Load()
	if sessionErr != nil && !errors.Is(sessionErr, ErrNotFound) {
		return sessionErr
	}
	execution, executionErr := legacy.LoadExecution()
	if executionErr != nil && !errors.Is(executionErr, ErrExecutionNotFound) {
		return executionErr
	}
	if errors.Is(sessionErr, ErrNotFound) && errors.Is(executionErr, ErrExecutionNotFound) {
		return nil
	}

	if sessionErr == nil && session.UpdatedAt == nil {
		updatedAt, err := projectionModTime(legacy.Path())
		if err != nil {
			return err
		}
		session.UpdatedAt = &updatedAt
	}

	targets := make([]legacyMigrationTarget, 0, 2)
	reserved := make(map[string]struct{}, 2)
	defer func() {
		for index := len(targets) - 1; index >= 0; index-- {
			returnErr = errors.Join(returnErr, targets[index].lock.Close())
		}
	}()

	if sessionErr == nil {
		var matchingExecution *subagent.ExecutionCheckpoint
		if executionErr == nil && session.RunID == execution.RunID {
			matchingExecution = &execution
		}
		target, err := prepareLegacyMigrationTarget(s.Root, session, matchingExecution, false, reserved)
		if err != nil {
			return err
		}
		targets = append(targets, target)
		reserved[target.store.SessionID] = struct{}{}
	}
	if executionErr == nil && (sessionErr != nil || session.RunID != execution.RunID) {
		updatedAt, err := projectionModTime(legacy.ExecutionPath())
		if err != nil {
			return err
		}
		planSession := Session{
			RunID: execution.RunID, Title: "Interrupted plan: " + strings.TrimSpace(execution.Objective), UpdatedAt: &updatedAt,
		}
		target, err := prepareLegacyMigrationTarget(s.Root, planSession, &execution, true, reserved)
		if err != nil {
			return err
		}
		targets = append(targets, target)
		reserved[target.store.SessionID] = struct{}{}
	}

	for index := range targets {
		target := &targets[index]
		if target.writeSession {
			if err := target.store.Save(target.session); err != nil {
				return err
			}
		}
		if target.execution != nil && target.writeExecution {
			if err := target.store.SaveExecution(*target.execution); err != nil {
				return err
			}
		}
	}
	if executionErr == nil {
		if err := legacy.ClearExecution(); err != nil {
			return err
		}
	}
	if sessionErr == nil {
		if err := legacy.Clear(); err != nil {
			return err
		}
	}
	return nil
}

type legacyMigrationTarget struct {
	store          Store
	session        Session
	execution      *subagent.ExecutionCheckpoint
	lock           *Lock
	writeSession   bool
	writeExecution bool
}

func prepareLegacyMigrationTarget(
	root string,
	session Session,
	execution *subagent.ExecutionCheckpoint,
	identityIncludesExecution bool,
	reserved map[string]struct{},
) (legacyMigrationTarget, error) {
	session = cloneSession(session)
	session.Version = CurrentVersion
	preferredID := preferredLegacySessionID(session)
	var identityExecution *subagent.ExecutionCheckpoint
	if identityIncludesExecution {
		identityExecution = execution
	}
	if preferredID == "" {
		var err error
		preferredID, err = deterministicLegacyProjectionID(session, identityExecution, 0)
		if err != nil {
			return legacyMigrationTarget{}, err
		}
	}
	if session.RunID == "" {
		session.RunID = "run-" + preferredID
	}

	for collision := 0; collision < 64; collision++ {
		sessionID := preferredID
		if collision > 0 {
			var err error
			sessionID, err = deterministicLegacyProjectionID(session, identityExecution, collision)
			if err != nil {
				return legacyMigrationTarget{}, err
			}
		}
		if _, exists := reserved[sessionID]; exists {
			continue
		}
		candidate := Store{Root: root, SessionID: sessionID}
		desired := cloneSession(session)
		desired.ID = sessionID
		compatible, _, _, err := inspectLegacyMigrationTarget(candidate, desired, execution)
		if err != nil {
			return legacyMigrationTarget{}, err
		}
		if !compatible {
			continue
		}

		lock, err := acquireMigrationSessionLock(root, sessionID)
		if err != nil {
			return legacyMigrationTarget{}, err
		}
		compatible, writeSession, writeExecution, err := inspectLegacyMigrationTarget(candidate, desired, execution)
		if err != nil {
			_ = lock.Close()
			return legacyMigrationTarget{}, err
		}
		if !compatible {
			_ = lock.Close()
			continue
		}
		return legacyMigrationTarget{
			store: candidate, session: desired, execution: execution, lock: lock,
			writeSession: writeSession, writeExecution: writeExecution,
		}, nil
	}
	return legacyMigrationTarget{}, errors.New("workspace: could not allocate a deterministic legacy session target")
}

func inspectLegacyMigrationTarget(
	store Store,
	desired Session,
	execution *subagent.ExecutionCheckpoint,
) (compatible, writeSession, writeExecution bool, returnErr error) {
	existing, err := store.Load()
	sessionMissing := errors.Is(err, ErrNotFound)
	if err != nil && !sessionMissing {
		return false, false, false, err
	}
	if !sessionMissing && !sameMigrationSession(existing, desired) {
		return false, false, false, nil
	}

	existingExecution, executionErr := store.LoadExecution()
	executionMissing := errors.Is(executionErr, ErrExecutionNotFound)
	if executionErr != nil && !executionMissing {
		return false, false, false, executionErr
	}
	if execution != nil {
		if !executionMissing && !reflect.DeepEqual(existingExecution, *execution) {
			return false, false, false, nil
		}
	} else if sessionMissing && !executionMissing && existingExecution.RunID != desired.RunID {
		// Do not attach a new session to an unrelated orphan checkpoint.
		return false, false, false, nil
	}
	return true, sessionMissing, execution != nil && executionMissing, nil
}

func acquireMigrationSessionLock(root, sessionID string) (*Lock, error) {
	var lock *Lock
	var err error
	for attempt := 0; attempt < 100; attempt++ {
		lock, err = AcquireSessionLock(root, sessionID, "q session migration")
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, ErrLocked) {
			return nil, err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil, fmt.Errorf("workspace: wait for migrated session %q: %w", sessionID, err)
}

func preferredLegacySessionID(session Session) string {
	for _, candidate := range []string{
		strings.TrimSpace(session.ID),
		strings.TrimPrefix(strings.TrimSpace(session.RunID), "run-"),
	} {
		if validateSessionID(candidate) == nil {
			return candidate
		}
	}
	return ""
}

func sameMigrationSession(left, right Session) bool {
	left = cloneSession(left)
	right = cloneSession(right)
	left.Version, right.Version = CurrentVersion, CurrentVersion
	left.ID, right.ID = "", ""
	return reflect.DeepEqual(left, right)
}

func projectionModTime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("workspace: inspect legacy projection %s: %w", path, err)
	}
	return info.ModTime().UTC(), nil
}

func deterministicLegacyProjectionID(
	session Session,
	execution *subagent.ExecutionCheckpoint,
	collision int,
) (string, error) {
	stable := cloneSession(session)
	stable.Version = CurrentVersion
	stable.ID = ""
	stable.UpdatedAt = nil
	body, err := json.Marshal(struct {
		Session   Session                       `json:"session"`
		Execution *subagent.ExecutionCheckpoint `json:"execution,omitempty"`
		Collision int                           `json:"collision"`
	}{Session: stable, Execution: execution, Collision: collision})
	if err != nil {
		return "", fmt.Errorf("workspace: encode legacy session identity: %w", err)
	}
	value := sha256.Sum256(body)
	value[6] = (value[6] & 0x0f) | 0x50
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	), nil
}

func deterministicLegacySessionID(session Session) (string, error) {
	return deterministicLegacyProjectionID(session, nil, 0)
}
