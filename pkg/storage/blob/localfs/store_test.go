package localfs_test

import (
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/blob/localfs"
	"github.com/snowmerak/q/v2/pkg/storage/contracttest"
)

func TestLocalFSBlobContract(t *testing.T) {
	contracttest.RunBlobContractTests(t, func(t *testing.T) storage.BlobStore {
		t.Helper()
		bs, err := localfs.Open(filepath.Join(t.TempDir(), "blobs"))
		if err != nil {
			t.Fatalf("localfs.Open: %v", err)
		}
		return bs
	})
}
