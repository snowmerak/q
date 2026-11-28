package sqlcommon_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/contracttest"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlcommon"
)

type sqliteTestDialect struct{}

func (sqliteTestDialect) Name() string           { return "sqlite-test" }
func (sqliteTestDialect) Rebind(q string) string { return q }
func (sqliteTestDialect) UpsertMemberSQL() string {
	return `INSERT INTO room_members (room_id, character_id, character_snapshot_json, base_speed, actor_state_json, active, joined_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(room_id, character_id) DO UPDATE SET character_snapshot_json = excluded.character_snapshot_json, base_speed = excluded.base_speed, actor_state_json = excluded.actor_state_json, active = excluded.active`
}
func (sqliteTestDialect) EnqueueOutbox(ctx context.Context, exec sqlcommon.Execer, entry domain.OutboxEntry) (int64, error) {
	res, err := exec.ExecContext(ctx, `INSERT INTO index_outbox (entity_type, entity_id, op, revision, payload_json, status, attempts, next_attempt_at, last_error, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.EntityType, entry.EntityID, string(entry.Op), entry.Revision, entry.PayloadJSON, string(entry.Status), entry.Attempts, entry.NextAttemptAt, entry.LastError, entry.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
func (sqliteTestDialect) BeforeAppendMessage(context.Context, sqlcommon.Execer, string) error {
	return nil
}
func (sqliteTestDialect) ClaimValueJobLockClause() string { return "" }
func (sqliteTestDialect) IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
func (sqliteTestDialect) MapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrNotFound
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return storage.ErrAlreadyExists
	}
	if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		return storage.ErrNotFound
	}
	return err
}

func TestRebindDollar(t *testing.T) {
	in := `SELECT * FROM t WHERE a = ? AND note = '?' AND b = ?`
	got := sqlcommon.RebindDollar(in)
	want := `SELECT * FROM t WHERE a = $1 AND note = '?' AND b = $2`
	if got != want {
		t.Fatalf("RebindDollar = %q, want %q", got, want)
	}
}

func TestSQLCommonRDBContract(t *testing.T) {
	contracttest.RunRDBContractTests(t, func(t *testing.T) storage.RDBStore {
		t.Helper()
		dbPath := filepath.Join(t.TempDir(), "common.sqlite")
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		stmts := []string{
			`PRAGMA foreign_keys = ON`,
			`CREATE TABLE personas (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', avatar_artifact_id TEXT NOT NULL DEFAULT '', is_default INTEGER NOT NULL DEFAULT 0, version INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER)`,
			`CREATE TABLE characters (id TEXT PRIMARY KEY, name TEXT NOT NULL, avatar_artifact_id TEXT NOT NULL DEFAULT '', personality TEXT NOT NULL DEFAULT '', background TEXT NOT NULL DEFAULT '', appearance TEXT NOT NULL DEFAULT '', initial_values_json TEXT NOT NULL DEFAULT '[]', initial_state_json TEXT NOT NULL DEFAULT '{}', model_prefs_json TEXT, approval_policy_override TEXT NOT NULL DEFAULT '', version INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER)`,
			`CREATE TABLE rooms (id TEXT PRIMARY KEY, title TEXT NOT NULL, mode TEXT NOT NULL, persona_id TEXT NOT NULL REFERENCES personas(id), persona_snapshot_json TEXT NOT NULL, approval_mode_override TEXT, model_overrides_json TEXT, max_turns_per_input INTEGER NOT NULL DEFAULT 3, context_version INTEGER NOT NULL DEFAULT 1, version INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER)`,
			`CREATE TABLE room_members (room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE, character_id TEXT NOT NULL REFERENCES characters(id), character_snapshot_json TEXT NOT NULL, base_speed INTEGER NOT NULL DEFAULT 50, actor_state_json TEXT NOT NULL DEFAULT '{}', active INTEGER NOT NULL DEFAULT 1, joined_at INTEGER NOT NULL, PRIMARY KEY (room_id, character_id))`,
			`CREATE TABLE turns (id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE, request_id TEXT NOT NULL, input_hash TEXT NOT NULL, status TEXT NOT NULL, error_code TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '', checkpoint_json TEXT NOT NULL DEFAULT '{}', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, completed_at INTEGER, UNIQUE(room_id, request_id))`,
			`CREATE TABLE messages (id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE, turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE, seq INTEGER NOT NULL, role TEXT NOT NULL, author_id TEXT NOT NULL DEFAULT '', author_name TEXT NOT NULL DEFAULT '', content TEXT NOT NULL, subtext TEXT NOT NULL DEFAULT '', observed_by_json TEXT NOT NULL DEFAULT '[]', location_tag TEXT NOT NULL DEFAULT '', metadata_json TEXT NOT NULL DEFAULT '{}', created_at INTEGER NOT NULL, UNIQUE(room_id, seq))`,
			`CREATE TABLE tool_invocations (id TEXT PRIMARY KEY, turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE, room_id TEXT NOT NULL, actor_id TEXT NOT NULL, call_id TEXT NOT NULL DEFAULT '', tool_name TEXT NOT NULL, tool_source TEXT NOT NULL, args_json TEXT NOT NULL DEFAULT '{}', gate_mode TEXT NOT NULL, decision_reason TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, result_summary TEXT NOT NULL DEFAULT '', artifact_id TEXT, error_message TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
			`CREATE TABLE user_questions (id TEXT PRIMARY KEY, turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE, room_id TEXT NOT NULL, actor_id TEXT NOT NULL, prompt TEXT NOT NULL, choices_json TEXT NOT NULL DEFAULT '[]', allow_freeform INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL, answer_text TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, answered_at INTEGER)`,
			`CREATE TABLE memories (id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE, owner_id TEXT NOT NULL, source_turn_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL, content TEXT NOT NULL, observed_by_json TEXT NOT NULL DEFAULT '[]', occurred_at INTEGER NOT NULL, pinned INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL, revision INTEGER NOT NULL DEFAULT 1, embedding_model TEXT NOT NULL DEFAULT '', embedding_dim INTEGER NOT NULL DEFAULT 0, embedding_blob BLOB, created_at INTEGER NOT NULL)`,
			`CREATE TABLE value_jobs (id TEXT PRIMARY KEY, room_id TEXT NOT NULL, character_id TEXT NOT NULL, source_turn_id TEXT NOT NULL, base_values_revision INTEGER NOT NULL, candidates_json TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', next_run_at INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
			`CREATE TABLE knowledge_docs (id TEXT PRIMARY KEY, scope TEXT NOT NULL, title TEXT NOT NULL, source_uri TEXT NOT NULL DEFAULT '', media_type TEXT NOT NULL DEFAULT 'text/plain', content TEXT NOT NULL, content_sha256 TEXT NOT NULL, revision INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
			`CREATE TABLE knowledge_chunks (id TEXT PRIMARY KEY, doc_id TEXT NOT NULL REFERENCES knowledge_docs(id) ON DELETE CASCADE, doc_revision INTEGER NOT NULL, chunk_ordinal INTEGER NOT NULL, char_offset INTEGER NOT NULL, text TEXT NOT NULL, embedding_model TEXT NOT NULL DEFAULT '', embedding_dim INTEGER NOT NULL DEFAULT 0, embedding_blob BLOB)`,
			`CREATE TABLE index_outbox (seq INTEGER PRIMARY KEY AUTOINCREMENT, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, op TEXT NOT NULL, revision INTEGER NOT NULL, payload_json TEXT NOT NULL DEFAULT '{}', status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at INTEGER NOT NULL, last_error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL)`,
			`CREATE TABLE artifacts (id TEXT PRIMARY KEY, kind TEXT NOT NULL, media_type TEXT NOT NULL, byte_size INTEGER NOT NULL, sha256 TEXT NOT NULL, blob_key TEXT NOT NULL, source_meta_json TEXT NOT NULL DEFAULT '{}', pinned INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, last_accessed_at INTEGER NOT NULL)`,
		}
		for _, stmt := range stmts {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("exec %q: %v", stmt, err)
			}
		}
		return sqlcommon.New(db, sqliteTestDialect{})
	})
}
