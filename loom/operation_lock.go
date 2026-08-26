package loom

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	operationLockFileName      = "operation.lock"
	operationLockRetryInterval = 5 * time.Millisecond
)

type operationFileLock struct {
	file *os.File
}

type operationContextKey struct{}

func withStoreOperation[T any](ctx context.Context, store *Store, operation func(context.Context) (T, error)) (result T, err error) {
	if ctx == nil {
		return result, errors.New("loom: operation context is nil")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	lockPath := filepath.Join(store.root, operationLockFileName)
	if operationContextOwns(ctx, lockPath) {
		return operation(ctx)
	}
	select {
	case <-ctx.Done():
		return result, ctx.Err()
	case <-store.operationGate:
	}
	defer func() { store.operationGate <- struct{}{} }()
	lock, err := acquireOperationFileLock(ctx, lockPath)
	if err != nil {
		return result, err
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("loom: release operation lock: %w", closeErr))
		}
	}()
	return operation(context.WithValue(ctx, operationContextKey{}, filepath.Clean(lockPath)))
}

// WithWorkspaceOperation coordinates a workspace projection mutation with
// Loom operations and GC. Callers that can add or remove Loom roots must wrap
// their atomic filesystem update in this lease. The callback must perform the
// projection mutation directly; it must not call a workspace Store mutation
// that acquires this lease with a fresh context. Loom operations are reentrant
// when they receive the callback context.
func WithWorkspaceOperation(ctx context.Context, workspaceRoot string, operation func(context.Context) error) (err error) {
	if ctx == nil {
		return errors.New("loom: operation context is nil")
	}
	if operation == nil {
		return errors.New("loom: workspace operation is nil")
	}
	root := filepath.Join(workspaceRoot, ".q", DirectoryName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("loom: create %s: %w", root, err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("loom: secure %s: %w", root, err)
	}
	lockPath := filepath.Join(root, operationLockFileName)
	if operationContextOwns(ctx, lockPath) {
		return operation(ctx)
	}
	lock, err := acquireOperationFileLock(ctx, lockPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("loom: release operation lock: %w", closeErr))
		}
	}()
	return operation(context.WithValue(ctx, operationContextKey{}, filepath.Clean(lockPath)))
}

func operationContextOwns(ctx context.Context, path string) bool {
	owned, _ := ctx.Value(operationContextKey{}).(string)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(owned), filepath.Clean(path))
	}
	return owned != "" && filepath.Clean(owned) == filepath.Clean(path)
}

func acquireOperationFileLock(ctx context.Context, path string) (*operationFileLock, error) {
	if ctx == nil {
		return nil, errors.New("loom: operation context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("loom: open operation lock: %w", err)
	}
	_ = file.Chmod(0o600)
	for {
		locked, lockErr := tryOperationFileLock(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("loom: acquire operation lock: %w", lockErr)
		}
		if locked {
			if err := ctx.Err(); err != nil {
				_ = unlockOperationFile(file)
				_ = file.Close()
				return nil, err
			}
			return &operationFileLock{file: file}, nil
		}

		timer := time.NewTimer(operationLockRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *operationFileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return errors.Join(unlockOperationFile(file), file.Close())
}
