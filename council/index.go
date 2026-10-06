package council

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const indexFileName = "index.sqlite"

// openIndexLocked opens the shared index once per Store. SQLite coordinates
// readers and writers across Studio processes; Store.mu only protects this instance.
func (s *Store) openIndexLocked() (*sql.DB, error) {
	if s.db != nil {
		return s.db, nil
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Root, indexFileName)
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS councils (
  id TEXT PRIMARY KEY,
  path TEXT NOT NULL UNIQUE,
  updated_at INTEGER NOT NULL,
  document BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS councils_recent ON councils(updated_at DESC, id);
CREATE TABLE IF NOT EXISTS council_index_meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL);
`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize council index: %w", err)
	}
	if err := s.reconcileIndexLocked(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("reconcile council index: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	s.db = db
	return db, nil
}

// reconcileIndexLocked imports existing JSON councils and repairs interrupted
// file/index writes. The first statement reserves SQLite's writer lock before
// reading directories, so another Studio cannot mutate them during the scan.
func (s *Store) reconcileIndexLocked(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO council_index_meta(key, value) VALUES('reconcile', 1)
ON CONFLICT(key) DO UPDATE SET value = value + 1`); err != nil {
		return err
	}
	values, err := s.listDiskLocked()
	if err != nil {
		return err
	}
	for _, value := range values {
		if err := upsertIndex(tx, value, filepath.Join(s.path(value), "council.json")); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT id, path FROM councils`)
	if err != nil {
		return err
	}
	var missing []string
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			rows.Close()
			return err
		}
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, id)
		} else if err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range missing {
		if _, err := tx.Exec(`DELETE FROM councils WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func upsertIndex(tx *sql.Tx, value Council, path string) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO councils(id, path, updated_at, document) VALUES(?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET path = excluded.path, updated_at = excluded.updated_at, document = excluded.document
WHERE excluded.updated_at > councils.updated_at`, value.ID, path, value.UpdatedAt.UnixNano(), body)
	return err
}

func (s *Store) indexPathLocked(id string) (string, error) {
	db, err := s.openIndexLocked()
	if err != nil {
		return "", err
	}
	var path string
	if err := db.QueryRow(`SELECT path FROM councils WHERE id = ?`, id).Scan(&path); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", os.ErrNotExist
		}
		return "", err
	}
	return path, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}
