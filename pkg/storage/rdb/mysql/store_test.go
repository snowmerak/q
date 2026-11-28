package mysql

import (
	"errors"
	"os"
	"strings"
	"testing"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/contracttest"
)

func TestMySQLDialectAndMigrations(t *testing.T) {
	d := mysqlDialect{}
	q := "SELECT * FROM rooms WHERE id = ?"
	if d.Rebind(q) != q {
		t.Fatalf("expected identity rebind")
	}
	if !d.IsUniqueViolation(&mysqldrv.MySQLError{Number: 1062}) {
		t.Fatal("expected 1062 to be unique violation")
	}
	if !errors.Is(d.MapError(&mysqldrv.MySQLError{Number: 1062}), storage.ErrAlreadyExists) {
		t.Fatal("expected ErrAlreadyExists for 1062")
	}
	if !errors.Is(d.MapError(&mysqldrv.MySQLError{Number: 1452}), storage.ErrNotFound) {
		t.Fatal("expected ErrNotFound for 1452")
	}
	if len(migrations) == 0 || len(migrations[0].stmts) < 14 {
		t.Fatalf("expected all mysql table migrations, got %+v", migrations)
	}
}

func TestMySQLContractLiveEnvGated(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("Q_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("Q_TEST_MYSQL_DSN not set")
	}
	contracttest.RunRDBContractTests(t, func(t *testing.T) storage.RDBStore {
		t.Helper()
		st, err := Open(t.Context(), dsn)
		if err != nil {
			t.Fatalf("mysql.Open: %v", err)
		}
		return st
	})
}
