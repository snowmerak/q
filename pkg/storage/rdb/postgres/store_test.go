package postgres

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/contracttest"
)

func TestPGDialectAndMigrations(t *testing.T) {
	d := pgDialect{}
	if got := d.Rebind("SELECT * FROM rooms WHERE id = ? AND version = ?"); got != "SELECT * FROM rooms WHERE id = $1 AND version = $2" {
		t.Fatalf("unexpected rebind: %q", got)
	}
	if !d.IsUniqueViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("expected 23505 to be unique violation")
	}
	if !errors.Is(d.MapError(&pgconn.PgError{Code: "23505"}), storage.ErrAlreadyExists) {
		t.Fatal("expected ErrAlreadyExists for 23505")
	}
	if !errors.Is(d.MapError(&pgconn.PgError{Code: "23503"}), storage.ErrNotFound) {
		t.Fatal("expected ErrNotFound for 23503")
	}
	if len(migrations) == 0 || len(migrations[0].stmts) < 14 {
		t.Fatalf("expected all postgres table migrations, got %+v", migrations)
	}
}

func TestPostgresContractLiveEnvGated(t *testing.T) {
	baseDSN := strings.TrimSpace(os.Getenv("Q_TEST_POSTGRES_DSN"))
	if baseDSN == "" {
		t.Skip("Q_TEST_POSTGRES_DSN not set")
	}

	contracttest.RunRDBContractTests(t, func(t *testing.T) storage.RDBStore {
		t.Helper()
		schema := "q_test_" + strings.ReplaceAll(domain.NewID(), "-", "")
		adminDB, err := sql.Open("pgx", baseDSN)
		if err != nil {
			t.Fatalf("open admin postgres: %v", err)
		}
		if _, err := adminDB.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
			_ = adminDB.Close()
			t.Fatalf("create isolated test schema: %v", err)
		}
		t.Cleanup(func() {
			_, _ = adminDB.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`)
			_ = adminDB.Close()
		})

		sep := "?"
		if strings.Contains(baseDSN, "?") {
			sep = "&"
		}
		scopedDSN := baseDSN + sep + "search_path=" + schema
		st, err := Open(t.Context(), scopedDSN)
		if err != nil {
			t.Fatalf("postgres.Open: %v", err)
		}
		return st
	})
}
