package qdriver_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	qdriver "github.com/snowmerak/q/v2/pkg/cdp"
)

func TestAttachValidation(t *testing.T) {
	for _, endpoint := range []string{"", "localhost:9222", "ftp://localhost:9222", "ws://localhost:9222/devtools/page/abc", "ws://localhost:9222/devtools/browser/"} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := qdriver.Attach(t.Context(), endpoint, qdriver.Options{}); err == nil {
				t.Fatal("accepted an invalid browser endpoint")
			}
		})
	}
	if _, err := qdriver.Attach(t.Context(), "http://127.0.0.1:9222", qdriver.Options{CleanupTimeout: -1}); err == nil {
		t.Fatal("accepted a negative timeout")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := qdriver.Attach(canceled, "http://127.0.0.1:9222", qdriver.Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled attachment: %v", err)
	}
}

func TestDiscoveryFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		code int
	}{
		{"HTTP failure", "", http.StatusServiceUnavailable},
		{"invalid JSON", "not JSON", http.StatusOK},
		{"missing WebSocket", `{}`, http.StatusOK},
		{"page WebSocket", `{"webSocketDebuggerUrl":"ws://localhost:9222/devtools/page/a"}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			if _, err := qdriver.Attach(t.Context(), server.URL, qdriver.Options{}); err == nil {
				t.Fatal("accepted invalid discovery")
			}
		})
	}
}

func TestDiscoveryCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := qdriver.Attach(ctx, server.URL, qdriver.Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("discovery deadline: %v", err)
	}
}
