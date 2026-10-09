package usagelog

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureCanceledRequestDoesNotStartIndependentLeader(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dir := filepath.Join(t.TempDir(), "unused")
	runtime, err := EnsureWithOptions(ctx, EnsureOptions{
		Dir: dir, Config: availableConfig(t), LeaderContext: t.Context(),
	})
	if runtime != nil {
		_ = runtime.Close()
		t.Fatal("canceled startup created a runtime")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled startup created directory: %v", err)
	}
}

func TestEnsureCancellationDuringProbeDoesNotBindOccupiedPort(t *testing.T) {
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-request.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		runtime, err := EnsureWithOptions(ctx, EnsureOptions{
			Dir: t.TempDir(), LeaderContext: t.Context(),
			Config: Config{Host: "127.0.0.1", Port: server.Listener.Addr().(*net.TCPAddr).Port},
		})
		if runtime != nil {
			_ = runtime.Close()
		}
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not stop")
	}
}
