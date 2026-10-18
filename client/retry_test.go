package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransientRequestRetriesAndPermanentErrors(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		failures  int32
		wantCalls int32
		wantError bool
	}{
		{"temporary server failure", 503, 1, 2, false},
		{"rate limit", 429, 1, 2, false},
		{"authentication", 401, 1, 1, true},
		{"invalid request", 400, 1, 1, true},
		{"bounded attempts", 502, 10, 3, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			ids := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ids <- r.Header.Get(UsageEventIDHeader)
				if calls.Add(1) <= test.failures {
					w.WriteHeader(test.status)
					_, _ = io.WriteString(w, `{"error":{"message":"temporary"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
			}))
			defer server.Close()
			configured, err := New(Config{BaseURL: server.URL, ForwardUsageMetadata: true,
				Retry: RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = configured.Close() }()
			_, err = configured.Chat(t.Context(), ChatRequest{Model: "test", Messages: []Message{{Role: RoleUser, Content: "hello"}}})
			if (err != nil) != test.wantError || calls.Load() != test.wantCalls {
				t.Fatalf("calls=%d error=%v", calls.Load(), err)
			}
			close(ids)
			seen := map[string]bool{}
			for id := range ids {
				if !ValidUsageEventID(id) || seen[id] {
					t.Fatalf("invalid or reused attempt ID: %q", id)
				}
				seen[id] = true
			}
		})
	}
}

func TestStreamRetriesBeforeFirstChunkOnly(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "before output", true: "after output"}[partial], func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if calls.Add(1) == 1 {
					// A truncated HTTP body deterministically yields unexpected EOF.
					w.Header().Set("Content-Length", "10000")
					w.WriteHeader(http.StatusOK)
					if partial {
						_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
					}
					w.(http.Flusher).Flush()
					return
				}
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			configured, err := New(Config{BaseURL: server.URL, Retry: RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = configured.Close() }()
			stream, err := configured.ChatStream(t.Context(), ChatRequest{Model: "test", Messages: []Message{{Role: RoleUser, Content: "hello"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			chunk, err := stream.Recv()
			if err != nil || chunk == nil {
				t.Fatalf("first chunk = %+v, %v", chunk, err)
			}
			text := chunk.Choices[0].Delta.Content
			if partial {
				if text != "first" {
					t.Fatalf("partial text = %q", text)
				}
				if _, err := stream.Recv(); !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("partial stream error = %v", err)
				}
				if calls.Load() != 1 {
					t.Fatalf("replayed output: %d requests", calls.Load())
				}
			} else if text != "recovered" || calls.Load() != 2 {
				t.Fatalf("recovery = %q, %d requests", text, calls.Load())
			}
		})
	}
}

func TestStreamOpenAndReadShareRetryBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) != 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "10000")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
	}))
	defer server.Close()
	configured, err := New(Config{BaseURL: server.URL, Retry: RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = configured.Close() }()
	stream, err := configured.ChatStream(t.Context(), ChatRequest{Model: "test", Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if _, err := stream.Recv(); err == nil || calls.Load() != 3 {
		t.Fatalf("attempts=%d error=%v", calls.Load(), err)
	}
}

func TestRetryWaitHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	finished := make(chan error, 1)
	var calls atomic.Int32
	go func() {
		_, err := retryRequest(&requestRetry{ctx: ctx, policy: RetryPolicy{MaxAttempts: 4, InitialBackoff: time.Hour, MaxBackoff: time.Hour}}, func() (int, error) {
			calls.Add(1)
			close(started)
			return 0, io.ErrUnexpectedEOF
		})
		finished <- err
	}()
	<-started
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
			t.Fatalf("cancellation: calls=%d error=%v", calls.Load(), err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry backoff did not stop after cancellation")
	}
}

type interruptedRetryStream struct{ started chan struct{} }

func (stream interruptedRetryStream) Recv() (*ChatChunk, error) {
	close(stream.started)
	return nil, io.ErrUnexpectedEOF
}

func (interruptedRetryStream) Close() error { return nil }

func TestClosingStreamCancelsRetryWait(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	stream, err := openRetryStream(t.Context(), RetryPolicy{MaxAttempts: 4, InitialBackoff: time.Hour, MaxBackoff: time.Hour}, func(context.Context) (retryStreamSource[ChatChunk], error) {
		calls.Add(1)
		return interruptedRetryStream{started: started}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	finished := make(chan error, 1)
	go func() { _, err := stream.Recv(); finished <- err }()
	<-started
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
			t.Fatalf("closed stream: calls=%d error=%v", calls.Load(), err)
		}
	case <-time.After(time.Second):
		t.Fatal("closing stream did not interrupt retry wait")
	}
}
