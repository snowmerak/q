package hostruntime

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/usagelog"
	"github.com/snowmerak/q/workspacememory"
)

func TestRuntimeCloseIsBoundedAndIdempotent(t *testing.T) {
	directory := t.TempDir()
	ports := reservePorts(t, 2)
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

	runtime, err := Open(t.Context(), Options{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runtime.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime close did not finish")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestOpenCleansUpStartedServicesWhenManagerCreationFails(t *testing.T) {
	want := errors.New("manager unavailable")
	libraryStopped := make(chan struct{})
	memoryStopped := make(chan struct{})
	waitForCancellation := func(stopped chan<- struct{}) func(context.Context, string, io.Writer) error {
		return func(ctx context.Context, _ string, _ io.Writer) error {
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		}
	}
	dependencies := dependencies{
		runLibrary: waitForCancellation(libraryStopped),
		runMemory:  waitForCancellation(memoryStopped),
		newManager: func(context.Context, providerhost.Store) (*providerhost.Manager, error) {
			return nil, want
		},
		newRecorder: usagelog.New,
	}
	runtime, err := open(t.Context(), Options{Directory: t.TempDir()}, dependencies)
	if runtime != nil || !errors.Is(err, want) {
		t.Fatalf("runtime = %v, error = %v", runtime, err)
	}
	for name, stopped := range map[string]<-chan struct{}{
		"Library": libraryStopped, "Workspace Memory": memoryStopped,
	} {
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatalf("%s was not stopped", name)
		}
	}
}

func reservePorts(t *testing.T, count int) []int {
	t.Helper()
	listeners := make([]net.Listener, count)
	ports := make([]int, count)
	for index := range listeners {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners[index] = listener
		ports[index] = listener.Addr().(*net.TCPAddr).Port
	}
	for _, listener := range listeners {
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return ports
}
