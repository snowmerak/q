package app

import (
	"errors"
	"testing"

	"github.com/snowmerak/q/workspace"
)

func TestEmbeddingTransitionKeepsNewSessionAdmissionClosed(t *testing.T) {
	host := &SessionHost{ctx: t.Context()}
	entered, finish := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- host.withIdleSessions(t.Context(), func() error {
			close(entered)
			<-finish
			return nil
		})
	}()
	<-entered
	_, err := host.beginLiveSessionOperation("new-session")
	if !errors.Is(err, workspace.ErrLocked) {
		t.Errorf("new session entered during migration: %v", err)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	operation, err := host.beginLiveSessionOperation("new-session")
	if err != nil {
		t.Fatalf("admission did not reopen: %v", err)
	}
	host.finishLiveSessionOperation("new-session", operation)
	operation.cancel()
}
