package usagelog

import (
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
)

func TestStoreAnalyticsMigratesCacheHitsAndJoinsAttribution(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	clock := func() time.Time { return now }
	store, err := openStore(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	at := now.Add(-48 * time.Hour)
	old := usageRecord(strings.Repeat("a", 32), at.Add(-100*24*time.Hour), "provider/model", "reviewer", 100)
	hit := usageRecord(strings.Repeat("b", 32), at, "provider/model", "reviewer", 100)
	for _, record := range []client.UsageRecord{old, hit} {
		if inserted, err := store.Append(t.Context(), record); !inserted || err != nil {
			t.Fatalf("append legacy record: inserted=%v err=%v", inserted, err)
		}
	}
	if _, err := store.ArchiveExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
DROP TABLE usage_attribution;
DROP TABLE model_calls;
ALTER TABLE usage_daily DROP COLUMN cache_hit_calls;
ALTER TABLE usage_daily DROP COLUMN cache_hit_samples;
PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = openStore(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	hit.Attribution = client.UsageAttribution{ProjectID: "project-1", ProjectName: "A project", Subagent: "builtin/reviewer"}
	if inserted, err := store.Append(t.Context(), hit); inserted || err != nil {
		t.Fatalf("Gateway record should gain attribution without another charge: inserted=%v err=%v", inserted, err)
	}
	miss := usageRecord(strings.Repeat("c", 32), at, hit.Model, hit.Role, 40)
	miss.CachedTokens, miss.Attribution = 0, hit.Attribution
	unknown := usageRecord(strings.Repeat("d", 32), at, hit.Model, hit.Role, 20)
	unknown.CachedTokens, unknown.CacheEstimated = 0, true
	other := usageRecord(strings.Repeat("e", 32), at, hit.Model, "main", 300)
	for _, record := range []client.UsageRecord{miss, unknown, other} {
		if inserted, err := store.Append(t.Context(), record); !inserted || err != nil {
			t.Fatalf("append event: inserted=%v err=%v", inserted, err)
		}
	}
	for index, duration := range []int64{100, 200, 300, 400} {
		metric := client.UsagePerformance{Status: "completed", DurationMS: duration}
		if index < 2 {
			ttft, rate := duration/10, float64(duration)/10
			metric.TTFTMS, metric.OutputTokensPerSec = &ttft, &rate
		}
		if index == 2 {
			metric.Status, metric.Retried = "failed", true
		}
		if index == 3 {
			metric.Status = "cancelled"
		}
		call := client.UsageRecord{EventID: strings.Repeat(string(rune('1'+index)), 32), At: at, Model: hit.Model, Role: hit.Role, Performance: &metric}
		for _, want := range []bool{true, false} {
			if inserted, err := store.Append(t.Context(), call); inserted != want || err != nil {
				t.Fatalf("append outcome: inserted=%v err=%v", inserted, err)
			}
		}
	}
	view, err := store.Query(t.Context(), Filter{From: now.Add(-7 * 24 * time.Hour), To: now, Model: hit.Model, Role: hit.Role, Details: true})
	if err != nil {
		t.Fatal(err)
	}
	if view.Totals.Calls != 3 || view.Totals.TotalTokens != 160 || view.Totals.CacheHitCalls != 1 || view.Totals.CacheHitSamples != 2 {
		t.Fatalf("filtered totals = %#v", view.Totals)
	}
	if len(view.Projects) != 1 || view.Projects[0].ID != "project-1" || view.Projects[0].Name != "A project" || view.Projects[0].TotalTokens != 140 ||
		len(view.Subagents) != 1 || view.Subagents[0].Name != "builtin/reviewer" || view.Subagents[0].Calls != 2 {
		t.Fatalf("project/subagent totals = %#v / %#v", view.Projects, view.Subagents)
	}
	if len(view.Performance) != 1 {
		t.Fatalf("performance = %#v", view.Performance)
	}
	metric := view.Performance[0]
	if metric.Completed != 2 || metric.Failed != 1 || metric.Stopped != 1 || metric.Retried != 1 ||
		*metric.DurationP50MS != 150 || *metric.DurationP95MS != 195 || *metric.TTFTP50MS != 15 || *metric.OutputRateP50 != 15 {
		t.Fatalf("model performance = %#v", metric)
	}
	view, err = store.Query(t.Context(), Filter{From: floorUTCDay(old.At), To: now, Model: hit.Model, Role: hit.Role, Details: true})
	if err != nil || view.Totals.Calls != 4 || view.Totals.CacheHitSamples != 2 {
		t.Fatalf("archived legacy rows must not count as measured misses: totals = %#v, err = %v", view.Totals, err)
	}
	view, err = store.Query(t.Context(), Filter{From: old.At.Add(-time.Hour), To: old.At, Model: hit.Model, Role: hit.Role})
	if err != nil || view.Totals.Calls != 0 {
		t.Fatalf("archived event on the exclusive boundary: totals = %#v, err = %v", view.Totals, err)
	}
	view, err = store.Query(t.Context(), Filter{From: old.At, To: old.At.Add(time.Hour), Model: hit.Model, Role: hit.Role, Details: true})
	if err != nil || view.Totals.Calls != 1 || view.Totals.CacheHitSamples != 1 {
		t.Fatalf("archived event on the inclusive boundary: totals = %#v, err = %v", view.Totals, err)
	}
	view, err = store.Query(t.Context(), Filter{From: now.Add(-7 * 24 * time.Hour), To: now, Role: "main", Details: true})
	if err != nil || view.Totals.Calls != 1 || len(view.Performance)+len(view.Projects)+len(view.Subagents) != 0 {
		t.Fatalf("role filter did not scope analytics: view = %#v, err = %v", view, err)
	}
}
