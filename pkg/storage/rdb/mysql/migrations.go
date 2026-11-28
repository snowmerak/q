package mysql

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
				id VARCHAR(191) PRIMARY KEY,
				name VARCHAR(191) NOT NULL,
				description LONGTEXT NOT NULL,
				avatar_artifact_id VARCHAR(191) NOT NULL DEFAULT '',
				is_default INT NOT NULL DEFAULT 0,
				version BIGINT NOT NULL DEFAULT 1,
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL,
				deleted_at BIGINT NULL
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS characters (
				id VARCHAR(191) PRIMARY KEY,
				name VARCHAR(191) NOT NULL,
				avatar_artifact_id VARCHAR(191) NOT NULL DEFAULT '',
				personality LONGTEXT NOT NULL,
				background LONGTEXT NOT NULL,
				appearance LONGTEXT NOT NULL,
				initial_values_json LONGTEXT NOT NULL,
				initial_state_json LONGTEXT NOT NULL,
				model_prefs_json LONGTEXT NULL,
				approval_policy_override VARCHAR(64) NOT NULL DEFAULT '',
				version BIGINT NOT NULL DEFAULT 1,
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL,
				deleted_at BIGINT NULL
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS rooms (
				id VARCHAR(191) PRIMARY KEY,
				title VARCHAR(191) NOT NULL,
				mode VARCHAR(64) NOT NULL,
				persona_id VARCHAR(191) NOT NULL,
				persona_snapshot_json LONGTEXT NOT NULL,
				approval_mode_override VARCHAR(64) NULL,
				model_overrides_json LONGTEXT NULL,
				max_turns_per_input INT NOT NULL DEFAULT 3,
				context_version BIGINT NOT NULL DEFAULT 1,
				version BIGINT NOT NULL DEFAULT 1,
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL,
				deleted_at BIGINT NULL,
				CONSTRAINT fk_rooms_persona FOREIGN KEY (persona_id) REFERENCES personas(id)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS room_members (
				room_id VARCHAR(191) NOT NULL,
				character_id VARCHAR(191) NOT NULL,
				character_snapshot_json LONGTEXT NOT NULL,
				base_speed INT NOT NULL DEFAULT 50,
				actor_state_json LONGTEXT NOT NULL,
				active INT NOT NULL DEFAULT 1,
				joined_at BIGINT NOT NULL,
				PRIMARY KEY (room_id, character_id),
				CONSTRAINT fk_room_members_room FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE CASCADE,
				CONSTRAINT fk_room_members_char FOREIGN KEY (character_id) REFERENCES characters(id)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS turns (
				id VARCHAR(191) PRIMARY KEY,
				room_id VARCHAR(191) NOT NULL,
				request_id VARCHAR(191) NOT NULL,
				input_hash VARCHAR(191) NOT NULL,
				status VARCHAR(64) NOT NULL,
				error_code VARCHAR(64) NOT NULL DEFAULT '',
				error_message LONGTEXT NOT NULL,
				checkpoint_json LONGTEXT NOT NULL,
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL,
				completed_at BIGINT NULL,
				UNIQUE KEY uq_turns_room_request (room_id, request_id),
				CONSTRAINT fk_turns_room FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE CASCADE
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS messages (
				id VARCHAR(191) PRIMARY KEY,
				room_id VARCHAR(191) NOT NULL,
				turn_id VARCHAR(191) NOT NULL,
				seq BIGINT NOT NULL,
				role VARCHAR(64) NOT NULL,
				author_id VARCHAR(191) NOT NULL DEFAULT '',
				author_name VARCHAR(191) NOT NULL DEFAULT '',
				content LONGTEXT NOT NULL,
				subtext LONGTEXT NOT NULL,
				observed_by_json LONGTEXT NOT NULL,
				location_tag VARCHAR(191) NOT NULL DEFAULT '',
				metadata_json LONGTEXT NOT NULL,
				created_at BIGINT NOT NULL,
				UNIQUE KEY uq_messages_room_seq (room_id, seq),
				INDEX idx_messages_room_seq (room_id, seq),
				CONSTRAINT fk_messages_room FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE CASCADE,
				CONSTRAINT fk_messages_turn FOREIGN KEY (turn_id) REFERENCES turns(id) ON DELETE CASCADE
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS tool_invocations (
				id VARCHAR(191) PRIMARY KEY,
				turn_id VARCHAR(191) NOT NULL,
				room_id VARCHAR(191) NOT NULL,
				actor_id VARCHAR(191) NOT NULL,
				call_id VARCHAR(191) NOT NULL DEFAULT '',
				tool_name VARCHAR(191) NOT NULL,
				tool_source VARCHAR(64) NOT NULL,
				args_json LONGTEXT NOT NULL,
				gate_mode VARCHAR(64) NOT NULL,
				decision_reason LONGTEXT NOT NULL,
				status VARCHAR(64) NOT NULL,
				result_summary LONGTEXT NOT NULL,
				artifact_id VARCHAR(191) NULL,
				error_message LONGTEXT NOT NULL,
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL,
				INDEX idx_tool_invocations_turn (turn_id),
				CONSTRAINT fk_tool_invocations_turn FOREIGN KEY (turn_id) REFERENCES turns(id) ON DELETE CASCADE
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS user_questions (
				id VARCHAR(191) PRIMARY KEY,
				turn_id VARCHAR(191) NOT NULL,
				room_id VARCHAR(191) NOT NULL,
				actor_id VARCHAR(191) NOT NULL,
				prompt LONGTEXT NOT NULL,
				choices_json LONGTEXT NOT NULL,
				allow_freeform INT NOT NULL DEFAULT 1,
				status VARCHAR(64) NOT NULL,
				answer_text LONGTEXT NOT NULL,
				created_at BIGINT NOT NULL,
				answered_at BIGINT NULL,
				INDEX idx_user_questions_turn (turn_id),
				CONSTRAINT fk_user_questions_turn FOREIGN KEY (turn_id) REFERENCES turns(id) ON DELETE CASCADE
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS memories (
				id VARCHAR(191) PRIMARY KEY,
				room_id VARCHAR(191) NOT NULL,
				owner_id VARCHAR(191) NOT NULL,
				source_turn_id VARCHAR(191) NOT NULL DEFAULT '',
				kind VARCHAR(64) NOT NULL,
				content LONGTEXT NOT NULL,
				observed_by_json LONGTEXT NOT NULL,
				occurred_at BIGINT NOT NULL,
				pinned INT NOT NULL DEFAULT 0,
				status VARCHAR(64) NOT NULL,
				revision BIGINT NOT NULL DEFAULT 1,
				embedding_model VARCHAR(191) NOT NULL DEFAULT '',
				embedding_dim INT NOT NULL DEFAULT 0,
				embedding_blob LONGBLOB NULL,
				created_at BIGINT NOT NULL,
				INDEX idx_memories_room_owner (room_id, owner_id, occurred_at),
				CONSTRAINT fk_memories_room FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE CASCADE
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS value_jobs (
				id VARCHAR(191) PRIMARY KEY,
				room_id VARCHAR(191) NOT NULL,
				character_id VARCHAR(191) NOT NULL,
				source_turn_id VARCHAR(191) NOT NULL,
				base_values_revision BIGINT NOT NULL,
				candidates_json LONGTEXT NOT NULL,
				status VARCHAR(64) NOT NULL,
				attempts INT NOT NULL DEFAULT 0,
				last_error LONGTEXT NOT NULL,
				next_run_at BIGINT NOT NULL,
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL,
				INDEX idx_value_jobs_queue (status, next_run_at)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS knowledge_docs (
				id VARCHAR(191) PRIMARY KEY,
				scope VARCHAR(191) NOT NULL,
				title VARCHAR(191) NOT NULL,
				source_uri LONGTEXT NOT NULL,
				media_type VARCHAR(128) NOT NULL DEFAULT 'text/plain',
				content LONGTEXT NOT NULL,
				content_sha256 VARCHAR(128) NOT NULL,
				revision BIGINT NOT NULL DEFAULT 1,
				status VARCHAR(64) NOT NULL,
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL,
				INDEX idx_knowledge_docs_scope (scope, status)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS knowledge_chunks (
				id VARCHAR(191) PRIMARY KEY,
				doc_id VARCHAR(191) NOT NULL,
				doc_revision BIGINT NOT NULL,
				chunk_ordinal INT NOT NULL,
				char_offset INT NOT NULL,
				text LONGTEXT NOT NULL,
				embedding_model VARCHAR(191) NOT NULL DEFAULT '',
				embedding_dim INT NOT NULL DEFAULT 0,
				embedding_blob LONGBLOB NULL,
				INDEX idx_knowledge_chunks_doc (doc_id, chunk_ordinal),
				CONSTRAINT fk_knowledge_chunks_doc FOREIGN KEY (doc_id) REFERENCES knowledge_docs(id) ON DELETE CASCADE
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS index_outbox (
				seq BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
				entity_type VARCHAR(64) NOT NULL,
				entity_id VARCHAR(191) NOT NULL,
				op VARCHAR(32) NOT NULL,
				revision BIGINT NOT NULL,
				payload_json LONGTEXT NOT NULL,
				status VARCHAR(32) NOT NULL,
				attempts INT NOT NULL DEFAULT 0,
				next_attempt_at BIGINT NOT NULL,
				last_error LONGTEXT NOT NULL,
				created_at BIGINT NOT NULL,
				INDEX idx_index_outbox_pending (status, next_attempt_at, seq)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS artifacts (
				id VARCHAR(191) PRIMARY KEY,
				kind VARCHAR(64) NOT NULL,
				media_type VARCHAR(128) NOT NULL,
				byte_size BIGINT NOT NULL,
				sha256 VARCHAR(128) NOT NULL,
				blob_key VARCHAR(191) NOT NULL,
				source_meta_json LONGTEXT NOT NULL,
				pinned INT NOT NULL DEFAULT 0,
				created_at BIGINT NOT NULL,
				last_accessed_at BIGINT NOT NULL
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		},
	},
	{
		version: 2,
		stmts: []string{
			`ALTER TABLE rooms ADD COLUMN work_dir VARCHAR(1024) NOT NULL DEFAULT ''`,
		},
	},
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INT PRIMARY KEY, applied_at BIGINT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	for _, m := range migrations {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		for _, stmt := range m.stmts {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("mysql migration v%d: %w", m.version, err)
			}
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, m.version, domain.NowMS()); err != nil {
			return err
		}
	}
	return nil
}
