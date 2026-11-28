// Package mysql implements Q v2's storage.RDBStore backed by MySQL 8.0+.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlcommon"
)

// Store wraps a pooled MySQL connection with sqlcommon repositories.
type Store struct {
	*sqlcommon.Store
}

// Open connects to MySQL using dsn, runs schema migrations, and returns an
// operational storage.RDBStore.
func Open(ctx context.Context, dsn string) (*Store, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("%w: mysql dsn is required", storage.ErrInvalidInput)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{Store: sqlcommon.New(db, mysqlDialect{})}, nil
}

type mysqlDialect struct{}

func (mysqlDialect) Name() string           { return "mysql" }
func (mysqlDialect) Rebind(q string) string { return q }
func (mysqlDialect) UpsertMemberSQL() string {
	return `INSERT INTO room_members (room_id, character_id, character_snapshot_json, base_speed, actor_state_json, active, joined_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE character_snapshot_json = VALUES(character_snapshot_json), base_speed = VALUES(base_speed), actor_state_json = VALUES(actor_state_json), active = VALUES(active)`
}

func (mysqlDialect) EnqueueOutbox(ctx context.Context, exec sqlcommon.Execer, entry domain.OutboxEntry) (int64, error) {
	res, err := exec.ExecContext(ctx,
		`INSERT INTO index_outbox (entity_type, entity_id, op, revision, payload_json, status, attempts, next_attempt_at, last_error, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.EntityType, entry.EntityID, string(entry.Op), entry.Revision, entry.PayloadJSON, string(entry.Status), entry.Attempts, entry.NextAttemptAt, entry.LastError, entry.CreatedAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (mysqlDialect) BeforeAppendMessage(ctx context.Context, exec sqlcommon.Execer, roomID string) error {
	var id string
	if err := exec.QueryRowContext(ctx, `SELECT id FROM rooms WHERE id = ? FOR UPDATE`, roomID).Scan(&id); err != nil {
		return err
	}
	return nil
}

func (mysqlDialect) ClaimValueJobLockClause() string {
	return "FOR UPDATE SKIP LOCKED"
}

func (mysqlDialect) IsUniqueViolation(err error) bool {
	var myErr *mysqldrv.MySQLError
	return errors.As(err, &myErr) && myErr.Number == 1062
}

func (mysqlDialect) MapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrNotFound
	}
	var myErr *mysqldrv.MySQLError
	if errors.As(err, &myErr) {
		switch myErr.Number {
		case 1062:
			return storage.ErrAlreadyExists
		case 1451, 1452:
			return storage.ErrNotFound
		}
	}
	return err
}
