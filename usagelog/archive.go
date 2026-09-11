package usagelog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

type archiveRow struct {
	EventID          string `parquet:"event_id,dict"`
	OccurredAtMillis int64  `parquet:"occurred_at_ms,timestamp(millisecond)"`
	Model            string `parquet:"model,dict"`
	Role             string `parquet:"role,dict"`
	PromptTokens     int64  `parquet:"prompt_tokens"`
	CompletionTokens int64  `parquet:"completion_tokens"`
	TotalTokens      int64  `parquet:"total_tokens"`
	CachedTokens     int64  `parquet:"cached_tokens"`
	CacheWriteTokens int64  `parquet:"cache_write_tokens"`
	Estimated        bool   `parquet:"estimated"`
	CacheEstimated   bool   `parquet:"cache_estimated"`
}

func (s *Store) ArchiveExpired(ctx context.Context) (int, error) {
	return s.ArchiveBefore(ctx, s.currentTime().Add(-HotRetention))
}

// ArchiveBefore moves complete UTC days before cutoff's UTC date to immutable
// Parquet files. Daily rollups remain in SQLite for dashboard queries.
func (s *Store) ArchiveBefore(ctx context.Context, cutoff time.Time) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("usage: store is unavailable")
	}
	cutoffDay := cutoff.UTC().Format("2006-01-02")
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT strftime('%Y-%m-%d', occurred_at_ms / 1000, 'unixepoch')
FROM usage_events
WHERE occurred_at_ms < ? AND strftime('%Y-%m-%d', occurred_at_ms / 1000, 'unixepoch') < ?
ORDER BY 1`, cutoff.UTC().UnixMilli(), cutoffDay)
	if err != nil {
		return 0, err
	}
	var days []string
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			_ = rows.Close()
			return 0, err
		}
		days = append(days, day)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	archived := 0
	for _, day := range days {
		if err := s.archiveDay(ctx, day); err != nil {
			return archived, err
		}
		archived++
	}
	return archived, nil
}

func (s *Store) archiveDay(ctx context.Context, day string) error {
	raw, err := s.rawRowsForDay(ctx, day)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	finalPath := s.archivePath(day)
	existing, err := readArchiveIfPresent(finalPath)
	if err != nil {
		return fmt.Errorf("usage: read existing archive for %s: %w", day, err)
	}
	if err := s.validateExistingArchive(ctx, day, finalPath, existing); err != nil {
		return err
	}
	merged := mergeArchiveRows(existing, raw)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return fmt.Errorf("usage: create archive directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(finalPath), ".usage-*.parquet.tmp")
	if err != nil {
		return fmt.Errorf("usage: create archive temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	writer := parquet.NewGenericWriter[archiveRow](temporary, parquet.Compression(&zstd.Codec{}))
	if _, err := writer.Write(merged); err != nil {
		return fmt.Errorf("usage: write Parquet for %s: %w", day, err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("usage: close Parquet for %s: %w", day, err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("usage: sync Parquet for %s: %w", day, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("usage: close archive file for %s: %w", day, err)
	}
	verified, err := parquet.ReadFile[archiveRow](temporaryPath)
	if err != nil {
		return fmt.Errorf("usage: verify Parquet for %s: %w", day, err)
	}
	if !sameArchiveRows(merged, verified) {
		return fmt.Errorf("usage: Parquet verification mismatch for %s", day)
	}
	if err := replaceFile(temporaryPath, finalPath); err != nil {
		return fmt.Errorf("usage: publish Parquet for %s: %w", day, err)
	}
	keep = true
	digest, err := fileSHA256(finalPath)
	if err != nil {
		return err
	}
	var total int64
	for _, row := range merged {
		total += row.TotalTokens
	}
	relative, err := filepath.Rel(s.dir, finalPath)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
INSERT INTO usage_archives(day, path, sha256, rows, total_tokens, archived_at_ms)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(day) DO UPDATE SET
  path = excluded.path, sha256 = excluded.sha256, rows = excluded.rows,
  total_tokens = excluded.total_tokens, archived_at_ms = excluded.archived_at_ms`,
		day, filepath.ToSlash(relative), digest, len(merged), total, s.currentTime().UnixMilli())
	if err != nil {
		return fmt.Errorf("usage: record archive manifest: %w", err)
	}
	start, end, err := dayBounds(day)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_events WHERE occurred_at_ms >= ? AND occurred_at_ms < ?`, start.UnixMilli(), end.UnixMilli()); err != nil {
		return fmt.Errorf("usage: delete archived events: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("usage: commit archive manifest: %w", err)
	}
	return nil
}

func (s *Store) validateExistingArchive(ctx context.Context, day, path string, rows []archiveRow) error {
	manifest, err := s.archiveManifest(ctx, day)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("usage: archive %s has a manifest but its file is unavailable: %w", day, err)
	}
	digest, err := fileSHA256(path)
	if err != nil {
		return err
	}
	var total int64
	for _, row := range rows {
		total += row.TotalTokens
	}
	if digest != manifest.SHA256 || int64(len(rows)) != manifest.Rows || total != manifest.TotalTokens {
		return fmt.Errorf("usage: archive %s does not match its manifest", day)
	}
	return nil
}

func (s *Store) rawRowsForDay(ctx context.Context, day string) ([]archiveRow, error) {
	start, end, err := dayBounds(day)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT event_id, occurred_at_ms, model, role, prompt_tokens, completion_tokens,
       total_tokens, cached_tokens, cache_write_tokens, estimated, cache_estimated
FROM usage_events WHERE occurred_at_ms >= ? AND occurred_at_ms < ? ORDER BY event_id`, start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []archiveRow
	for rows.Next() {
		var value archiveRow
		if err := rows.Scan(&value.EventID, &value.OccurredAtMillis, &value.Model, &value.Role,
			&value.PromptTokens, &value.CompletionTokens, &value.TotalTokens,
			&value.CachedTokens, &value.CacheWriteTokens, &value.Estimated, &value.CacheEstimated); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) archivePath(day string) string {
	parsed, _ := time.Parse("2006-01-02", day)
	return filepath.Join(s.ArchiveDirectory(), parsed.Format("2006"), parsed.Format("01"), "usage-"+day+".parquet")
}

func dayBounds(day string) (time.Time, time.Time, error) {
	start, err := time.Parse("2006-01-02", day)
	if err != nil || start.Format("2006-01-02") != day {
		return time.Time{}, time.Time{}, fmt.Errorf("usage: invalid archive day %q", day)
	}
	return start.UTC(), start.Add(24 * time.Hour).UTC(), nil
}

func readArchiveIfPresent(path string) ([]archiveRow, error) {
	values, err := parquet.ReadFile[archiveRow](path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return values, err
}

func mergeArchiveRows(groups ...[]archiveRow) []archiveRow {
	byID := make(map[string]archiveRow)
	for _, group := range groups {
		for _, row := range group {
			byID[row.EventID] = row
		}
	}
	values := make([]archiveRow, 0, len(byID))
	for _, row := range byID {
		values = append(values, row)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].EventID < values[j].EventID })
	return values
}

func sameArchiveRows(left, right []archiveRow) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Store) archiveManifest(ctx context.Context, day string) (archiveManifest, error) {
	var value archiveManifest
	err := s.db.QueryRowContext(ctx, `SELECT day, path, sha256, rows, total_tokens FROM usage_archives WHERE day = ?`, day).
		Scan(&value.Day, &value.Path, &value.SHA256, &value.Rows, &value.TotalTokens)
	if errors.Is(err, sql.ErrNoRows) {
		return archiveManifest{}, err
	}
	return value, err
}
