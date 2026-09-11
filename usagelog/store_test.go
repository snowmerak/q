package usagelog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/snowmerak/q/client"
)

func usageRecord(id string, at time.Time, model, role string, total int) client.UsageRecord {
	return client.UsageRecord{
		EventID: id, At: at, Model: model, Role: role,
		PromptTokens: total - 10, CompletionTokens: 10, TotalTokens: total,
		CachedTokens: total / 2, CacheWriteTokens: 3,
	}
}

func TestStoreAppendIsIdempotentAndUpdatesRollups(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	store, err := openStore(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := usageRecord(strings.Repeat("a", 32), now.Add(-time.Hour), "codex/gpt", "planner", 160_000)
	for index, want := range []bool{true, false} {
		inserted, err := store.Append(t.Context(), record)
		if err != nil || inserted != want {
			t.Fatalf("append %d: inserted=%v err=%v", index, inserted, err)
		}
	}
	view, err := store.Query(t.Context(), Filter{From: now.Add(-24 * time.Hour), To: now})
	if err != nil {
		t.Fatal(err)
	}
	if view.Totals.Calls != 1 || view.Totals.TotalTokens != 160_000 || len(view.Models) != 1 || view.Models[0].Name != "codex/gpt" || len(view.Roles) != 1 || view.Roles[0].Name != "planner" {
		t.Fatalf("view = %#v", view)
	}
	if info, err := os.Stat(store.DBPath()); err != nil || info.Size() > 256<<10 {
		t.Fatalf("one 160K-token call should remain one compact row: info=%v err=%v", info, err)
	}
}

func TestStoreConcurrentAppend(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	store, err := openStore(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const count = 48
	var group sync.WaitGroup
	errorsByCall := make(chan error, count)
	for index := range count {
		group.Add(1)
		go func() {
			defer group.Done()
			id := strings.Repeat(string(rune('a'+index%6)), 31) + string("0123456789abcdef"[index%16])
			_, err := store.Append(context.Background(), usageRecord(id, now, "model", "coder", index+10))
			errorsByCall <- err
		}()
	}
	group.Wait()
	close(errorsByCall)
	for err := range errorsByCall {
		if err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&rows); err != nil || rows != count {
		t.Fatalf("rows=%d err=%v", rows, err)
	}
}

func TestHotDailyQueryKeepsExactTimestampBoundaries(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	store, err := openStore(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	from := now.Add(-7 * 24 * time.Hour)
	outside := usageRecord(strings.Repeat("a", 32), from.Add(-time.Hour), "model", "main", 100)
	inside := usageRecord(strings.Repeat("b", 32), from.Add(time.Hour), "model", "main", 200)
	for _, record := range []client.UsageRecord{outside, inside} {
		if _, err := store.Append(t.Context(), record); err != nil {
			t.Fatal(err)
		}
	}
	view, err := store.Query(t.Context(), Filter{From: from, To: now})
	if err != nil {
		t.Fatal(err)
	}
	if view.Resolution != "day" || view.Totals.Calls != 1 || view.Totals.TotalTokens != 200 {
		t.Fatalf("view=%#v", view)
	}
}

func TestLegacyImportSkipsCorruptRowsAndReplaysIdempotently(t *testing.T) {
	dir := t.TempDir()
	legacyDir := filepath.Join(dir, "logs", "model-usage")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(legacyDir, "usage-20260911-old.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	valid := client.UsageRecord{At: time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC), Model: "legacy", TotalTokens: 42}
	_ = json.NewEncoder(file).Encode(valid)
	_, _ = file.WriteString("{broken\n")
	_ = file.Close()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	store, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var events, imports, invalid int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&events)
	_ = store.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(invalid_rows),0) FROM usage_imports`).Scan(&imports, &invalid)
	if events != 1 || imports != 1 || invalid != 1 {
		t.Fatalf("events=%d imports=%d invalid=%d", events, imports, invalid)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("legacy source was removed: %v", err)
	}
}

func TestArchivePreservesRollupAndMergesLateRows(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	store, err := openStore(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	day := time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)
	for index := range 3 {
		id := strings.Repeat(string(rune('a'+index)), 32)
		if _, err := store.Append(t.Context(), usageRecord(id, day.Add(time.Duration(index)*time.Hour), "model", "scout", 100+index)); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := store.ArchiveBefore(t.Context(), now.Add(-HotRetention)); err != nil || count != 1 {
		t.Fatalf("archive count=%d err=%v", count, err)
	}
	path := store.archivePath("2026-01-02")
	rows, err := parquet.ReadFile[archiveRow](path)
	if err != nil || len(rows) != 3 {
		t.Fatalf("archive rows=%d err=%v", len(rows), err)
	}
	var raw, rollupCalls int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&raw)
	_ = store.db.QueryRow(`SELECT calls FROM usage_daily WHERE day='2026-01-02'`).Scan(&rollupCalls)
	if raw != 0 || rollupCalls != 3 {
		t.Fatalf("raw=%d rollup=%d", raw, rollupCalls)
	}
	late := usageRecord(strings.Repeat("f", 32), day.Add(4*time.Hour), "model", "scout", 110)
	if _, err := store.Append(t.Context(), late); err != nil {
		t.Fatal(err)
	}
	if count, err := store.ArchiveBefore(t.Context(), now.Add(-HotRetention)); err != nil || count != 1 {
		t.Fatalf("late archive count=%d err=%v", count, err)
	}
	rows, err = parquet.ReadFile[archiveRow](path)
	if err != nil || len(rows) != 4 {
		t.Fatalf("merged archive rows=%d err=%v", len(rows), err)
	}
	manifest, err := store.archiveManifest(t.Context(), "2026-01-02")
	if err != nil || manifest.Rows != 4 || manifest.TotalTokens != 100+101+102+110 {
		t.Fatalf("manifest=%#v err=%v", manifest, err)
	}
	_ = store.db.QueryRow(`SELECT calls FROM usage_daily WHERE day='2026-01-02'`).Scan(&rollupCalls)
	if rollupCalls != 4 {
		t.Fatalf("rollup calls=%d", rollupCalls)
	}
}

func TestArchiveDoesNotReplaceAFileMissingBehindItsManifest(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	store, err := openStore(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	day := time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)
	if _, err := store.Append(t.Context(), usageRecord(strings.Repeat("a", 32), day, "model", "scout", 100)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ArchiveBefore(t.Context(), now.Add(-HotRetention)); err != nil {
		t.Fatal(err)
	}
	path := store.archivePath("2026-01-02")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(t.Context(), usageRecord(strings.Repeat("b", 32), day.Add(time.Hour), "model", "scout", 110)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ArchiveBefore(t.Context(), now.Add(-HotRetention)); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("archive mismatch error = %v", err)
	}
	var raw int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&raw); err != nil || raw != 1 {
		t.Fatalf("raw rows=%d err=%v", raw, err)
	}
}

func TestHTTPBoundaryAndDashboard(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	testServer := httptest.NewUnstartedServer(nil)
	allowedHost := testServer.Listener.Addr().String()
	testServer.Config.Handler = newHTTPHandler(store, Health{Service: ServiceName, ProtocolVersion: ProtocolVersion, Ready: true}, allowedHost)
	testServer.Start()
	defer testServer.Close()
	response, err := http.Get(testServer.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "Token traffic, without the payload") {
		t.Fatalf("dashboard status=%d body=%q", response.StatusCode, body)
	}
	record := usageRecord(strings.Repeat("a", 32), time.Now().UTC(), "model", "main", 10)
	encoded, _ := json.Marshal(record)
	request, _ := http.NewRequest(http.MethodPost, testServer.URL+"/v1/events", strings.NewReader(string(encoded)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://attacker.example")
	response, err = http.DefaultClient.Do(request)
	if err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status=%v err=%v", response.StatusCode, err)
	}
	_ = response.Body.Close()
	request, _ = http.NewRequest(http.MethodPost, testServer.URL+"/v1/events", strings.NewReader(string(append(encoded, []byte(` {}`)...))))
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("trailing JSON status=%v err=%v", response.StatusCode, err)
	}
	_ = response.Body.Close()
	request, _ = http.NewRequest(http.MethodGet, testServer.URL+"/v1/health", nil)
	request.Host = "evil.example"
	response, err = http.DefaultClient.Do(request)
	if err != nil || response.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("Host boundary status=%v err=%v", response.StatusCode, err)
	}
	_ = response.Body.Close()
	request, _ = http.NewRequest(http.MethodPost, testServer.URL+"/v1/events", strings.NewReader(strings.Repeat("x", maximumEventBody+1)))
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized body status=%v err=%v", response.StatusCode, err)
	}
	_ = response.Body.Close()
	response, err = http.Get(testServer.URL + "/api/v1/usage?surprise=true")
	if err != nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown query status=%v err=%v", response.StatusCode, err)
	}
	_ = response.Body.Close()
}

func TestConfigRejectsNonLoopbackHost(t *testing.T) {
	for _, value := range []Config{
		{Version: ConfigVersion, Host: "0.0.0.0", Port: 17893},
		{Version: ConfigVersion, Host: "example.com", Port: 17893},
		{Version: ConfigVersion, Host: "127.0.0.1", ProbeHost: "192.0.2.1", Port: 17893},
	} {
		if err := value.Validate(); err == nil {
			t.Fatalf("non-loopback config unexpectedly passed: %#v", value)
		}
	}
}

func TestEnsureRejectsIncompatibleService(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/health" {
			http.NotFound(writer, request)
			return
		}
		writeJSON(writer, http.StatusOK, Health{Service: "not-q-usage", ProtocolVersion: ProtocolVersion, Ready: true})
	}))
	defer testServer.Close()
	parsed, _ := url.Parse(testServer.URL)
	_, portText, _ := net.SplitHostPort(parsed.Host)
	port, _ := strconv.Atoi(portText)
	_, err := EnsureWithOptions(t.Context(), EnsureOptions{
		Dir: t.TempDir(), Config: Config{Version: ConfigVersion, Host: "127.0.0.1", Port: port},
	})
	if err == nil || !strings.Contains(err.Error(), "incompatible service") {
		t.Fatalf("incompatible service error = %v", err)
	}
}

func TestEnsureElectsOneLeaderAndFollowerSharesStore(t *testing.T) {
	dir := t.TempDir()
	config := availableConfig(t)
	if err := (ConfigStore{Dir: dir}).Save(config); err != nil {
		t.Fatal(err)
	}
	first, err := EnsureWithOptions(t.Context(), EnsureOptions{Dir: dir, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := EnsureWithOptions(t.Context(), EnsureOptions{Dir: dir, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if !first.IsLeader() || second.IsLeader() {
		t.Fatalf("leader states first=%v second=%v", first.IsLeader(), second.IsLeader())
	}
	record := usageRecord(strings.Repeat("b", 32), time.Now().UTC(), "shared", "planner", 50)
	if err := second.Client().Append(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	view, err := first.Client().Query(t.Context(), Filter{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
	if err != nil || view.Totals.Calls != 1 {
		t.Fatalf("view=%#v err=%v", view, err)
	}
}

func TestRunTakesOverWhenLeaderStops(t *testing.T) {
	dir := t.TempDir()
	config := availableConfig(t)
	if err := (ConfigStore{Dir: dir}).Save(config); err != nil {
		t.Fatal(err)
	}
	leader, err := EnsureWithOptions(t.Context(), EnsureOptions{Dir: dir, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	firstHealth, err := leader.Client().Health(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, dir, io.Discard) }()
	time.Sleep(100 * time.Millisecond)
	if err := leader.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	client := NewClient(config.Endpoint(), 200*time.Millisecond)
	for {
		health, healthErr := client.Health(t.Context())
		if healthErr == nil && health.Compatible() && health.Generation != firstHealth.Generation {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("follower did not take over: health=%#v err=%v", health, healthErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("usage service did not stop after cancellation")
	}
}

func TestRecorderWritesThroughService(t *testing.T) {
	dir := t.TempDir()
	config := availableConfig(t)
	if err := (ConfigStore{Dir: dir}).Save(config); err != nil {
		t.Fatal(err)
	}
	recorder := New(dir)
	defer recorder.Close()
	if err := recorder.RecordUsage(client.UsageRecord{Model: "local", Role: "coder", TotalTokens: 12}); err != nil {
		t.Fatal(err)
	}
	view, err := recorder.runtime.Client().Query(t.Context(), Filter{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
	if err != nil || view.Totals.Calls != 1 || view.Models[0].Name != "local" {
		t.Fatalf("view=%#v err=%v", view, err)
	}
}

func TestRecorderRetryReusesEventIDAndDeadlineIsBounded(t *testing.T) {
	var mu sync.Mutex
	var ids []string
	events := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/health":
			writeJSON(writer, http.StatusOK, Health{Service: ServiceName, ProtocolVersion: ProtocolVersion, Ready: true})
		case "/v1/events":
			var record client.UsageRecord
			_ = json.NewDecoder(request.Body).Decode(&record)
			mu.Lock()
			ids = append(ids, record.EventID)
			events++
			current := events
			mu.Unlock()
			if current == 1 {
				writeProblem(writer, http.StatusInternalServerError, "lost", "retry")
				return
			}
			if current >= 3 {
				<-request.Context().Done()
				return
			}
			writeJSON(writer, http.StatusOK, map[string]bool{"inserted": true})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	_, portText, _ := net.SplitHostPort(parsed.Host)
	port, _ := strconv.Atoi(portText)
	dir := t.TempDir()
	if err := (ConfigStore{Dir: dir}).Save(Config{Version: ConfigVersion, Host: "127.0.0.1", Port: port}); err != nil {
		t.Fatal(err)
	}
	recorder := New(dir)
	defer recorder.Close()
	if err := recorder.RecordUsage(client.UsageRecord{Model: "model", TotalTokens: 1}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("retry IDs = %#v", ids)
	}
	mu.Unlock()
	started := time.Now()
	if err := recorder.RecordUsage(client.UsageRecord{Model: "model", TotalTokens: 1}); err == nil {
		t.Fatal("slow record unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("recording exceeded bounded deadline: %s", elapsed)
	}
}

func TestRecorderDeadlineDoesNotDependOnCooperativeStartup(t *testing.T) {
	release := make(chan struct{})
	recorder := New(t.TempDir())
	recorder.ensure = func(context.Context, EnsureOptions) (*Runtime, error) {
		<-release
		return nil, errors.New("delayed startup")
	}
	started := time.Now()
	if err := recorder.RecordUsage(client.UsageRecord{Model: "model", TotalTokens: 1}); err == nil {
		t.Fatal("uncooperative startup unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("uncooperative startup exceeded bounded deadline: %s", elapsed)
	}
	close(release)
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
}

func availableConfig(t *testing.T) Config {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return Config{Version: ConfigVersion, Host: "127.0.0.1", Port: port}
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	var body strings.Builder
	for scanner.Scan() {
		body.WriteString(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return body.String()
}

func BenchmarkSQLiteUsageAggregate(b *testing.B) {
	dir := b.TempDir()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	store, err := openStore(dir, func() time.Time { return now })
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	tx, err := store.db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for index := range 187_000 {
		record := usageRecord(strings.Repeat("a", 16)+hexIndex(index), now.Add(-time.Duration(index%2160)*time.Minute), "codex/gpt", "main", 33_000+index%1000)
		if _, err := appendRecord(context.Background(), tx, record); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		b.Fatal(err)
	}
	info, err := os.Stat(store.DBPath())
	if err != nil {
		b.Fatal(err)
	}
	bytesPerRow := float64(info.Size()) / 187_000
	benchmarks := []struct {
		name   string
		filter Filter
	}{
		{name: "hot-exact", filter: Filter{From: now.Add(-90 * 24 * time.Hour), To: now.Add(time.Hour)}},
		{name: "all-time-rollup", filter: Filter{From: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), To: now.Add(time.Hour)}},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportMetric(bytesPerRow, "db-B/row")
			for range b.N {
				if _, err := store.Query(context.Background(), benchmark.filter); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func hexIndex(value int) string { return strings.ToLower(fmt.Sprintf("%016x", value)) }
