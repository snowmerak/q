package workspacememory

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWorkspaceIndexingAllowsFiveMinutesAndHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		select {
		case <-time.After(50 * time.Millisecond):
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{}`))
		case <-request.Context().Done():
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "", 10*time.Millisecond)
	if client.index.Timeout != 5*time.Minute {
		t.Fatalf("indexing timeout = %s", client.index.Timeout)
	}
	for _, path := range []string{"/workspaces/open", "/workspaces/id/vector/configure", "/workspaces/id/records/save-batch", "/workspaces/id/records/embeddings"} {
		t.Run(path, func(t *testing.T) {
			if err := client.doJSON(t.Context(), http.MethodPost, path, struct{}{}, nil, ""); err != nil {
				t.Fatalf("indexing used the ordinary timeout: %v", err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
			defer cancel()
			if err := client.doJSON(ctx, http.MethodPost, path, struct{}{}, nil, ""); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("caller deadline = %v", err)
			}
		})
	}
	if err := client.doJSON(t.Context(), http.MethodPost, "/workspaces/id/search", struct{}{}, nil, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ordinary timeout = %v", err)
	}
}
