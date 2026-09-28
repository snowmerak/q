package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type notifyWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func newNotifyWriter() *notifyWriter { return &notifyWriter{} }

func (writer *notifyWriter) Write(body []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buffer.Write(body)
}

func (writer *notifyWriter) String() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buffer.String()
}

func TestStudioServiceStatusAndShutdownSmoke(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	output := newNotifyWriter()
	diagnostics := newNotifyWriter()
	opened := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runStudio(ctx, nil, output, diagnostics, func(url string) error {
			opened <- url
			return nil
		})
	}()

	var url string
	select {
	case url = <-opened:
	case err := <-done:
		t.Fatalf("q studio stopped before readiness: %v; diagnostics: %s", err, diagnostics.String())
	case <-time.After(10 * time.Second):
		t.Fatalf("q studio did not report readiness; output: %s; diagnostics: %s", output.String(), diagnostics.String())
	}
	if !strings.Contains(output.String(), "q studio listening on "+url) {
		t.Fatalf("readiness output = %q", output.String())
	}

	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(url + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || strings.Contains(string(body), "workspace") {
		t.Fatalf("status = %d %q; read: %v; close: %v", response.StatusCode, body, readErr, closeErr)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v; diagnostics: %s", err, diagnostics.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("q studio did not shut down")
	}
}

func TestParseStudioOptions(t *testing.T) {
	options, err := parseStudioOptions([]string{"--port", "7070", "--no-open"}, io.Discard)
	if err != nil || options.port != 7070 || !options.noOpen {
		t.Fatalf("options = %#v, %v", options, err)
	}
	if _, err := parseStudioOptions([]string{"--port", "65536"}, io.Discard); err == nil {
		t.Fatal("invalid port was accepted")
	}
}
