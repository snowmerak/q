// Package fsutil provides atomic file writes and OS-enforced single-instance
// file locks for q v2's global data directory.
package fsutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// ErrLocked indicates another live process already holds the instance lock.
var ErrLocked = errors.New("q data directory is already locked by another instance")

type LockOwner struct {
	PID          int   `json:"pid"`
	AcquiredAtMS int64 `json:"acquired_at_ms"`
}

// InstanceLock holds an exclusive OS lock on instance.lock until Release.
type InstanceLock struct {
	path   string
	owner  LockOwner
	file   *os.File
	once   sync.Once
	active atomic.Bool
}

// AcquireInstanceLock creates or opens path and acquires a non-blocking OS file lock.
func AcquireInstanceLock(path string) (*InstanceLock, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve lock path: %w", err)
	}
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	locked, err := tryLockFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire lock %s: %w", abs, err)
	}
	if !locked {
		_ = file.Close()
		return nil, fmt.Errorf("%w (%s)", ErrLocked, abs)
	}
	owner := LockOwner{
		PID:          os.Getpid(),
		AcquiredAtMS: time.Now().UTC().UnixMilli(),
	}
	if data, err := json.Marshal(owner); err == nil {
		_ = file.Truncate(0)
		_, _ = file.Seek(0, 0)
		_, _ = file.Write(append(data, '\n'))
		_ = file.Sync()
	}
	lock := &InstanceLock{path: abs, owner: owner, file: file}
	lock.active.Store(true)
	return lock, nil
}

func (l *InstanceLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Release unlocks and closes the underlying file handle. Safe to call multiple times.
func (l *InstanceLock) Release() error {
	if l == nil {
		return nil
	}
	var releaseErr error
	l.once.Do(func() {
		l.active.Store(false)
		if l.file != nil {
			unlockErr := unlockFile(l.file)
			closeErr := l.file.Close()
			releaseErr = errors.Join(unlockErr, closeErr)
			l.file = nil
		}
	})
	return releaseErr
}
