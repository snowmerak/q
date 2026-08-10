// Package worklock provides process-level exclusive ownership of q workspace
// metadata without depending on higher-level workspace state types.
package worklock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	directoryName   = ".q"
	lockFileName    = "workspace.lock"
	metadataLimit   = 16 << 10
	metadataVersion = 1
)

// ErrLocked means another q process currently owns this workspace. The lock
// file may remain after that process exits; ownership is determined solely by
// the operating-system lock held on its open file handle.
var ErrLocked = errors.New("q workspace is already open in another process")

// Owner is diagnostic metadata written by the process holding a lock. It is
// never used to decide whether the lock is active.
type Owner struct {
	Version   int       `json:"version"`
	PID       int       `json:"pid"`
	Hostname  string    `json:"hostname,omitempty"`
	Command   string    `json:"command,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// Error reports active ownership by another process.
type Error struct {
	Path  string
	Owner *Owner
}

func (e *Error) Error() string {
	if e == nil {
		return ErrLocked.Error()
	}
	detail := ""
	if e.Owner != nil {
		parts := make([]string, 0, 3)
		if e.Owner.PID > 0 {
			parts = append(parts, fmt.Sprintf("pid %d", e.Owner.PID))
		}
		if e.Owner.Hostname != "" {
			parts = append(parts, "host "+e.Owner.Hostname)
		}
		if e.Owner.Command != "" {
			parts = append(parts, "command "+e.Owner.Command)
		}
		if len(parts) > 0 {
			detail = " (" + strings.Join(parts, ", ") + ")"
		}
	}
	return ErrLocked.Error() + detail
}

func (e *Error) Unwrap() error { return ErrLocked }

// Lock holds exclusive writer ownership of one workspace for the lifetime of
// its open file handle.
type Lock struct {
	root   string
	path   string
	owner  Owner
	file   *os.File
	once   sync.Once
	active atomic.Bool
	err    error
}

// Acquire takes exclusive writer ownership of root. A stale lock file is
// harmless: abnormal process termination closes the owning handle and the OS
// releases the actual lock automatically.
func Acquire(root, command string) (*Lock, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("workspace: resolve lock root: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("workspace: inspect lock root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("workspace: lock root is not a directory")
	}
	directory := filepath.Join(absolute, directoryName)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("workspace: create lock directory: %w", err)
	}
	_ = os.Chmod(directory, 0o700)
	path := filepath.Join(directory, lockFileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("workspace: open lock %s: %w", path, err)
	}
	_ = file.Chmod(0o600)
	locked, err := tryLockFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("workspace: acquire lock %s: %w", path, err)
	}
	if !locked {
		owner := readOwner(file)
		_ = file.Close()
		return nil, &Error{Path: path, Owner: owner}
	}

	hostname, _ := os.Hostname()
	owner := Owner{
		Version: metadataVersion, PID: os.Getpid(), Hostname: hostname,
		Command: strings.TrimSpace(command), StartedAt: time.Now().UTC(),
	}
	if err := writeOwner(file, owner); err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("workspace: write lock owner: %w", err)
	}
	lock := &Lock{root: absolute, path: path, owner: owner, file: file}
	lock.active.Store(true)
	return lock, nil
}

func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func (l *Lock) Owner() Owner {
	if l == nil {
		return Owner{}
	}
	return l.owner
}

// Owns reports whether this live lock protects root.
func (l *Lock) Owns(root string) bool {
	if l == nil || !l.active.Load() {
		return false
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	ownedInfo, ownedErr := os.Stat(l.root)
	candidateInfo, candidateErr := os.Stat(absolute)
	if ownedErr == nil && candidateErr == nil {
		return os.SameFile(ownedInfo, candidateInfo)
	}
	return filepath.Clean(l.root) == filepath.Clean(absolute)
}

// Close releases the OS lock. The metadata file intentionally remains so the
// next process can reuse it; file existence never represents ownership.
func (l *Lock) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.file == nil {
			return
		}
		l.err = errors.Join(unlockFile(l.file), l.file.Close())
		l.active.Store(false)
		l.file = nil
	})
	return l.err
}

func readOwner(file *os.File) *Owner {
	if file == nil {
		return nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	var owner Owner
	decoder := json.NewDecoder(io.LimitReader(file, metadataLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&owner); err != nil || owner.Version != metadataVersion || owner.PID < 1 {
		return nil
	}
	return &owner
}

func writeOwner(file *os.File, owner Owner) error {
	body, err := json.MarshalIndent(owner, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > metadataLimit {
		return errors.New("lock owner metadata is too large")
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		return err
	}
	return file.Sync()
}
