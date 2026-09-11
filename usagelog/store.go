package usagelog

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/snowmerak/q/client"
	_ "modernc.org/sqlite"
)

const databaseFileName = "usage.sqlite"

type Store struct {
	db  *sql.DB
	dir string
	now func() time.Time
}

func OpenStore(configDir string) (*Store, error) {
	return openStore(configDir, time.Now)
}

func openStore(configDir string, now func() time.Time) (*Store, error) {
	if strings.TrimSpace(configDir) == "" {
		return nil, errors.New("usage: config directory is unavailable")
	}
	root := filepath.Join(configDir, "usage")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("usage: create store directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("usage: secure store directory: %w", err)
	}
	path := filepath.Join(root, databaseFileName)
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)")
	if err != nil {
		return nil, fmt.Errorf("usage: open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, dir: root, now: now}
	if err := store.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("usage: secure SQLite: %w", err)
	}
	if err := store.importLegacy(configDir); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) DBPath() string {
	if s == nil {
		return ""
	}
	return filepath.Join(s.dir, databaseFileName)
}

func (s *Store) ArchiveDirectory() string {
	if s == nil {
		return ""
	}
	return filepath.Join(s.dir, "archive")
}

func (s *Store) initialize() error {
	if s == nil || s.db == nil {
		return errors.New("usage: store is unavailable")
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("usage: read schema version: %w", err)
	}
	if version > 1 {
		return fmt.Errorf("usage: database schema version %d is newer than supported version 1", version)
	}
	if version == 1 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("usage: begin schema migration: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
CREATE TABLE IF NOT EXISTS usage_events (
  event_id TEXT PRIMARY KEY,
  occurred_at_ms INTEGER NOT NULL,
  model TEXT NOT NULL,
  role TEXT NOT NULL,
  prompt_tokens INTEGER NOT NULL CHECK (prompt_tokens >= 0),
  completion_tokens INTEGER NOT NULL CHECK (completion_tokens >= 0),
  total_tokens INTEGER NOT NULL CHECK (total_tokens >= 0),
  cached_tokens INTEGER NOT NULL CHECK (cached_tokens >= 0),
  cache_write_tokens INTEGER NOT NULL CHECK (cache_write_tokens >= 0),
  estimated INTEGER NOT NULL CHECK (estimated IN (0, 1)),
  cache_estimated INTEGER NOT NULL CHECK (cache_estimated IN (0, 1))
) STRICT;
CREATE INDEX IF NOT EXISTS usage_events_at_model ON usage_events(occurred_at_ms, model);
CREATE INDEX IF NOT EXISTS usage_events_at_role ON usage_events(occurred_at_ms, role);
CREATE TABLE IF NOT EXISTS usage_daily (
  day TEXT NOT NULL,
  model TEXT NOT NULL,
  role TEXT NOT NULL,
  calls INTEGER NOT NULL,
  prompt_tokens INTEGER NOT NULL,
  completion_tokens INTEGER NOT NULL,
  total_tokens INTEGER NOT NULL,
  cached_tokens INTEGER NOT NULL,
  cache_write_tokens INTEGER NOT NULL,
  estimated_calls INTEGER NOT NULL,
  cache_estimated_calls INTEGER NOT NULL,
  PRIMARY KEY(day, model, role)
) STRICT;
CREATE TABLE IF NOT EXISTS usage_archives (
  day TEXT PRIMARY KEY,
  path TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  rows INTEGER NOT NULL,
  total_tokens INTEGER NOT NULL,
  archived_at_ms INTEGER NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS usage_imports (
  source_path TEXT NOT NULL,
  source_sha256 TEXT NOT NULL,
  rows INTEGER NOT NULL,
  invalid_rows INTEGER NOT NULL,
  imported_at_ms INTEGER NOT NULL,
  PRIMARY KEY(source_path, source_sha256)
) STRICT;`)
	if err != nil {
		return fmt.Errorf("usage: initialize SQLite: %w", err)
	}
	if _, err := tx.Exec(`PRAGMA user_version = 1`); err != nil {
		return fmt.Errorf("usage: set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("usage: commit schema migration: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Append(ctx context.Context, record client.UsageRecord) (bool, error) {
	if ctx == nil {
		return false, errors.New("usage: append context is nil")
	}
	if err := normalizeRecord(&record, s.currentTime()); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	inserted, err := appendRecord(ctx, tx, record)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return inserted, nil
}

type recordTransaction interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func appendRecord(ctx context.Context, tx recordTransaction, record client.UsageRecord) (bool, error) {
	result, err := tx.ExecContext(ctx, `
INSERT INTO usage_events(
  event_id, occurred_at_ms, model, role, prompt_tokens, completion_tokens,
  total_tokens, cached_tokens, cache_write_tokens, estimated, cache_estimated
) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(event_id) DO NOTHING`,
		record.EventID, record.At.UnixMilli(), record.Model, record.Role,
		record.PromptTokens, record.CompletionTokens, record.TotalTokens,
		record.CachedTokens, record.CacheWriteTokens, boolInt(record.Estimated), boolInt(record.CacheEstimated),
	)
	if err != nil {
		return false, fmt.Errorf("usage: insert event: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 0 {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO usage_daily(
  day, model, role, calls, prompt_tokens, completion_tokens, total_tokens,
  cached_tokens, cache_write_tokens, estimated_calls, cache_estimated_calls
) VALUES(?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(day, model, role) DO UPDATE SET
  calls = calls + 1,
  prompt_tokens = prompt_tokens + excluded.prompt_tokens,
  completion_tokens = completion_tokens + excluded.completion_tokens,
  total_tokens = total_tokens + excluded.total_tokens,
  cached_tokens = cached_tokens + excluded.cached_tokens,
  cache_write_tokens = cache_write_tokens + excluded.cache_write_tokens,
  estimated_calls = estimated_calls + excluded.estimated_calls,
  cache_estimated_calls = cache_estimated_calls + excluded.cache_estimated_calls`,
		record.At.UTC().Format("2006-01-02"), record.Model, record.Role,
		record.PromptTokens, record.CompletionTokens, record.TotalTokens,
		record.CachedTokens, record.CacheWriteTokens, boolInt(record.Estimated), boolInt(record.CacheEstimated),
	)
	if err != nil {
		return false, fmt.Errorf("usage: update daily rollup: %w", err)
	}
	return true, nil
}

func normalizeRecord(record *client.UsageRecord, now time.Time) error {
	if record == nil {
		return errors.New("usage: record is nil")
	}
	record.EventID = strings.ToLower(strings.TrimSpace(record.EventID))
	decoded, err := hex.DecodeString(record.EventID)
	if err != nil || (len(decoded) != 16 && len(decoded) != 32) {
		return errors.New("usage: event_id must be 32 or 64 lowercase hexadecimal characters")
	}
	if record.At.IsZero() {
		record.At = now.UTC()
	} else {
		record.At = record.At.UTC()
	}
	record.Model = strings.TrimSpace(record.Model)
	if record.Model == "" {
		record.Model = "unknown"
	}
	if len(record.Model) > 256 {
		return errors.New("usage: model exceeds 256 bytes")
	}
	record.Role = normalizeRole(record.Role)
	counts := []int{record.PromptTokens, record.CompletionTokens, record.TotalTokens, record.CachedTokens, record.CacheWriteTokens}
	for _, count := range counts {
		if count < 0 {
			return errors.New("usage: token counts must not be negative")
		}
	}
	return nil
}

func normalizeRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || len(role) > 64 {
		return client.UsageRoleUnknown
	}
	for _, value := range role {
		if (value < 'a' || value > 'z') && (value < '0' || value > '9') && value != '_' && value != '-' {
			return client.UsageRoleUnknown
		}
	}
	return role
}

func (s *Store) Query(ctx context.Context, filter Filter) (UsageView, error) {
	if s == nil || s.db == nil {
		return UsageView{}, errors.New("usage: store is unavailable")
	}
	if ctx == nil {
		return UsageView{}, errors.New("usage: query context is nil")
	}
	filter = effectiveFilter(filter, s.currentTime())
	if !filter.From.Before(filter.To) {
		return UsageView{}, errors.New("usage: from must be before to")
	}
	view := UsageView{From: filter.From, To: filter.To}
	var err error
	if !filter.From.Before(s.currentTime().Add(-HotRetention)) && filter.To.Sub(filter.From) <= 48*time.Hour {
		view.Resolution = "hour"
		err = s.queryEvents(ctx, filter, &view)
	} else if !filter.From.Before(s.currentTime().Add(-HotRetention)) {
		view.Resolution = "day"
		err = s.queryHybrid(ctx, filter, &view)
	} else {
		view.Resolution = "day"
		err = s.queryDaily(ctx, filter, &view)
	}
	if err != nil {
		return UsageView{}, err
	}
	if view.Series == nil {
		view.Series = []SeriesPoint{}
	}
	if view.Models == nil {
		view.Models = []DimensionTotal{}
	}
	if view.Roles == nil {
		view.Roles = []DimensionTotal{}
	}
	return view, nil
}

func effectiveFilter(filter Filter, now time.Time) Filter {
	if filter.To.IsZero() {
		filter.To = now.UTC()
	} else {
		filter.To = filter.To.UTC()
	}
	if filter.From.IsZero() {
		filter.From = filter.To.Add(-24 * time.Hour)
	} else {
		filter.From = filter.From.UTC()
	}
	filter.Model = strings.TrimSpace(filter.Model)
	filter.Role = strings.TrimSpace(filter.Role)
	return filter
}

func (s *Store) queryEvents(ctx context.Context, filter Filter, view *UsageView) error {
	where, args := eventWhere(filter)
	return scanGroupedView(ctx, s.db, `
SELECT strftime('%Y-%m-%dT%H:00:00Z', occurred_at_ms / 1000, 'unixepoch'), model, role, `+totalsSQL+`
FROM usage_events WHERE `+where+` GROUP BY 1, 2, 3 ORDER BY 1`, args, view)
}

func (s *Store) queryHybrid(ctx context.Context, filter Filter, view *UsageView) error {
	fullStart := ceilUTCDay(filter.From)
	fullEnd := floorUTCDay(filter.To)
	dailyWhere, dailyArgs := dimensionWhere("day >= ? AND day < ?", []any{
		fullStart.Format("2006-01-02"), fullEnd.Format("2006-01-02"),
	}, filter)
	eventWhere, eventArgs := dimensionWhere(`occurred_at_ms >= ? AND occurred_at_ms < ?
AND (occurred_at_ms < ? OR occurred_at_ms >= ?)`, []any{
		filter.From.UnixMilli(), filter.To.UnixMilli(), fullStart.UnixMilli(), fullEnd.UnixMilli(),
	}, filter)
	query := `
WITH hybrid(day, model, role, calls, prompt_tokens, completion_tokens, total_tokens,
            cached_tokens, cache_write_tokens, estimated_calls, cache_estimated_calls) AS (
  SELECT day, model, role, ` + rollupColumnsSQL + ` FROM usage_daily WHERE ` + dailyWhere + `
  UNION ALL
  SELECT strftime('%Y-%m-%d', occurred_at_ms / 1000, 'unixepoch'), model, role, ` + totalsSQL + `
  FROM usage_events WHERE ` + eventWhere + ` GROUP BY 1, 2, 3
)
SELECT day, model, role, ` + rollupTotalsSQL + ` FROM hybrid GROUP BY 1, 2, 3 ORDER BY 1`
	return scanGroupedView(ctx, s.db, query, append(dailyArgs, eventArgs...), view)
}

func (s *Store) queryDaily(ctx context.Context, filter Filter, view *UsageView) error {
	where, args := dailyWhere(filter)
	return scanGroupedView(ctx, s.db, `SELECT day, model, role, `+rollupColumnsSQL+`
FROM usage_daily WHERE `+where+` ORDER BY day`, args, view)
}

const totalsSQL = `COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
COALESCE(SUM(total_tokens),0), COALESCE(SUM(cached_tokens),0), COALESCE(SUM(cache_write_tokens),0),
COALESCE(SUM(estimated),0), COALESCE(SUM(cache_estimated),0)`

const rollupTotalsSQL = `COALESCE(SUM(calls),0), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
COALESCE(SUM(total_tokens),0), COALESCE(SUM(cached_tokens),0), COALESCE(SUM(cache_write_tokens),0),
COALESCE(SUM(estimated_calls),0), COALESCE(SUM(cache_estimated_calls),0)`

const rollupColumnsSQL = `calls, prompt_tokens, completion_tokens, total_tokens, cached_tokens,
cache_write_tokens, estimated_calls, cache_estimated_calls`

func scanGroupedView(ctx context.Context, db *sql.DB, query string, args []any, view *UsageView) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	series := make(map[string]Totals)
	models := make(map[string]Totals)
	roles := make(map[string]Totals)
	for rows.Next() {
		var bucket, model, role string
		var totals Totals
		if err := rows.Scan(&bucket, &model, &role, &totals.Calls, &totals.PromptTokens,
			&totals.CompletionTokens, &totals.TotalTokens, &totals.CachedTokens,
			&totals.CacheWriteTokens, &totals.EstimatedCalls, &totals.CacheEstimatedCalls); err != nil {
			return err
		}
		addTotals(&view.Totals, totals)
		value := series[bucket]
		addTotals(&value, totals)
		series[bucket] = value
		value = models[model]
		addTotals(&value, totals)
		models[model] = value
		value = roles[role]
		addTotals(&value, totals)
		roles[role] = value
	}
	if err := rows.Err(); err != nil {
		return err
	}
	view.Series = sortedSeries(series)
	view.Models = sortedDimensions(models)
	view.Roles = sortedDimensions(roles)
	return nil
}

func addTotals(target *Totals, value Totals) {
	target.Calls += value.Calls
	target.PromptTokens += value.PromptTokens
	target.CompletionTokens += value.CompletionTokens
	target.TotalTokens += value.TotalTokens
	target.CachedTokens += value.CachedTokens
	target.CacheWriteTokens += value.CacheWriteTokens
	target.EstimatedCalls += value.EstimatedCalls
	target.CacheEstimatedCalls += value.CacheEstimatedCalls
}

func sortedSeries(values map[string]Totals) []SeriesPoint {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]SeriesPoint, 0, len(keys))
	for _, key := range keys {
		result = append(result, SeriesPoint{Bucket: key, Totals: values[key]})
	}
	return result
}

func sortedDimensions(values map[string]Totals) []DimensionTotal {
	result := make([]DimensionTotal, 0, len(values))
	for name, totals := range values {
		result = append(result, DimensionTotal{Name: name, Totals: totals})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TotalTokens == result[j].TotalTokens {
			return result[i].Name < result[j].Name
		}
		return result[i].TotalTokens > result[j].TotalTokens
	})
	return result
}

func eventWhere(filter Filter) (string, []any) {
	where := "occurred_at_ms >= ? AND occurred_at_ms < ?"
	args := []any{filter.From.UnixMilli(), filter.To.UnixMilli()}
	return dimensionWhere(where, args, filter)
}

func dailyWhere(filter Filter) (string, []any) {
	operator := "<="
	if filter.To.Equal(floorUTCDay(filter.To)) {
		operator = "<"
	}
	where := "day >= ? AND day " + operator + " ?"
	args := []any{filter.From.Format("2006-01-02"), filter.To.Format("2006-01-02")}
	return dimensionWhere(where, args, filter)
}

func floorUTCDay(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func ceilUTCDay(value time.Time) time.Time {
	floor := floorUTCDay(value)
	if value.UTC().Equal(floor) {
		return floor
	}
	return floor.Add(24 * time.Hour)
}

func dimensionWhere(where string, args []any, filter Filter) (string, []any) {
	if filter.Model != "" {
		where += " AND model = ?"
		args = append(args, filter.Model)
	}
	if filter.Role != "" {
		where += " AND role = ?"
		args = append(args, filter.Role)
	}
	return where, args
}

func (s *Store) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
