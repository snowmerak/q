package main

import (
	"context"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/providerhost"
)

func TestStudioServiceRestartsOnlyForStartupSettings(t *testing.T) {
	directory := t.TempDir()
	providers := providerhost.Store{Dir: directory}
	settings := gatewayconfig.Store{Dir: directory}
	value := gatewayconfig.Default()
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	var starts atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		runStudioService(ctx, studioService{
			name:     "Gateway",
			paths:    []string{providers.Path(), settings.Path()},
			settings: func() (any, error) { return gatewayServiceSettings(providers, settings) },
			run: func(ctx context.Context) error {
				starts.Add(1)
				<-ctx.Done()
				return nil
			},
		}, io.Discard)
	}()
	defer func() { cancel(); <-finished }()
	waitStarts := func(want int32) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for starts.Load() != want {
			if time.Now().After(deadline) {
				t.Fatalf("starts = %d, want %d", starts.Load(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	settle := func(want int32) {
		t.Helper()
		time.Sleep(400 * time.Millisecond)
		if got := starts.Load(); got != want {
			t.Fatalf("starts = %d, want %d", got, want)
		}
	}
	waitStarts(1)

	// The running Gateway reloads its API keys itself.
	if _, _, err := settings.CreateAPIKey(value, "desktop", time.Now()); err != nil {
		t.Fatal(err)
	}
	settle(1)

	// Invalid settings leave the running server in place.
	if err := os.WriteFile(settings.Path(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	settle(1)

	value.Server.Port++
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	waitStarts(2)
	settle(2)
}
