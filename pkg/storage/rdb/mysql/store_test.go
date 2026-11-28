package mysql

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/snowmerak/q/v2/pkg/domain"
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
	baseDSN := strings.TrimSpace(os.Getenv("Q_TEST_MYSQL_DSN"))
	if baseDSN == "" {
		t.Skip("Q_TEST_MYSQL_DSN not set")
	}

	contracttest.RunRDBContractTests(t, func(t *testing.T) storage.RDBStore {
		t.Helper()
		parsed, err := mysqldrv.ParseDSN(baseDSN)
		if err != nil {
			t.Fatalf("ParseDSN: %v", err)
		}
		adminCfg := *parsed
		adminCfg.DBName = ""
		adminDB, err := sql.Open("mysql", adminCfg.FormatDSN())
		if err != nil {
			t.Fatalf("open admin mysql: %v", err)
		}
		dbName := "q_test_" + strings.ReplaceAll(domain.NewID()[:16], "-", "")
		if _, err := adminDB.Exec("CREATE DATABASE `" + dbName + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
			_ = adminDB.Close()
			t.Fatalf("create isolated test db: %v", err)
		}
		t.Cleanup(func() {
			_, _ = adminDB.Exec("DROP DATABASE IF EXISTS `" + dbName + "`")
			_ = adminDB.Close()
		})

		scopedCfg := *parsed
		scopedCfg.DBName = dbName
		st, err := Open(t.Context(), scopedCfg.FormatDSN())
		if err != nil {
			t.Fatalf("mysql.Open: %v", err)
		}
		return st
	})
}
