package sqlcommon

import (
	"context"
	"database/sql"
	"errors"

	"github.com/snowmerak/q/v2/pkg/storage"
)

// Store implements storage.RDBStore over any standard database/sql handle and Dialect.
type Store struct {
	db      *sql.DB
	dialect Dialect
}

// New wraps an opened, migrated *sql.DB with its SQL dialect adapter.
func New(db *sql.DB, dialect Dialect) *Store {
	return &Store{db: db, dialect: dialect}
}

func (s *Store) DB() *sql.DB { return s.db }

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

func (s *Store) Tx(ctx context.Context, fn func(tx storage.RDBTx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return s.dialect.MapError(err)
	}
	wrapped := &txRunner{base: repoBase{db: s.db, tx: tx, q: tx, d: s.dialect}}
	if err := fn(wrapped); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return s.dialect.MapError(err)
	}
	return nil
}

func (s *Store) base() repoBase {
	return repoBase{db: s.db, q: s.db, d: s.dialect}
}

func (s *Store) Personas() storage.PersonaRepo     { return personaRepo{s.base()} }
func (s *Store) Characters() storage.CharacterRepo { return characterRepo{s.base()} }
func (s *Store) Rooms() storage.RoomRepo           { return roomRepo{s.base()} }
func (s *Store) Turns() storage.TurnRepo           { return turnRepo{s.base()} }
func (s *Store) Messages() storage.MessageRepo     { return messageRepo{s.base()} }
func (s *Store) Tools() storage.ToolInvocationRepo { return toolRepo{s.base()} }
func (s *Store) Memories() storage.MemoryRepo      { return memoryRepo{s.base()} }
func (s *Store) Knowledge() storage.KnowledgeRepo  { return knowledgeRepo{s.base()} }
func (s *Store) Outbox() storage.OutboxRepo        { return outboxRepo{s.base()} }
func (s *Store) Artifacts() storage.ArtifactRepo   { return artifactRepo{s.base()} }

type txRunner struct {
	base repoBase
}

func (t *txRunner) Personas() storage.PersonaRepo     { return personaRepo{t.base} }
func (t *txRunner) Characters() storage.CharacterRepo { return characterRepo{t.base} }
func (t *txRunner) Rooms() storage.RoomRepo           { return roomRepo{t.base} }
func (t *txRunner) Turns() storage.TurnRepo           { return turnRepo{t.base} }
func (t *txRunner) Messages() storage.MessageRepo     { return messageRepo{t.base} }
func (t *txRunner) Tools() storage.ToolInvocationRepo { return toolRepo{t.base} }
func (t *txRunner) Memories() storage.MemoryRepo      { return memoryRepo{t.base} }
func (t *txRunner) Knowledge() storage.KnowledgeRepo  { return knowledgeRepo{t.base} }
func (t *txRunner) Outbox() storage.OutboxRepo        { return outboxRepo{t.base} }
func (t *txRunner) Artifacts() storage.ArtifactRepo   { return artifactRepo{t.base} }

type repoBase struct {
	db *sql.DB
	tx *sql.Tx
	q  Execer
	d  Dialect
}

func (r repoBase) mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrNotFound
	}
	return r.d.MapError(err)
}
