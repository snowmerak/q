package workitems

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

type ProjectLimits struct {
	MaxParallelTickets   int `json:"max_parallel_tickets"`
	MaxParallelSubagents int `json:"max_parallel_subagents"`
}

func (l ProjectLimits) Validate() error {
	if l.MaxParallelTickets < 0 || l.MaxParallelTickets > 64 || l.MaxParallelSubagents < 0 || l.MaxParallelSubagents > 64 {
		return fmt.Errorf("%w: project limits must be between 0 (inherit) and 64", ErrInvalidInput)
	}
	return nil
}

func EffectiveParallel(project, global int) int {
	if global <= 0 {
		global = 3
	}
	if project <= 0 {
		return global
	}
	return min(project, global)
}

type ProjectDefaults struct {
	AgentIDs []string `json:"agent_ids"`
	Revision int64    `json:"revision"`
}

func (s *Store) initializeProjectSettings(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS workspace_settings (
workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id), max_parallel_tickets INTEGER NOT NULL DEFAULT 0, max_parallel_subagents INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS workspace_defaults (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL, agent_ids BLOB NOT NULL);
INSERT OR IGNORE INTO workspace_defaults VALUES(1,1,'[]'); PRAGMA user_version=7;`)
	return err
}

func saveProjectLimits(ctx context.Context, tx *sql.Tx, id string, limits ProjectLimits) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO workspace_settings VALUES(?,?,?) ON CONFLICT(workspace_id) DO UPDATE SET max_parallel_tickets=excluded.max_parallel_tickets,max_parallel_subagents=excluded.max_parallel_subagents`, id, limits.MaxParallelTickets, limits.MaxParallelSubagents)
	return err
}

func projectDefaultsTx(ctx context.Context, tx *sql.Tx) (ProjectDefaults, error) {
	var value ProjectDefaults
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT revision,agent_ids FROM workspace_defaults WHERE id=1`).Scan(&value.Revision, &raw); err != nil {
		return value, err
	}
	err := json.Unmarshal(raw, &value.AgentIDs)
	return value, err
}

func (s *Store) GetProjectDefaults(ctx context.Context) (ProjectDefaults, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectDefaults{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return projectDefaultsTx(ctx, tx)
}

func (s *Store) UpdateProjectDefaults(ctx context.Context, value ProjectDefaults) (ProjectDefaults, error) {
	if len(value.AgentIDs) > 64 {
		return value, ErrInvalidInput
	}
	seen := map[string]bool{}
	for _, id := range value.AgentIDs {
		if id != strings.TrimSpace(id) || (!strings.HasPrefix(id, "builtin/") && !strings.HasPrefix(id, "global/")) || len(id) > 256 || strings.Count(id, "/") != 1 || strings.HasSuffix(id, "/") || seen[id] {
			return value, ErrInvalidInput
		}
		seen[id] = true
	}
	if value.AgentIDs == nil {
		value.AgentIDs = []string{}
	}
	raw, err := json.Marshal(value.AgentIDs)
	if err != nil {
		return value, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE workspace_defaults SET revision=revision+1,agent_ids=? WHERE id=1 AND revision=?`, raw, value.Revision)
	if err != nil {
		return value, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return value, err
	}
	if n != 1 {
		return value, ErrConflict
	}
	value.Revision++
	return value, nil
}
