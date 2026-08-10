package workspace

import "github.com/snowmerak/q/worklock"

var ErrLocked = worklock.ErrLocked

type LockOwner = worklock.Owner
type LockError = worklock.Error
type Lock = worklock.Lock

func AcquireLock(root, command string) (*Lock, error) {
	return worklock.Acquire(root, command)
}
