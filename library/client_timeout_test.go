package library

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/agentskills"
)

func TestEnsureSeparatesProbeAndRequestTimeouts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/health":
			writeJSON(writer, http.StatusOK, Health{Service: ServiceName, ProtocolVersion: ProtocolVersion, Ready: true})
		case "/v1/skills/search":
			select {
			case <-time.After(100 * time.Millisecond):
				writeJSON(writer, http.StatusOK, SkillSearchResponse{})
			case <-request.Context().Done():
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	value := Config{Version: ConfigVersion, Host: "127.0.0.1", Port: server.Listener.Addr().(*net.TCPAddr).Port}
	for _, timeout := range []time.Duration{0, time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			options := testOptions(t.TempDir(), value)
			options.ProbeTimeout = 20 * time.Millisecond
			options.RequestTimeout = timeout
			runtime, err := EnsureWithOptions(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = runtime.Close() }()
			if runtime.IsLeader() {
				t.Fatal("expected a follower of the test server")
			}
			if _, err := runtime.Client().SearchSkills(t.Context(), SkillSearchRequest{Query: "cat"}); err != nil {
				t.Fatalf("ordinary request exceeded the probe timeout: %v", err)
			}
			want := timeout
			if want == 0 {
				want = 5 * time.Second
			}
			if got := runtime.Client().http.Timeout; got != want {
				t.Fatalf("request timeout = %s, want %s", got, want)
			}
		})
	}
}

func TestEnsureKeepsShortProbeTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	value := Config{Version: ConfigVersion, Host: "127.0.0.1", Port: server.Listener.Addr().(*net.TCPAddr).Port}
	options := testOptions(t.TempDir(), value)
	options.ProbeTimeout = 20 * time.Millisecond
	options.StartupTimeout = 50 * time.Millisecond
	options.RequestTimeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := EnsureWithOptions(ctx, options); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Ensure error = %v; expected startup failure before the caller's deadline", err)
	}
}

func TestSkillIndexingUsesFiveMinuteTimeout(t *testing.T) {
	var sourceRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-time.After(50 * time.Millisecond):
		case <-request.Context().Done():
			return
		}
		switch request.URL.Path {
		case "/v1/skills/reload":
			writeJSON(writer, http.StatusOK, SkillReloadResponse{Active: 1})
		case "/v1/skills/embeddings/configure":
			writeJSON(writer, http.StatusOK, struct{}{})
		case "/v1/skills/embeddings/sources":
			output := SkillEmbeddingSourceResponse{}
			if sourceRequests.Add(1) == 1 {
				output.Remaining = 1
				output.Sources = []SkillEmbeddingSource{{
					ID: "cat-care", Digest: "digest",
					Parts: []agentskills.EmbeddingPart{{ID: "description", Text: "Care for a cat."}},
				}}
			}
			writeJSON(writer, http.StatusOK, output)
		case "/v1/skills/embeddings":
			writeJSON(writer, http.StatusOK, SkillEmbeddingApplyResponse{Updated: 1})
		case "/v1/skills/search":
			writeJSON(writer, http.StatusOK, SkillSearchResponse{})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL+"/v1", "", 10*time.Millisecond)
	if got := client.index.Timeout; got != 5*time.Minute {
		t.Fatalf("indexing timeout = %s, want 5m", got)
	}
	if got := client.register.Timeout; got != 2*time.Minute {
		t.Fatalf("proposition registration timeout = %s, want 2m", got)
	}
	if err := client.ConfigureEmbedding(&semanticSkillEmbedder{}, "embed-v1", 3); err != nil {
		t.Fatal(err)
	}
	output, err := client.ReloadSkills(t.Context())
	if err != nil || output.Active != 1 || sourceRequests.Load() != 2 {
		t.Fatalf("reload and embedding sync = %#v, %v; source requests = %d", output, err, sourceRequests.Load())
	}
	if _, err := client.SearchSkills(t.Context(), SkillSearchRequest{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ordinary request error = %v, want its shorter timeout", err)
	}
}

func TestSkillIndexingHonorsCallerDeadline(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input SkillVectorConfigRequest
		if err := decodeLibraryJSON(writer, request, &input); err != nil {
			return
		}
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	client := NewClient(server.URL+"/v1", "", time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := client.SyncSkillEmbeddings(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("sync error = %v, want caller's deadline", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("indexing request did not reach the server")
	}
}
