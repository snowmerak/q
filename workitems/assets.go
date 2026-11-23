package workitems

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/snowmerak/q/internal/workmedia"
)

const AssetPrefix = "q-asset:"

type Asset struct {
	ID   string `json:"id"`
	MIME string `json:"mime"`
	Data []byte `json:"data,omitempty"`
}

func (s *Store) initializeAssets(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS work_assets (
workspace_id TEXT NOT NULL REFERENCES workspaces(id), id TEXT NOT NULL,
mime TEXT NOT NULL, data BLOB NOT NULL, PRIMARY KEY(workspace_id, id)
)`)
	return err
}

func (s *Store) PutAsset(ctx context.Context, workspace string, data []byte, imagesOnly bool) (_ Asset, returnErr error) {
	mime := http.DetectContentType(data)
	if !workmedia.Supported(mime) {
		if imagesOnly || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
			return Asset{}, ErrInvalidInput
		}
		mime = "text/plain; charset=utf-8"
	}
	if len(data) == 0 || len(data) > workmedia.MaxImageBytes {
		return Asset{}, ErrInvalidInput
	}
	digest := sha256.Sum256(data)
	asset := Asset{ID: hex.EncodeToString(digest[:]), MIME: mime}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Asset{}, err
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			returnErr = errors.Join(returnErr, err)
		}
	}()
	if err := requireWorkspace(ctx, tx, workspace); err != nil {
		return Asset{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_assets (workspace_id, id, mime, data) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`, workspace, asset.ID, mime, data); err != nil {
		return Asset{}, err
	}
	return asset, tx.Commit()
}

func (s *Store) GetAsset(ctx context.Context, workspace, id string) (Asset, error) {
	asset := Asset{ID: id}
	err := s.db.QueryRowContext(ctx, `SELECT mime, data FROM work_assets WHERE workspace_id=? AND id=?`, workspace, id).Scan(&asset.MIME, &asset.Data)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return asset, err
}
