// Package sqlite implements storage.RDBStore backed by modernc.org/sqlite.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/snowmerak/q/v2/pkg/storage"
)

type dbExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Store struct {
	db *sql.DB
	repoSet
}

var _ storage.RDBStore = (*Store)(nil)

type txStore struct {
	repoSet
}

var _ storage.RDBTx = (*txStore)(nil)

type repoSet struct {
	personas   *personaRepo
	characters *characterRepo
	rooms      *roomRepo
	turns      *turnRepo
	messages   *messageRepo
	tools      *toolRepo
	memories   *memoryRepo
	knowledge  *knowledgeRepo
	outbox     *outboxRepo
	artifacts  *artifactRepo
}

func newRepoSet(exec dbExec) repoSet {
	return repoSet{
		personas:   &personaRepo{db: exec},
		characters: &characterRepo{db: exec},
		rooms:      &roomRepo{db: exec},
		turns:      &turnRepo{db: exec},
		messages:   &messageRepo{db: exec},
		tools:      &toolRepo{db: exec},
		memories:   &memoryRepo{db: exec},
		knowledge:  &knowledgeRepo{db: exec},
		outbox:     &outboxRepo{db: exec},
		artifacts:  &artifactRepo{db: exec},
	}
}

func (r repoSet) Personas() storage.PersonaRepo     { return r.personas }
func (r repoSet) Characters() storage.CharacterRepo { return r.characters }
func (r repoSet) Rooms() storage.RoomRepo           { return r.rooms }
func (r repoSet) Turns() storage.TurnRepo           { return r.turns }
func (r repoSet) Messages() storage.MessageRepo     { return r.messages }
func (r repoSet) Tools() storage.ToolInvocationRepo { return r.tools }
func (r repoSet) Memories() storage.MemoryRepo      { return r.memories }
func (r repoSet) Knowledge() storage.KnowledgeRepo  { return r.knowledge }
func (r repoSet) Outbox() storage.OutboxRepo        { return r.outbox }
func (r repoSet) Artifacts() storage.ArtifactRepo   { return r.artifacts }

// Open initializes or opens a WAL-mode SQLite database at path and applies schema migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return nil, fmt.Errorf("%w: sqlite path is required", storage.ErrInvalidInput)
	}
	if clean != ":memory:" && !strings.HasPrefix(clean, "file:") {
		if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite parent dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", clean)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	pragmas := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA synchronous = NORMAL`,
	}
	for _, pragma := range pragmas {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("sqlite %s: %w", pragma, err)
		}
	}
	if err := applyMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{
		db:      db,
		repoSet: newRepoSet(db),
	}, nil
}

func (s *Store) Tx(ctx context.Context, fn func(tx storage.RDBTx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(&txStore{repoSet: newRepoSet(tx)}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return storage.ErrUnavailable
	}
	return s.db.PingContext(ctx)
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
