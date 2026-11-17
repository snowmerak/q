package main

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/snowmerak/q/internal/studioprocess"
)

func TestDesktopOwnerAttachmentAndParentExit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	start := func() (studioprocess.Endpoint, io.Closer, <-chan error) {
		input, writer := io.Pipe()
		reader, output := io.Pipe()
		done := make(chan error, 1)
		go func() {
			err := runStudioDesktop(t.Context(), input, output, io.Discard)
			_ = output.CloseWithError(err)
			done <- err
		}()
		if _, err := io.WriteString(writer, "{\"protocol\":1}\n"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = writer.Close(); _ = reader.Close(); _ = input.Close() })
		var endpoint studioprocess.Endpoint
		if err := json.NewDecoder(reader).Decode(&endpoint); err != nil {
			t.Fatal(err)
		}
		return endpoint, writer, done
	}
	owner, parent, done := start()
	if !owner.Owned || !owner.Secure {
		t.Fatalf("desktop owner: %#v", owner)
	}
	borrowed, attachment, attachedDone := start()
	if borrowed.Owned || borrowed.Instance != owner.Instance {
		t.Fatalf("duplicate desktop engine: %#v", borrowed)
	}
	_ = attachment.Close()
	select {
	case err := <-attachedDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("attachment did not close")
	}
	if err := owner.Check(t.Context()); err != nil {
		t.Fatalf("attachment stopped owner: %v", err)
	}
	response, err := owner.Request(t.Context(), http.MethodGet, "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal(response.StatusCode)
	}
	_ = parent.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("parent exit did not stop owned engine")
	}
	if err := owner.Check(t.Context()); err == nil {
		t.Fatal("owned engine survived parent exit")
	}
}
