package s3_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/blob/s3"
	"github.com/snowmerak/q/v2/pkg/storage/contracttest"
)

func newMockS3Server() *httptest.Server {
	var (
		mu      sync.Mutex
		objects = make(map[string][]byte)
	)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			objects[key] = data
			sum := sha256.Sum256(data)
			w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			data, ok := objects[key]
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code><Message>not found</Message></Error>`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
		case http.MethodDelete:
			delete(objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
}

func TestS3BlobContractOfflineMock(t *testing.T) {
	srv := newMockS3Server()
	t.Cleanup(srv.Close)

	contracttest.RunBlobContractTests(t, func(t *testing.T) storage.BlobStore {
		t.Helper()
		st, err := s3.Open(t.Context(), s3.Config{
			Endpoint:     srv.URL,
			Bucket:       "q-test-bucket",
			Region:       "us-east-1",
			AccessKey:    "test-access",
			SecretKey:    "test-secret",
			UsePathStyle: true,
		})
		if err != nil {
			t.Fatalf("s3.Open: %v", err)
		}
		return st
	})
}
