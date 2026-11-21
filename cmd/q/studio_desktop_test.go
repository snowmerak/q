package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/internal/studioprocess"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/systemoneconfig"
	"github.com/snowmerak/q/workspacememory"
)

func TestDesktopOwnerAttachmentAndParentExit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	directory := filepath.Join(home, ".q")
	// Never attach this disposable profile to the user's running services.
	if err := (qlibrary.ConfigStore{Dir: directory}).Save(qlibrary.Config{Host: "127.0.0.1", Port: availableUsagePort(t)}); err != nil {
		t.Fatal(err)
	}
	if err := (workspacememory.ConfigStore{Dir: directory}).Save(workspacememory.Config{Host: "127.0.0.1", Port: availableUsagePort(t)}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			http.NotFound(writer, request)
			return
		}
		_, _ = io.WriteString(writer, `{"models":[{"name":"jev"}]}`)
	}))
	t.Cleanup(upstream.Close)
	decisions := systemoneconfig.Store{Dir: directory}
	settings := systemoneconfig.Default()
	settings.Providers[0] = systemoneconfig.ProviderConfig{ID: "fixture", URI: upstream.URL + "/v1/systemone"}
	settings.DefaultModel = "fixture/jev"
	if err := decisions.Save(settings); err != nil {
		t.Fatal(err)
	}
	engineDiagnostics := newNotifyWriter()
	start := func() (studioprocess.Endpoint, io.Closer, <-chan error) {
		input, writer := io.Pipe()
		reader, output := io.Pipe()
		done := make(chan error, 1)
		go func() {
			err := runStudioDesktop(t.Context(), input, output, engineDiagnostics)
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
	client := &http.Client{Timeout: time.Second}
	t.Cleanup(client.CloseIdleConnections)
	waitForDecisionsModel := func(model string) string {
		t.Helper()
		const listening = "q decisions listening on "
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			output := engineDiagnostics.String()
			index := strings.LastIndex(output, listening)
			if index < 0 {
				continue
			}
			baseURL, _, _ := strings.Cut(output[index+len(listening):], "\n")
			url := baseURL + "/models"
			response, err := client.Get(url)
			if err != nil {
				continue
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err == nil && response.StatusCode == http.StatusOK && strings.Contains(string(body), `"`+model+`"`) {
				return url
			}
		}
		t.Fatalf("Decisions did not serve model %q: %s", model, engineDiagnostics.String())
		return ""
	}
	waitForDecisionsModel(settings.DefaultModel)
	settings.Providers[0].ID = "updated"
	settings.DefaultModel = "updated/jev"
	if err := decisions.Save(settings); err != nil {
		t.Fatal(err)
	}
	decisionsURL := waitForDecisionsModel(settings.DefaultModel)
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
	if response, err := client.Get(decisionsURL); err == nil {
		_ = response.Body.Close()
		t.Fatal("Decisions survived parent exit")
	}
}
