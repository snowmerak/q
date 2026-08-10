package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const (
	lockHelperRoot  = "Q_TEST_WORKSPACE_LOCK_ROOT"
	lockHelperReady = "Q_TEST_WORKSPACE_LOCK_READY"
)

func TestWorkspaceLockRejectsSecondOwnerAndReusesStaleFile(t *testing.T) {
	root := t.TempDir()
	first, err := AcquireLock(root, "q first")
	if err != nil {
		t.Fatal(err)
	}
	if first.Owner().PID != os.Getpid() || first.Owner().Command != "q first" {
		t.Fatalf("owner = %#v", first.Owner())
	}

	second, err := AcquireLock(root, "q second")
	if second != nil {
		_ = second.Close()
		t.Fatal("second owner acquired an active workspace lock")
	}
	var lockErr *LockError
	if !errors.Is(err, ErrLocked) || !errors.As(err, &lockErr) {
		t.Fatalf("second AcquireLock() error = %v, want LockError", err)
	}
	if lockErr.Owner == nil || lockErr.Owner.PID != os.Getpid() || lockErr.Owner.Command != "q first" {
		t.Fatalf("contended owner = %#v", lockErr.Owner)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path()); err != nil {
		t.Fatalf("lock metadata file should remain after release: %v", err)
	}
	reopened, err := AcquireLock(root, "q reopened")
	if err != nil {
		t.Fatalf("AcquireLock() after release: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
}

func TestWorkspaceLockReleasedWhenOwnerProcessIsKilled(t *testing.T) {
	root := t.TempDir()
	ready := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestWorkspaceLockProcessHelper$")
	command.Env = append(os.Environ(), lockHelperRoot+"="+root, lockHelperReady+"="+ready)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	waitForFile(t, ready, 5*time.Second)
	contender, err := AcquireLock(root, "q parent")
	if contender != nil {
		_ = contender.Close()
		t.Fatal("parent acquired the helper's active workspace lock")
	}
	var lockErr *LockError
	if !errors.Is(err, ErrLocked) || !errors.As(err, &lockErr) {
		t.Fatalf("AcquireLock() while helper is alive = %v, want LockError", err)
	}
	if lockErr.Owner == nil || lockErr.Owner.PID != command.Process.Pid {
		t.Fatalf("helper owner = %#v, want pid %d", lockErr.Owner, command.Process.Pid)
	}

	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed helper unexpectedly exited successfully")
	}
	stopped = true

	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, acquireErr := AcquireLock(root, "q recovered")
		if acquireErr == nil {
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			break
		}
		if !errors.Is(acquireErr, ErrLocked) || time.Now().After(deadline) {
			t.Fatalf("AcquireLock() after helper termination = %v", acquireErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWorkspaceLockProcessHelper(t *testing.T) {
	root := os.Getenv(lockHelperRoot)
	if root == "" {
		return
	}
	lock, err := AcquireLock(root, "q test helper")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ready := os.Getenv(lockHelperReady)
	if err := os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
