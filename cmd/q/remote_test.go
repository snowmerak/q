package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workspacememory"
)

type notifyWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	wrote  chan struct{}
}

func newNotifyWriter() *notifyWriter { return &notifyWriter{wrote: make(chan struct{}, 1)} }

func (w *notifyWriter) Write(body []byte) (int, error) {
	w.mu.Lock()
	count, err := w.buffer.Write(body)
	w.mu.Unlock()
	select {
	case w.wrote <- struct{}{}:
	default:
	}
	return count, err
}

func (w *notifyWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func TestRemoteServiceHealthAndShutdownSmoke(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	output := newNotifyWriter()
	diagnostics := newNotifyWriter()
	done := make(chan error, 1)
	store := config.Store{Dir: t.TempDir()}
	configureRemoteTestServices(t, store.Dir)
	go func() {
		done <- runRemoteWithStore(ctx, store, output, diagnostics)
	}()

	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	var line string
	for line == "" {
		select {
		case <-output.wrote:
			line = strings.TrimSpace(output.String())
		case err := <-done:
			t.Fatalf("q remote stopped before readiness: %v; diagnostics: %s", err, diagnostics.String())
		case <-deadline.C:
			t.Fatalf("q remote did not report readiness; diagnostics: %s", diagnostics.String())
		}
	}
	prefix := "q remote listening on "
	address, _, found := strings.Cut(strings.TrimPrefix(line, prefix), " · ")
	if !strings.HasPrefix(line, prefix) || !found {
		t.Fatalf("readiness line = %q", line)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(strings.TrimSuffix(address, "/v1") + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read health: %v; close: %v", readErr, closeErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v; diagnostics: %s", err, diagnostics.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("q remote did not shut down")
	}
}

func configureRemoteTestServices(t *testing.T, directory string) {
	t.Helper()
	listeners := make([]net.Listener, 2)
	for index := range listeners {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners[index] = listener
	}
	ports := []int{
		listeners[0].Addr().(*net.TCPAddr).Port,
		listeners[1].Addr().(*net.TCPAddr).Port,
	}
	for _, listener := range listeners {
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := (qlibrary.ConfigStore{Dir: directory}).Save(qlibrary.Config{
		Version: qlibrary.ConfigVersion, Host: qlibrary.DefaultHost, Port: ports[0],
	}); err != nil {
		t.Fatal(err)
	}
	if err := (workspacememory.ConfigStore{Dir: directory}).Save(workspacememory.Config{
		Version: workspacememory.ConfigVersion, Host: workspacememory.DefaultHost, Port: ports[1],
	}); err != nil {
		t.Fatal(err)
	}
}
