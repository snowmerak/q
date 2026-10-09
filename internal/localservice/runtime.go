// Package localservice shares foreground service monitoring without owning
// service-specific startup, health contracts, or resources.
package localservice

import (
	"context"
	"time"
)

// Runtime supplies the lifecycle of either a local leader or a remote follower.
// Close must be safe to call more than once.
type Runtime interface {
	IsLeader() bool
	Done() <-chan struct{}
	Close() error
}

// Run reacquires a service after its leader exits or its follower loses health.
// Ensure owns leader election; waitForFailure returns nil on cancellation.
func Run[T Runtime](
	ctx context.Context,
	ensure func(context.Context) (T, error),
	waitForFailure func(context.Context, T) error,
	announce func(T) error,
) error {
	announced := false
	for ctx.Err() == nil {
		runtime, err := ensure(ctx)
		if err != nil {
			return err
		}
		if !announced {
			if err := announce(runtime); err != nil {
				_ = runtime.Close()
				return err
			}
			announced = true
		}
		if runtime.IsLeader() {
			select {
			case <-ctx.Done():
				return runtime.Close()
			case <-runtime.Done():
				_ = runtime.Close()
			}
		} else if waitForFailure(ctx, runtime) == nil {
			_ = runtime.Close()
			return nil
		}
		_ = runtime.Close()
	}
	return nil
}

// WaitForFailure polls health until it fails or the caller cancels. Health owns
// its probe timeout and returns its service-specific incompatibility error.
func WaitForFailure(ctx context.Context, interval time.Duration, health func(context.Context) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := health(ctx); err != nil {
				return err
			}
		}
	}
}
