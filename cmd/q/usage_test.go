package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/usagelog"
)

func TestUsageCommandHostsAndOpensDashboard(t *testing.T) {
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if err := (usagelog.ConfigStore{Dir: dir}).Save(usagelog.Config{Version: usagelog.ConfigVersion, Host: "127.0.0.1", Port: port}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var output, errorOutput bytes.Buffer
	opened := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runUsageCommandWithStore(ctx, config.Store{Dir: dir}, &output, &errorOutput, func(url string) error {
			response, err := http.Get(url)
			if err != nil {
				return err
			}
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Token traffic") {
				return fmt.Errorf("dashboard status=%d body=%q readErr=%v", response.StatusCode, body, readErr)
			}
			opened <- url
			cancel()
			return nil
		})
	}()
	select {
	case url := <-opened:
		if !strings.Contains(output.String(), url) || errorOutput.Len() != 0 {
			t.Fatalf("stdout=%q stderr=%q", output.String(), errorOutput.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("usage command did not open the dashboard")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("usage command did not stop after cancellation")
	}
}

func TestUsageCommandKeepsMonitoringExistingLeader(t *testing.T) {
	dir := t.TempDir()
	configValue := usagelog.Config{Version: usagelog.ConfigVersion, Host: "127.0.0.1", Port: availableUsagePort(t)}
	if err := (usagelog.ConfigStore{Dir: dir}).Save(configValue); err != nil {
		t.Fatal(err)
	}
	leader, err := usagelog.Ensure(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- runUsageCommandWithStore(ctx, config.Store{Dir: dir}, io.Discard, io.Discard, func(string) error { return nil })
	}()
	select {
	case err := <-done:
		t.Fatalf("usage command stopped while the existing leader was healthy: %v", err)
	case <-time.After(750 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("usage command did not stop after cancellation")
	}
}

func availableUsagePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}
