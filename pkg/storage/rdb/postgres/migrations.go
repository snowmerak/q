package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/snowmerak/q/v2/pkg/domain"
)

type migration struct {
	version int
	stmts   []string
}

var migrations = []migration{
	{
		version: 1,
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS personas (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				avatar_artifact_id TEXT NOT NULL DEFAULT '',
				is_default INTEGER NOT NULL DEFAULT 0,
				version BIGINT NOT NULL DEFAULT 1,
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL,
				deleted_at BIGINT
			)`,
			`CREATE TABLE IF NOT EXISTS characters (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				avatar_artifact_id TEXT NOT NULL DEFAULT '',
				personality TEXT NOT NULL DEFAULT '',
				background TEXT NOT NULL DEFAULT '',
				appearance TEXT NOT NULL DEFAULT '',
				initial_values_json TEXT NOT NULL DEFAULT '[]',
				initial_state_json TEXT NOT NULL DEFAULT '{}',
				model_prefs_json TEXT,
				approval_policy_override TEXT NOT NULL DEFAULT '',
				version BIGINT NOT NULL DEFAULT 1,
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL,
				deleted_at BIGINT
			)`,
			`CREATE TABLE IF NOT EXISTS rooms (
				id TEXT PRIMARY KEY,
				title TEXT NOT NULL,
				mode TEXT NOT NULL,
				persona_id TEXT NOT NULL REFERENCES personas(id),
				persona_snapshot_json TEXT NOT NULL,
				approval_mode_override TEXT,
				model_overrides_json TEXT,
				max_turns_per_input INTEGER NOT NULL DEFAULT 3,
				context_version BIGINT NOT NULL DEFAULT 1,
				version BIGINT NOT NULL DEFAULT 1,
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL,
				deleted_at BIGINT
			)`,
			`CREATE TABLE IF NOT EXISTS room_members (
				room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
				character_id TEXT NOT NULL REFERENCES characters(id),
				character_snapshot_json TEXT NOT NULL,
				base_speed INTEGER NOT NULL DEFAULT 50,
				actor_state_json TEXT NOT NULL DEFAULT '{}',
				active INTEGER NOT NULL DEFAULT 1,
				joined_at BIGINT NOT NULL,
				PRIMARY KEY (room_id, character_id)
			)`,
			`CREATE TABLE IF NOT EXISTS turns (
				id TEXT PRIMARY KEY,
				room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
				request_id TEXT NOT NULL,
				input_hash TEXT NOT NULL,
				status TEXT NOT NULL,
				error_code TEXT NOT NULL DEFAULT '',
				error_message TEXT NOT NULL DEFAULT '',
				checkpoint_json TEXT NOT NULL DEFAULT '{}',
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL,
				completed_at BIGINT,
				UNIQUE (room_id, request_id)
			)`,
			`CREATE TABLE IF NOT EXISTS messages (
				id TEXT PRIMARY KEY,
				room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
				turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
				seq BIGINT NOT NULL,
				role TEXT NOT NULL,
				author_id TEXT NOT NULL DEFAULT '',
				author_name TEXT NOT NULL DEFAULT '',
				content TEXT NOT NULL,
				subtext TEXT NOT NULL DEFAULT '',
				observed_by_json TEXT NOT NULL DEFAULT '[]',
				location_tag TEXT NOT NULL DEFAULT '',
				metadata_json TEXT NOT NULL DEFAULT '{}',
				created_at BIGINT NOT NULL,
				UNIQUE (room_id, seq)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_room_seq ON messages(room_id, seq DESC)`,
			`CREATE TABLE IF NOT EXISTS tool_invocations (
				id TEXT PRIMARY KEY,
				turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
				room_id TEXT NOT NULL,
				actor_id TEXT NOT NULL,
				call_id TEXT NOT NULL DEFAULT '',
				tool_name TEXT NOT NULL,
				tool_source TEXT NOT NULL,
				args_json TEXT NOT NULL DEFAULT '{}',
				gate_mode TEXT NOT NULL,
				decision_reason TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL,
				result_summary TEXT NOT NULL DEFAULT '',
				artifact_id TEXT,
				error_message TEXT NOT NULL DEFAULT '',
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_tool_invocations_turn ON tool_invocations(turn_id)`,
			`CREATE TABLE IF NOT EXISTS user_questions (
				id TEXT PRIMARY KEY,
				turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
				room_id TEXT NOT NULL,
				actor_id TEXT NOT NULL,
				prompt TEXT NOT NULL,
				choices_json TEXT NOT NULL DEFAULT '[]',
				allow_freeform INTEGER NOT NULL DEFAULT 1,
				status TEXT NOT NULL,
				answer_text TEXT NOT NULL DEFAULT '',
				created_at BIGINT NOT NULL,
				answered_at BIGINT
			)`,
			`CREATE TABLE IF NOT EXISTS memories (
				id TEXT PRIMARY KEY,
				room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
				owner_id TEXT NOT NULL,
				source_turn_id TEXT NOT NULL DEFAULT '',
				kind TEXT NOT NULL,
				content TEXT NOT NULL,
				observed_by_json TEXT NOT NULL DEFAULT '[]',
				occurred_at BIGINT NOT NULL,
				pinned INTEGER NOT NULL DEFAULT 0,
				status TEXT NOT NULL,
				revision BIGINT NOT NULL DEFAULT 1,
				embedding_model TEXT NOT NULL DEFAULT '',
				embedding_dim INTEGER NOT NULL DEFAULT 0,
				embedding_blob BYTEA,
				created_at BIGINT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_memories_room_owner ON memories(room_id, owner_id, occurred_at DESC)`,
			`CREATE TABLE IF NOT EXISTS value_jobs (
				id TEXT PRIMARY KEY,
				room_id TEXT NOT NULL,
				character_id TEXT NOT NULL,
				source_turn_id TEXT NOT NULL,
				base_values_revision BIGINT NOT NULL,
				candidates_json TEXT NOT NULL DEFAULT '[]',
				status TEXT NOT NULL,
				attempts INTEGER NOT NULL DEFAULT 0,
				last_error TEXT NOT NULL DEFAULT '',
				next_run_at BIGINT NOT NULL,
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_value_jobs_queue ON value_jobs(status, next_run_at)`,
			`CREATE TABLE IF NOT EXISTS knowledge_docs (
				id TEXT PRIMARY KEY,
				scope TEXT NOT NULL,
				title TEXT NOT NULL,
				source_uri TEXT NOT NULL DEFAULT '',
				media_type TEXT NOT NULL DEFAULT 'text/plain',
				content TEXT NOT NULL,
				content_sha256 TEXT NOT NULL,
				revision BIGINT NOT NULL DEFAULT 1,
				status TEXT NOT NULL,
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_knowledge_docs_scope ON knowledge_docs(scope, status)`,
			`CREATE TABLE IF NOT EXISTS knowledge_chunks (
				id TEXT PRIMARY KEY,
				doc_id TEXT NOT NULL REFERENCES knowledge_docs(id) ON DELETE CASCADE,
				doc_revision BIGINT NOT NULL,
				chunk_ordinal INTEGER NOT NULL,
				char_offset INTEGER NOT NULL,
				text TEXT NOT NULL,
				embedding_model TEXT NOT NULL DEFAULT '',
				embedding_dim INTEGER NOT NULL DEFAULT 0,
				embedding_blob BYTEA
			)`,
			`CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_doc ON knowledge_chunks(doc_id, chunk_ordinal)`,
			`CREATE TABLE IF NOT EXISTS index_outbox (
				seq BIGSERIAL PRIMARY KEY,
				entity_type TEXT NOT NULL,
				entity_id TEXT NOT NULL,
				op TEXT NOT NULL,
				revision BIGINT NOT NULL,
				payload_json TEXT NOT NULL DEFAULT '{}',
				status TEXT NOT NULL,
				attempts INTEGER NOT NULL DEFAULT 0,
				next_attempt_at BIGINT NOT NULL,
				last_error TEXT NOT NULL DEFAULT '',
				created_at BIGINT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_index_outbox_pending ON index_outbox(status, next_attempt_at, seq)`,
			`CREATE TABLE IF NOT EXISTS artifacts (
				id TEXT PRIMARY KEY,
				kind TEXT NOT NULL,
				media_type TEXT NOT NULL,
				byte_size BIGINT NOT NULL,
				sha256 TEXT NOT NULL,
				blob_key TEXT NOT NULL,
				source_meta_json TEXT NOT NULL DEFAULT '{}',
				pinned INTEGER NOT NULL DEFAULT 0,
				created_at BIGINT NOT NULL,
				last_accessed_at BIGINT NOT NULL
			)`,
		},
	},
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at BIGINT NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	for _, m := range migrations {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = $1`, m.version).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range m.stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("postgres migration v%d: %w", m.version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES ($1, $2)`, m.version, domain.NowMS()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
