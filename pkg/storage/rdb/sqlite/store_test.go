package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/contracttest"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlite"
)

func TestSQLiteContract(t *testing.T) {
	contracttest.RunRDBContractTests(t, func(t *testing.T) storage.RDBStore {
		t.Helper()
		st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "q.sqlite"))
		if err != nil {
			t.Fatalf("sqlite.Open: %v", err)
		}
		return st
	})
}
