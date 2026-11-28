package main

import (
	"context"
	"testing"
	"time"
)

func TestReloadPreservesAuthenticationWhileReplacementServes(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	runtime := &gatewayRuntime{current: &gatewayGeneration{waitAuthentication: func(ctx context.Context) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}}
	t.Cleanup(func() { _ = runtime.Close() })
	upstream := modelServer(t, "replacement-model")
	defer upstream.Close()
	done := make(chan error, 1)
	go func() { _, err := runtime.Reload(t.Context(), runtimeConfig(upstream.URL)); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("authentication was not drained")
	}
	assertRuntimeModel(t, runtime, "local/replacement-model")
	select {
	case err := <-done:
		t.Fatalf("callback owner retired during sign-in: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("completed sign-in did not release retired generation")
	}
}
