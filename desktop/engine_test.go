package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProxyKeepsCredentialsOutOfFrontend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-token" || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
			t.Error("proxy did not replace caller credentials")
		}
		if r.URL.RequestURI() != "/api/v1/events?after=2" {
			t.Error(r.URL)
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'")
		_, _ = io.WriteString(w, "events")
	}))
	defer server.Close()
	host := newEngineHost("", io.Discard, slog.New(slog.DiscardHandler))
	host.process = &engineProcess{endpoint: engineEndpoint{URL: server.URL, Token: "private-token"}}
	req := httptest.NewRequest(http.MethodGet, "http://wails.localhost/api/v1/events?after=2", nil)
	req.Header.Set("Cookie", "unrelated=secret")
	req.Header.Set("Origin", "http://wails.localhost")
	req.Header.Set("Authorization", "Bearer attacker")
	response := httptest.NewRecorder()
	host.ServeHTTP(response, req)
	if response.Code != 200 || response.Body.String() != "events" || response.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("proxy: %d %s", response.Code, response.Body)
	}
	response = httptest.NewRecorder()
	host.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://wails.localhost"+controlPath+"launch", nil))
	if response.Code != 404 {
		t.Fatal("frontend can access engine control")
	}
}

func TestProxyLogsUnavailable(t *testing.T) {
	const headerTimeout = 30 * time.Millisecond
	connectionError := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	for _, test := range []struct {
		reason      string
		detail      string
		minDuration time.Duration
		setup       func(*testing.T, *engineHost)
	}{
		{
			reason: "connection_failed",
			detail: connectionError.Error(),
			setup: func(_ *testing.T, host *engineHost) {
				host.process = &engineProcess{endpoint: engineEndpoint{URL: "http://127.0.0.1:1"}}
				transport := host.proxy.Transport.(*http.Transport)
				transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
					return nil, connectionError
				}
			},
		},
		{
			reason:      "timeout",
			detail:      "net/http: timeout awaiting response headers",
			minDuration: headerTimeout,
			setup: func(t *testing.T, host *engineHost) {
				release := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					<-release
				}))
				t.Cleanup(server.Close)
				t.Cleanup(func() { close(release) })
				host.process = &engineProcess{endpoint: engineEndpoint{URL: server.URL}}
				host.proxy.Transport.(*http.Transport).ResponseHeaderTimeout = headerTimeout
			},
		},
		{reason: "engine_unavailable", detail: "no Q engine endpoint is connected"},
	} {
		t.Run(test.reason, func(t *testing.T) {
			var logs bytes.Buffer
			host := newEngineHost("", io.Discard, slog.New(slog.NewJSONHandler(&logs, nil)))
			if test.setup != nil {
				test.setup(t, host)
			}
			response := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://wails.localhost/api/v1/events?workspace_root=private-query", nil)
			req.Header.Set("Authorization", "Bearer private-header")
			host.ServeHTTP(response, req)
			if response.Code != http.StatusServiceUnavailable || !strings.HasPrefix(response.Body.String(), "Q engine reconnecting") {
				t.Fatalf("unexpected response: %d %s", response.Code, response.Body)
			}
			if strings.Contains(logs.String(), "private-query") || strings.Contains(logs.String(), "private-header") {
				t.Fatal("proxy logged request query or credentials")
			}
			var entry struct {
				Message    string `json:"msg"`
				Level      string `json:"level"`
				Method     string `json:"method"`
				Path       string `json:"path"`
				Status     int    `json:"status"`
				Reason     string `json:"reason_code"`
				Error      string `json:"error"`
				DurationMS *int64 `json:"duration_ms"`
			}
			if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
				t.Fatalf("decode proxy failure log: %v", err)
			}
			if entry.Message != "desktop.engine.proxy_failed" || entry.Level != "ERROR" || entry.Status != response.Code ||
				entry.Method != http.MethodGet || entry.Path != "/api/v1/events" || entry.Reason != test.reason || entry.Error != test.detail {
				t.Fatalf("unexpected proxy failure log: %s", logs.String())
			}
			if entry.DurationMS == nil || *entry.DurationMS < test.minDuration.Milliseconds() {
				t.Fatalf("missing or incorrect duration: %s", logs.String())
			}
		})
	}
}

func TestInvalidEngineReadiness(t *testing.T) {
	for _, address := range []string{"http://attacker.invalid:7070", "http://127.0.0.1:7070/path", "https://127.0.0.1:7070", "http://user:pass@127.0.0.1:7070"} {
		e := engineEndpoint{Protocol: 1, URL: address, Token: strings.Repeat("s", 32), Instance: "instance"}
		if e.validate() == nil {
			t.Error(address)
		}
	}
}

func TestDesktopSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	initial, err := loadSettings(dir)
	if err != nil || !initial.KeepRunning || !initial.StartHidden {
		t.Fatalf("defaults: %+v %v", initial, err)
	}
	want := desktopSettings{KeepRunning: false, StartHidden: true}
	for range 2 {
		if err = saveSettings(dir, want); err != nil {
			t.Fatal(err)
		}
	}
	got, err := loadSettings(dir)
	if err != nil || got != want {
		t.Fatalf("saved settings: %+v %v", got, err)
	}
}

// Set Q_STUDIO_TEST_ENGINE to the assembled q executable to verify the real
// stdio protocol, auth, attachment, and process containment together.
func TestEngineIntegration(t *testing.T) {
	path := os.Getenv("Q_STUDIO_TEST_ENGINE")
	if path == "" {
		t.Skip("Q_STUDIO_TEST_ENGINE is not set")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err = os.MkdirAll(filepath.Join(home, ".q"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"library.json", "workspace-memory.json"} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		body, _ := json.Marshal(map[string]any{"version": 1, "host": "127.0.0.1", "port": port})
		if err = os.WriteFile(filepath.Join(home, ".q", name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	owner, err := startEngine(ctx, path, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.close() })
	if !owner.endpoint.Owned || !owner.endpoint.Secure {
		t.Fatal("engine ownership or auth missing")
	}
	attached, err := startEngine(ctx, path, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if attached.endpoint.Owned || attached.endpoint.Instance != owner.endpoint.Instance {
		t.Fatal("engine duplicated")
	}
	if err = attached.close(); err != nil {
		t.Fatal(err)
	}
	host := newEngineHost(path, os.Stderr, slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	host.process = owner
	response := httptest.NewRecorder()
	host.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://wails.localhost/api/v1/status", nil))
	var status struct {
		Ready bool `json:"ready"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &status) != nil || !status.Ready {
		t.Fatalf("real proxy: %d %s", response.Code, response.Body)
	}
	// Overview loads this route, which initializes Library and bundled skills.
	// The lightweight status endpoint alone cannot detect a broken bundle.
	response = httptest.NewRecorder()
	host.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://wails.localhost/api/v1/registered-sessions", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("load Overview sessions and bundled skills: %d %s", response.Code, response.Body)
	}
	if _, err = host.browserURL(ctx); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	host.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "http://wails.localhost/api/v1/ui-state", strings.NewReader(`{"values":{"q-studio-theme":"night"}}`)))
	if response.Code != http.StatusNoContent {
		t.Fatalf("save UI state through desktop proxy: %d %s", response.Code, response.Body)
	}
	if err = owner.close(); err != nil {
		t.Fatal(err)
	}
	// close is intentionally safe to repeat during application teardown.
	response = httptest.NewRecorder()
	host.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://wails.localhost/api/v1/status", nil))
	if response.Code != 503 {
		t.Fatal("stopped engine still serving")
	}
	restarted, err := startEngine(ctx, path, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.close() })
	host.process = restarted
	response = httptest.NewRecorder()
	host.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://wails.localhost/api/v1/ui-state", nil))
	var ui struct {
		Values map[string]*string `json:"values"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &ui) != nil || ui.Values["q-studio-theme"] == nil || *ui.Values["q-studio-theme"] != "night" {
		t.Fatalf("restore UI state after engine restart: %d %s", response.Code, response.Body)
	}
}
