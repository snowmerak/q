// Package workitems owns the Library's workspace-scoped knowledge and work
// catalog. Only the Library leader opens its database; consumers use its API.
package workitems

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"modernc.org/sqlite"
	sqlitecode "modernc.org/sqlite/lib"
)

var (
	ErrNotFound     = errors.New("workitems: requested resource does not exist")
	ErrConflict     = errors.New("workitems: resource conflict or stale revision")
	ErrInvalidInput = errors.New("workitems: invalid workspace input")
)

const FileName = "workspace-knowledge.sqlite"

type Workspace struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	WorkspaceRoots []string `json:"workspace_roots"`
	// ManagerSessionID is the Integrator's identity, retained for compatibility.
	ManagerSessionID     string        `json:"manager_session_id"`
	CoordinatorSessionID string        `json:"coordinator_session_id"`
	CreatedAt            time.Time     `json:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at"`
	ArchivedAt           *time.Time    `json:"archived_at,omitempty"`
	Limits               ProjectLimits `json:"limits"`
}

type WorkspaceInput struct {
	Limits         *ProjectLimits `json:"limits,omitempty"`
	InitialMembers *[]MemberInput `json:"initial_members,omitempty"`
	Name           string         `json:"name"`
	WorkspaceRoots []string       `json:"workspace_roots"`
}

type Store struct{ db *sql.DB }

// Open creates the catalog and imports the old Studio catalog once, in one
// transaction. The original JSON is retained unchanged as a migration backup.
func Open(ctx context.Context, configDir string) (*Store, error) {
	dir := filepath.Join(configDir, "library")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, FileName)
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.initialize(ctx, configDir); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initialize(ctx context.Context, dir string) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > 7 {
		return fmt.Errorf("workitems: unsupported catalog schema version %d", version)
	}
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS workspaces (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, name_key TEXT NOT NULL,
 manager_session_id TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS workspace_active_name ON workspaces(name_key) WHERE archived_at IS NULL;
CREATE TABLE IF NOT EXISTS workspace_roots (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), position INTEGER NOT NULL,
 path TEXT NOT NULL, path_key TEXT NOT NULL, active INTEGER NOT NULL CHECK(active IN (0,1)),
 PRIMARY KEY(workspace_id, position)
);
CREATE UNIQUE INDEX IF NOT EXISTS workspace_active_root ON workspace_roots(path_key) WHERE active = 1;
CREATE TABLE IF NOT EXISTS catalog_migrations (name TEXT PRIMARY KEY, completed_at TEXT NOT NULL);
`)
	if err != nil {
		return fmt.Errorf("workitems: initialize catalog: %w", err)
	}
	if err := s.initializeItems(ctx); err != nil {
		return err
	}
	if err := s.initializeCoordinator(ctx); err != nil {
		return err
	}
	if err := s.initializeScheduling(ctx); err != nil {
		return err
	}
	if err := s.initializeManagerRoles(ctx, version); err != nil {
		return err
	}
	if err := s.initializeProjectSettings(ctx); err != nil {
		return err
	}
	var complete int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM catalog_migrations WHERE name = 'studio-projects-v1'`).Scan(&complete); err != nil {
		return err
	}
	if complete != 0 {
		return nil
	}
	var legacy struct {
		Version  int `json:"version"`
		Projects []struct {
			ID             string    `json:"id"`
			Name           string    `json:"name"`
			WorkspaceRoots []string  `json:"workspace_roots"`
			CreatedAt      time.Time `json:"created_at"`
			UpdatedAt      time.Time `json:"updated_at"`
		} `json:"projects"`
	}
	file, err := os.Open(filepath.Join(dir, "studio-projects.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if info.Size() > 1<<20 {
			return errors.New("workitems: legacy Studio catalog is too large")
		}
		decoder := json.NewDecoder(io.LimitReader(file, (1<<20)+1))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&legacy); err != nil {
			return fmt.Errorf("workitems: decode legacy Studio catalog: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return errors.New("workitems: legacy Studio catalog contains trailing data")
		}
		if legacy.Version != 1 {
			return fmt.Errorf("workitems: unsupported legacy Studio catalog version %d", legacy.Version)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, project := range legacy.Projects {
		if uuid.Validate(project.ID) != nil || project.CreatedAt.IsZero() || project.UpdatedAt.Before(project.CreatedAt) {
			return errors.New("workitems: invalid legacy project identity or timestamps")
		}
		input, err := normalize(WorkspaceInput{Name: project.Name, WorkspaceRoots: project.WorkspaceRoots})
		if err != nil {
			return fmt.Errorf("workitems: import project %q: %w", project.ID, err)
		}
		if input.Name != project.Name || len(input.WorkspaceRoots) != len(project.WorkspaceRoots) {
			return errors.New("workitems: legacy project contains an unnormalized name or duplicate directories")
		}
		managerID, err := uuid.NewRandom()
		if err != nil {
			return err
		}
		value := Workspace{ID: project.ID, Name: input.Name, WorkspaceRoots: input.WorkspaceRoots, ManagerSessionID: managerID.String(), CoordinatorSessionID: uuid.NewString(), CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
		if err := insertWorkspace(ctx, tx, value); err != nil {
			return fmt.Errorf("workitems: import project %q: %w", value.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_migrations(name, completed_at) VALUES('studio-projects-v1', ?)`, stamp(time.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

// initializeManagerRoles preserves the original manager as Integrator. The
// new intake conversation gets its own stable identity, including archived workspaces.
func (s *Store) initializeManagerRoles(ctx context.Context, version int) error {
	if version >= 6 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `ALTER TABLE workspaces ADD COLUMN coordinator_session_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM workspaces`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	readErr := rows.Err()
	if err := errors.Join(readErr, rows.Close()); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE workspaces SET coordinator_session_id=? WHERE id=?`, uuid.NewString(), id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX workspace_coordinator_session ON workspaces(coordinator_session_id); PRAGMA user_version=6`); err != nil {
		return err
	}
	return tx.Commit()
}

func normalize(input WorkspaceInput) (WorkspaceInput, error) {
	if input.Limits != nil {
		if err := input.Limits.Validate(); err != nil {
			return WorkspaceInput{}, err
		}
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 128 {
		return WorkspaceInput{}, fmt.Errorf("%w: name must contain 1 to 128 bytes", ErrInvalidInput)
	}
	if len(input.WorkspaceRoots) < 1 || len(input.WorkspaceRoots) > 32 {
		return WorkspaceInput{}, fmt.Errorf("%w: 1 to 32 directories are required", ErrInvalidInput)
	}
	roots := make([]string, 0, len(input.WorkspaceRoots))
	seen := make(map[string]bool)
	for _, root := range input.WorkspaceRoots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return WorkspaceInput{}, fmt.Errorf("%w: directories must be clean absolute paths", ErrInvalidInput)
		}
		key, err := pathKey(root)
		if err != nil {
			return WorkspaceInput{}, err
		}
		if !seen[key] {
			roots = append(roots, root)
			seen[key] = true
		}
	}
	input.WorkspaceRoots = roots
	return input, nil
}

func pathKey(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil {
		path = resolved
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path, nil
}

func stamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func insertWorkspace(ctx context.Context, tx *sql.Tx, value Workspace) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces(id,name,name_key,manager_session_id,coordinator_session_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
		value.ID, value.Name, strings.ToLower(value.Name), value.ManagerSessionID, value.CoordinatorSessionID, stamp(value.CreatedAt), stamp(value.UpdatedAt)); err != nil {
		return mutationError(err)
	}
	return insertRoots(ctx, tx, value.ID, value.WorkspaceRoots)
}

func insertRoots(ctx context.Context, tx *sql.Tx, id string, roots []string) error {
	for position, root := range roots {
		key, err := pathKey(root)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_roots(workspace_id,position,path,path_key,active) VALUES(?,?,?,?,1)`, id, position, root, key); err != nil {
			return mutationError(err)
		}
	}
	return nil
}

func mutationError(err error) error {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && (sqliteErr.Code() == sqlitecode.SQLITE_CONSTRAINT_UNIQUE || sqliteErr.Code() == sqlitecode.SQLITE_CONSTRAINT_PRIMARYKEY) {
		return ErrConflict
	}
	return err
}

func (s *Store) ListWorkspaces(ctx context.Context, includeArchived bool) ([]Workspace, error) {
	query := `SELECT w.id,w.name,w.manager_session_id,w.coordinator_session_id,w.created_at,w.updated_at,w.archived_at,r.path,coalesce(p.max_parallel_tickets,0),coalesce(p.max_parallel_subagents,0) FROM workspaces w JOIN workspace_roots r ON r.workspace_id=w.id LEFT JOIN workspace_settings p ON p.workspace_id=w.id`
	if !includeArchived {
		query += ` WHERE w.archived_at IS NULL`
	}
	query += ` ORDER BY w.name_key,w.id,r.position`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := []Workspace{}
	for rows.Next() {
		var value Workspace
		var created, updated, path string
		var archived sql.NullString
		if err := rows.Scan(&value.ID, &value.Name, &value.ManagerSessionID, &value.CoordinatorSessionID, &created, &updated, &archived, &path, &value.Limits.MaxParallelTickets, &value.Limits.MaxParallelSubagents); err != nil {
			return nil, err
		}
		if len(values) == 0 || values[len(values)-1].ID != value.ID {
			value.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
			if err != nil {
				return nil, err
			}
			value.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
			if err != nil {
				return nil, err
			}
			if archived.Valid {
				at, err := time.Parse(time.RFC3339Nano, archived.String)
				if err != nil {
					return nil, err
				}
				value.ArchivedAt = &at
			}
			values = append(values, value)
		}
		values[len(values)-1].WorkspaceRoots = append(values[len(values)-1].WorkspaceRoots, path)
	}
	return values, rows.Err()
}

func (s *Store) CreateWorkspace(ctx context.Context, input WorkspaceInput) (Workspace, error) {
	input, err := normalize(input)
	if err != nil {
		return Workspace{}, err
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return Workspace{}, err
	}
	manager, err := uuid.NewRandom()
	if err != nil {
		return Workspace{}, err
	}
	now := time.Now().UTC()
	value := Workspace{ID: id.String(), Name: input.Name, WorkspaceRoots: input.WorkspaceRoots, ManagerSessionID: manager.String(), CoordinatorSessionID: uuid.NewString(), CreatedAt: now, UpdatedAt: now}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Workspace{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertWorkspace(ctx, tx, value); err != nil {
		return Workspace{}, err
	}
	if input.Limits != nil {
		value.Limits = *input.Limits
	}
	if err := saveProjectLimits(ctx, tx, value.ID, value.Limits); err != nil {
		return Workspace{}, err
	}
	members := input.InitialMembers
	if members == nil {
		defaults, err := projectDefaultsTx(ctx, tx)
		if err != nil {
			return Workspace{}, err
		}
		initial := make([]MemberInput, 0, len(defaults.AgentIDs))
		for _, agent := range defaults.AgentIDs {
			initial = append(initial, MemberInput{AgentID: agent, DefinitionRoot: value.WorkspaceRoots[0], ExecutionRoot: value.WorkspaceRoots[0]})
		}
		members = &initial
	}
	if len(*members) > 64 {
		return Workspace{}, ErrInvalidInput
	}
	for _, member := range *members {
		if member.ID != "" || member.Revision != 0 {
			return Workspace{}, ErrInvalidInput
		}
		if _, err := putMemberTx(ctx, tx, value.ID, member); err != nil {
			return Workspace{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return Workspace{}, err
	}
	return value, nil
}

func (s *Store) UpdateWorkspace(ctx context.Context, id string, input WorkspaceInput) (Workspace, error) {
	if input.InitialMembers != nil {
		return Workspace{}, fmt.Errorf("%w: initial_members is only accepted at creation", ErrInvalidInput)
	}
	input, err := normalize(input)
	if err != nil {
		return Workspace{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Workspace{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var value Workspace
	var created string
	if err := tx.QueryRowContext(ctx, `SELECT id,manager_session_id,coordinator_session_id,created_at FROM workspaces WHERE id=? AND archived_at IS NULL`, id).Scan(&value.ID, &value.ManagerSessionID, &value.CoordinatorSessionID, &created); errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, ErrNotFound
	} else if err != nil {
		return Workspace{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT max_parallel_tickets FROM workspace_settings WHERE workspace_id=?),0),coalesce((SELECT max_parallel_subagents FROM workspace_settings WHERE workspace_id=?),0)`, id, id).Scan(&value.Limits.MaxParallelTickets, &value.Limits.MaxParallelSubagents); err != nil {
		return Workspace{}, err
	}
	if input.Limits != nil {
		value.Limits = *input.Limits
		if err := saveProjectLimits(ctx, tx, id, value.Limits); err != nil {
			return Workspace{}, err
		}
	}
	value.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Workspace{}, err
	}
	value.Name, value.WorkspaceRoots, value.UpdatedAt = input.Name, input.WorkspaceRoots, time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE workspaces SET name=?,name_key=?,updated_at=? WHERE id=?`, value.Name, strings.ToLower(value.Name), stamp(value.UpdatedAt), id); err != nil {
		return Workspace{}, mutationError(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_roots WHERE workspace_id=?`, id); err != nil {
		return Workspace{}, err
	}
	if err := insertRoots(ctx, tx, id, input.WorkspaceRoots); err != nil {
		return Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return Workspace{}, err
	}
	return value, nil
}

func (s *Store) ArchiveWorkspace(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := stamp(time.Now())
	result, err := tx.ExecContext(ctx, `UPDATE workspaces SET archived_at=?,updated_at=? WHERE id=? AND archived_at IS NULL`, now, now, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_roots SET active=0 WHERE workspace_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
