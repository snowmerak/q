package usagelog

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/snowmerak/q/client"
)

const analyticsSchema = `
ALTER TABLE usage_daily ADD COLUMN cache_hit_calls INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_daily ADD COLUMN cache_hit_samples INTEGER NOT NULL DEFAULT 0;

UPDATE usage_daily AS daily
SET cache_hit_calls = events.hits, cache_hit_samples = events.samples
FROM (
  SELECT strftime('%Y-%m-%d', occurred_at_ms / 1000, 'unixepoch') AS day, model, role,
         SUM(cached_tokens > 0) AS hits, COUNT(*) AS samples
  FROM usage_events WHERE cache_estimated = 0 GROUP BY 1, 2, 3
) AS events
WHERE daily.day = events.day AND daily.model = events.model AND daily.role = events.role;

CREATE TABLE usage_attribution (
  event_id TEXT PRIMARY KEY REFERENCES usage_events(event_id) ON DELETE CASCADE,
  project_id TEXT NOT NULL,
  project_name TEXT NOT NULL,
  subagent TEXT NOT NULL
) STRICT;

CREATE TABLE model_calls (
  call_id TEXT PRIMARY KEY,
  occurred_at_ms INTEGER NOT NULL,
  model TEXT NOT NULL,
  role TEXT NOT NULL,
  status TEXT NOT NULL,
  duration_ms INTEGER NOT NULL,
  ttft_ms INTEGER,
  output_rate REAL,
  retried INTEGER NOT NULL
) STRICT;
CREATE INDEX model_calls_at ON model_calls(occurred_at_ms);
`

func validateAnalytics(record *client.UsageRecord) error {
	attribution := &record.Attribution
	attribution.ProjectID = boundedLabel(attribution.ProjectID)
	attribution.ProjectName = boundedLabel(attribution.ProjectName)
	attribution.Subagent = boundedLabel(attribution.Subagent)
	if attribution.ProjectID == "" {
		attribution.ProjectName = ""
	}
	performance := record.Performance
	if performance == nil {
		return nil
	}
	switch performance.Status {
	case "completed", "failed", "cancelled", "closed":
	default:
		return errors.New("usage: invalid model call status")
	}
	if performance.DurationMS < 0 || (performance.TTFTMS != nil && *performance.TTFTMS < 0) {
		return errors.New("usage: call timing must not be negative")
	}
	if rate := performance.OutputTokensPerSec; rate != nil && (*rate < 0 || math.IsNaN(*rate) || math.IsInf(*rate, 0)) {
		return errors.New("usage: invalid output rate")
	}
	return nil
}

func boundedLabel(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		return ""
	}
	return value
}

func appendAttribution(ctx context.Context, tx recordTransaction, record client.UsageRecord) error {
	// The Gateway may have already recorded the tokens. Attach Q's local
	// metadata to that same event without changing its counts or rollup.
	_, err := tx.ExecContext(ctx, `
INSERT INTO usage_attribution(event_id, project_id, project_name, subagent)
VALUES(?, ?, ?, ?) ON CONFLICT(event_id) DO NOTHING`,
		record.EventID, record.Attribution.ProjectID, record.Attribution.ProjectName, record.Attribution.Subagent)
	return err
}

func appendPerformance(ctx context.Context, tx recordTransaction, record client.UsageRecord) (bool, error) {
	performance := record.Performance
	result, err := tx.ExecContext(ctx, `
INSERT INTO model_calls(call_id, occurred_at_ms, model, role, status, duration_ms, ttft_ms, output_rate, retried)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(call_id) DO NOTHING`,
		record.EventID, record.At.UnixMilli(), record.Model, record.Role, performance.Status,
		performance.DurationMS, performance.TTFTMS, performance.OutputTokensPerSec, boolInt(performance.Retried))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) queryAnalytics(ctx context.Context, filter Filter, view *UsageView) error {
	where, args := eventWhere(filter)
	projects, err := s.queryAttribution(ctx, "project_id, MAX(project_name)", "project_id", where, args)
	if err != nil {
		return err
	}
	subagents, err := s.queryAttribution(ctx, "subagent, subagent", "subagent", where, args)
	if err != nil {
		return err
	}
	performance, err := s.queryPerformance(ctx, where, args)
	if err != nil {
		return err
	}
	view.Projects, view.Subagents, view.Performance = projects, subagents, performance
	return nil
}

func (s *Store) queryAttribution(ctx context.Context, labels, group, where string, args []any) ([]DimensionTotal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+labels+", "+totalsSQL+`
FROM usage_attribution JOIN usage_events USING(event_id)
WHERE `+where+" AND "+group+" <> '' GROUP BY "+group+" ORDER BY SUM(total_tokens) DESC, 1", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []DimensionTotal
	for rows.Next() {
		var value DimensionTotal
		if err := rows.Scan(&value.ID, &value.Name, &value.Calls, &value.PromptTokens, &value.CompletionTokens,
			&value.TotalTokens, &value.CachedTokens, &value.CacheWriteTokens, &value.EstimatedCalls,
			&value.CacheEstimatedCalls, &value.CacheHitCalls, &value.CacheHitSamples); err != nil {
			return nil, err
		}
		if value.Name == "" {
			value.Name = value.ID
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

type modelSamples struct {
	metrics                           ModelPerformance
	duration, firstOutput, outputRate []float64
}

func (s *Store) queryPerformance(ctx context.Context, where string, args []any) ([]ModelPerformance, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT model, status, duration_ms, ttft_ms, output_rate, retried FROM model_calls WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := make(map[string]*modelSamples)
	for rows.Next() {
		var name, status string
		var duration int64
		var ttft, rate sql.NullFloat64
		var retried int64
		if err := rows.Scan(&name, &status, &duration, &ttft, &rate, &retried); err != nil {
			return nil, err
		}
		samples := models[name]
		if samples == nil {
			samples = &modelSamples{metrics: ModelPerformance{Name: name}}
			models[name] = samples
		}
		switch status {
		case "completed":
			samples.metrics.Completed++
			samples.duration = append(samples.duration, float64(duration))
			if ttft.Valid {
				samples.firstOutput = append(samples.firstOutput, ttft.Float64)
			}
			if rate.Valid {
				samples.outputRate = append(samples.outputRate, rate.Float64)
			}
		case "failed":
			samples.metrics.Failed++
		default:
			samples.metrics.Stopped++
			continue
		}
		samples.metrics.Retried += retried
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	values := make([]ModelPerformance, 0, len(models))
	for _, samples := range models {
		slices.Sort(samples.duration)
		slices.Sort(samples.firstOutput)
		slices.Sort(samples.outputRate)
		metrics := samples.metrics
		metrics.DurationP50MS, metrics.DurationP95MS = percentile(samples.duration, .50), percentile(samples.duration, .95)
		metrics.TTFTP50MS, metrics.TTFTP95MS = percentile(samples.firstOutput, .50), percentile(samples.firstOutput, .95)
		metrics.OutputRateP50 = percentile(samples.outputRate, .50)
		values = append(values, metrics)
	}
	sort.Slice(values, func(i, j int) bool {
		left, right := values[i], values[j]
		if left.Completed+left.Failed != right.Completed+right.Failed {
			return left.Completed+left.Failed > right.Completed+right.Failed
		}
		return left.Name < right.Name
	})
	return values, nil
}

func percentile(sorted []float64, fraction float64) *float64 {
	if len(sorted) == 0 {
		return nil
	}
	position := float64(len(sorted)-1) * fraction
	lower, upper := int(math.Floor(position)), int(math.Ceil(position))
	value := sorted[lower] + (sorted[upper]-sorted[lower])*(position-float64(lower))
	return &value
}
