package workitems

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// OpenPostgres opens a PostgreSQL connection pool from dsn, initializes the
// workitems schema, and returns a Repository-compatible *Store that owns the pool.
func OpenPostgres(ctx context.Context, dsn string) (*Store, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("%w: postgres dsn is required", ErrInvalidInput)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	store, err := OpenPostgresDB(ctx, db, true)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// OpenPostgresDB initializes the workitems schema on an existing PostgreSQL *sql.DB.
// If ownsDB is false, Store.Close leaves the caller-owned pool open.
func OpenPostgresDB(ctx context.Context, db *sql.DB, ownsDB bool) (*Store, error) {
	if ctx == nil {
		return nil, errors.New("workitems: context is nil")
	}
	if db == nil {
		return nil, errors.New("workitems: postgres db is nil")
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	store := &Store{
		db:     db,
		d:      postgresDialect{},
		ownsDB: ownsDB,
	}
	if err := store.initializePostgres(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// PingPostgres verifies PostgreSQL connectivity using dsn without creating tables.
func PingPostgres(ctx context.Context, dsn string) error {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return fmt.Errorf("%w: postgres dsn is required", ErrInvalidInput)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return db.PingContext(ctx)
}

func (s *Store) initializePostgres(ctx context.Context) error {
	d := postgresDialect{}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return fmt.Errorf("workitems: begin postgres schema init: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ddl := `
CREATE OR REPLACE FUNCTION workitems_ts_epoch(val text) RETURNS double precision
LANGUAGE SQL IMMUTABLE AS $$
  SELECT CASE
    WHEN val IS NULL OR val = '' OR val = '0001-01-01T00:00:00Z' THEN 0::double precision
    ELSE EXTRACT(EPOCH FROM val::timestamptz)
  END
$$;

CREATE TABLE IF NOT EXISTS workspaces (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  name_key TEXT NOT NULL,
  manager_session_id TEXT NOT NULL UNIQUE,
  coordinator_session_id TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  archived_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS workspace_active_name ON workspaces(name_key) WHERE archived_at IS NULL;

CREATE TABLE IF NOT EXISTS workspace_roots (
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  position INTEGER NOT NULL,
  path TEXT NOT NULL,
  path_key TEXT NOT NULL,
  active INTEGER NOT NULL CHECK(active IN (0,1)),
  PRIMARY KEY(workspace_id, position)
);
CREATE UNIQUE INDEX IF NOT EXISTS workspace_active_root ON workspace_roots(path_key) WHERE active = 1;

CREATE TABLE IF NOT EXISTS catalog_migrations (
  name TEXT PRIMARY KEY,
  completed_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS items (
  seq BIGSERIAL PRIMARY KEY,
  id TEXT NOT NULL UNIQUE,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  kind TEXT NOT NULL CHECK(kind IN ('issue','ticket')),
  number BIGINT NOT NULL,
  payload JSONB NOT NULL,
  UNIQUE(workspace_id,kind,number)
);
CREATE INDEX IF NOT EXISTS items_workspace ON items(workspace_id,kind,seq);
CREATE INDEX IF NOT EXISTS items_ticket_board_active ON items(workspace_id,seq) WHERE ` + activeBoardTicketsSQL(d) + `;
CREATE INDEX IF NOT EXISTS items_ticket_board_done ON items(workspace_id,seq) WHERE ` + doneBoardTicketsSQL(d) + `;

CREATE TABLE IF NOT EXISTS posts (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  item_id TEXT NOT NULL REFERENCES items(id),
  seq BIGINT NOT NULL,
  revision BIGINT NOT NULL,
  payload JSONB NOT NULL,
  UNIQUE(item_id,seq)
);
CREATE TABLE IF NOT EXISTS post_revisions (
  post_id TEXT NOT NULL REFERENCES posts(id),
  revision BIGINT NOT NULL,
  payload JSONB NOT NULL,
  PRIMARY KEY(post_id,revision)
);
CREATE TABLE IF NOT EXISTS item_revisions (
  item_id TEXT NOT NULL REFERENCES items(id),
  revision BIGINT NOT NULL,
  payload JSONB NOT NULL,
  PRIMARY KEY(item_id,revision)
);
CREATE TABLE IF NOT EXISTS mutation_receipts (
  workspace_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  fingerprint BYTEA NOT NULL,
  result BYTEA NOT NULL,
  PRIMARY KEY(workspace_id,request_id)
);
CREATE TABLE IF NOT EXISTS changes (
  seq BIGSERIAL PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  post_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS projection_receipts (
  projection TEXT PRIMARY KEY,
  applied_seq BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS work_assets (
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  id TEXT NOT NULL,
  mime TEXT NOT NULL,
  data BYTEA NOT NULL,
  PRIMARY KEY(workspace_id, id)
);

CREATE TABLE IF NOT EXISTS workspace_member_bindings (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  agent_id TEXT NOT NULL,
  definition_root TEXT NOT NULL,
  execution_root TEXT NOT NULL,
  payload JSONB NOT NULL,
  UNIQUE(workspace_id,agent_id,definition_root,execution_root)
);

CREATE TABLE IF NOT EXISTS ticket_attempts (
  seq BIGSERIAL PRIMARY KEY,
  id TEXT NOT NULL UNIQUE,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  ticket_id TEXT NOT NULL REFERENCES items(id),
  payload JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS attempts_ticket ON ticket_attempts(ticket_id,seq);

CREATE TABLE IF NOT EXISTS manager_inbox_events (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  attempt_id TEXT NOT NULL REFERENCES ticket_attempts(id),
  payload JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS workspace_settings (
  workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id),
  max_parallel_tickets INTEGER NOT NULL DEFAULT 0,
  max_parallel_subagents INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS workspace_defaults (
  id INTEGER PRIMARY KEY CHECK(id=1),
  revision BIGINT NOT NULL,
  agent_ids JSONB NOT NULL
);
INSERT INTO workspace_defaults VALUES(1,1,'[]'::jsonb) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS workspace_start_approval (
  workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id),
  required BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS ticket_dependencies (
  ticket_id TEXT NOT NULL REFERENCES items(id),
  prerequisite_id TEXT NOT NULL REFERENCES items(id),
  PRIMARY KEY(ticket_id,prerequisite_id),
  CHECK(ticket_id<>prerequisite_id)
);
CREATE INDEX IF NOT EXISTS ticket_dependents ON ticket_dependencies(prerequisite_id,ticket_id);

CREATE INDEX IF NOT EXISTS ticket_assignment_queue_v2 ON items(workspace_id,seq,(` + assignmentDueSQL(d) + `)) WHERE ` + assignmentQueueSQL(d) + `;
CREATE INDEX IF NOT EXISTS ticket_schedule_due ON items(workspace_id,(` + d.tsExpr(d.jsonText("payload", "next_scheduled_at")) + `),seq) WHERE ` + recurringScheduleQueueSQL(d) + `;
CREATE INDEX IF NOT EXISTS ticket_schedule_runs ON items((` + d.jsonText("payload", "schedule_id") + `),seq) WHERE ` + pendingScheduleRunSQL(d) + `;
CREATE INDEX IF NOT EXISTS ticket_merge_queue ON items(workspace_id,seq) WHERE ` + mergeQueueSQL(d) + `;
CREATE INDEX IF NOT EXISTS attempts_active ON ticket_attempts(workspace_id,(` + d.jsonText("payload", "member_id") + `),ticket_id) WHERE ` + activeAttemptsSQL(d) + `;
CREATE INDEX IF NOT EXISTS attempts_lease_expiry ON ticket_attempts((` + d.tsExpr(d.jsonText("payload", "lease_until")) + `)) WHERE ` + leasedAttemptsSQL(d) + `;
CREATE INDEX IF NOT EXISTS inbox_pending ON manager_inbox_events(workspace_id,(` + d.tsExpr(d.jsonText("payload", "created_at")) + `),id) WHERE ` + pendingInboxSQL(d) + `;
CREATE INDEX IF NOT EXISTS ticket_parent_worktrees ON items((` + d.jsonText("payload", "parent_ticket_id") + `),(` + d.jsonText("payload", "parent_worktree_id") + `),seq) WHERE ` + visibleBoardTicketSQL(d) + `;
CREATE INDEX IF NOT EXISTS ticket_parent_resume ON items(workspace_id,seq) WHERE ` + resumeQueueSQL(d) + `;
`
	if _, err := tx.Tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("workitems: initialize postgres schema: %w", err)
	}
	return tx.Commit()
}
