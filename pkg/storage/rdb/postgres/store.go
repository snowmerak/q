// Package postgres implements Q v2's storage.RDBStore backed by PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlcommon"
)

// Store wraps a pooled PostgreSQL connection with sqlcommon repositories.
type Store struct {
	*sqlcommon.Store
}

// Open connects to PostgreSQL using dsn, applies schema migrations, and returns
// an operational storage.RDBStore.
func Open(ctx context.Context, dsn string) (*Store, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("%w: postgres dsn is required", storage.ErrInvalidInput)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{Store: sqlcommon.New(db, pgDialect{})}, nil
}

type pgDialect struct{}

func (pgDialect) Name() string           { return "postgres" }
func (pgDialect) Rebind(q string) string { return sqlcommon.RebindDollar(q) }
func (pgDialect) UpsertMemberSQL() string {
	return `INSERT INTO room_members (room_id, character_id, character_snapshot_json, base_speed, actor_state_json, active, joined_at) VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (room_id, character_id) DO UPDATE SET character_snapshot_json = EXCLUDED.character_snapshot_json, base_speed = EXCLUDED.base_speed, actor_state_json = EXCLUDED.actor_state_json, active = EXCLUDED.active`
}

func (pgDialect) EnqueueOutbox(ctx context.Context, exec sqlcommon.Execer, entry domain.OutboxEntry) (int64, error) {
	var seq int64
	err := exec.QueryRowContext(ctx,
		`INSERT INTO index_outbox (entity_type, entity_id, op, revision, payload_json, status, attempts, next_attempt_at, last_error, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING seq`,
		entry.EntityType, entry.EntityID, string(entry.Op), entry.Revision, entry.PayloadJSON, string(entry.Status), entry.Attempts, entry.NextAttemptAt, entry.LastError, entry.CreatedAt,
	).Scan(&seq)
	return seq, err
}

func (pgDialect) BeforeAppendMessage(ctx context.Context, exec sqlcommon.Execer, roomID string) error {
	var id string
	if err := exec.QueryRowContext(ctx, `SELECT id FROM rooms WHERE id = $1 FOR UPDATE`, roomID).Scan(&id); err != nil {
		return err
	}
	return nil
}

func (pgDialect) ClaimValueJobLockClause() string {
	return "FOR UPDATE SKIP LOCKED"
}

func (pgDialect) IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (pgDialect) MapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return storage.ErrAlreadyExists
		case "23503":
			return storage.ErrNotFound
		}
	}
	return err
}
