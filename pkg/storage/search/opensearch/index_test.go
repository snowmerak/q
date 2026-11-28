package opensearch_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/contracttest"
	"github.com/snowmerak/q/v2/pkg/storage/search/opensearch"
)

type mockDoc struct {
	ID         string    `json:"id"`
	EntityType string    `json:"entity_type"`
	Scope      string    `json:"scope"`
	OwnerID    string    `json:"owner_id"`
	ObservedBy []string  `json:"observed_by"`
	Text       string    `json:"text"`
	OccurredAt int64     `json:"occurred_at"`
	Revision   int64     `json:"revision"`
	Vector     []float32 `json:"vector,omitempty"`
}

type mockOpenSearchServer struct {
	srv         *httptest.Server
	mu          sync.Mutex
	indices     map[string]map[string]mockDoc
	aliases     map[string][]string
	deleted     []string
	swapMode    string // "", "commit_then_drop", "delayed_commit_after_recheck", "fail_400"
	pendingSwap []byte
}

func newMockOpenSearchServer() *httptest.Server {
	return newControllableMockOpenSearchServer().srv
}

func applyMockAliasActions(aliases map[string][]string, bodyBytes []byte) {
	var req struct {
		Actions []map[string]struct {
			Index string `json:"index"`
			Alias string `json:"alias"`
		} `json:"actions"`
	}
	_ = json.Unmarshal(bodyBytes, &req)
	for _, act := range req.Actions {
		if rem, ok := act["remove"]; ok {
			cur := aliases[rem.Alias]
			next := cur[:0]
			for _, x := range cur {
				if x != rem.Index {
					next = append(next, x)
				}
			}
			aliases[rem.Alias] = next
		}
		if add, ok := act["add"]; ok {
			aliases[add.Alias] = append(aliases[add.Alias], add.Index)
		}
	}
}

func newControllableMockOpenSearchServer() *mockOpenSearchServer {
	m := &mockOpenSearchServer{
		indices: make(map[string]map[string]mockDoc),
		aliases: make(map[string][]string),
	}

	resolveTarget := func(name string) (string, bool) {
		if _, ok := m.indices[name]; ok {
			return name, true
		}
		if list := m.aliases[name]; len(list) > 0 {
			return list[len(list)-1], true
		}
		return "", false
	}

	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		bodyBytes, _ := io.ReadAll(r.Body)
		m.mu.Lock()

		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/_alias/"):
			aliasName, _ := url.PathUnescape(strings.TrimPrefix(path, "/_alias/"))
			targets := slices.Clone(m.aliases[aliasName])
			if len(m.pendingSwap) > 0 {
				// Simulate cluster state applying an in-flight alias swap right as GET /_alias returns the old view.
				applyMockAliasActions(m.aliases, m.pendingSwap)
				m.pendingSwap = nil
			}
			if len(targets) == 0 {
				m.mu.Unlock()
				w.WriteHeader(http.StatusNotFound)
				return
			}
			resp := make(map[string]any, len(targets))
			for _, idx := range targets {
				resp[idx] = map[string]any{"aliases": map[string]any{aliasName: map[string]any{}}}
			}
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(resp)

		case path == "/_aliases" && r.Method == http.MethodPost:
			mode := m.swapMode
			if mode == "fail_400" {
				m.swapMode = ""
				m.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"rejected alias swap"}`)
				return
			}
			if mode == "delayed_commit_after_recheck" {
				m.swapMode = ""
				m.pendingSwap = slices.Clone(bodyBytes)
				m.mu.Unlock()
				if hj, ok := w.(http.Hijacker); ok {
					if conn, _, err := hj.Hijack(); err == nil {
						_ = conn.Close()
						return
					}
				}
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			applyMockAliasActions(m.aliases, bodyBytes)
			if mode == "commit_then_drop" {
				m.swapMode = ""
				m.mu.Unlock()
				if hj, ok := w.(http.Hijacker); ok {
					if conn, _, err := hj.Hijack(); err == nil {
						_ = conn.Close()
						return
					}
				}
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"acknowledged": true})

		case path == "/_bulk" && r.Method == http.MethodPost:
			sc := bufio.NewScanner(strings.NewReader(string(bodyBytes)))
			for sc.Scan() {
				line1 := strings.TrimSpace(sc.Text())
				if line1 == "" {
					continue
				}
				if !sc.Scan() {
					break
				}
				var meta struct {
					Index struct {
						Index string `json:"_index"`
						ID    string `json:"_id"`
					} `json:"index"`
				}
				_ = json.Unmarshal([]byte(line1), &meta)
				var doc mockDoc
				_ = json.Unmarshal(sc.Bytes(), &doc)
				target, ok := resolveTarget(meta.Index.Index)
				if !ok {
					m.mu.Unlock()
					w.WriteHeader(http.StatusNotFound)
					return
				}
				m.indices[target][meta.Index.ID] = doc
			}
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": false})

		case strings.HasSuffix(path, "/_count"):
			name := strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/_count")
			name, _ = url.PathUnescape(name)
			target, ok := resolveTarget(name)
			if !ok {
				m.mu.Unlock()
				w.WriteHeader(http.StatusNotFound)
				return
			}
			docs := m.indices[target]
			if strings.Contains(string(bodyBytes), `"vector"`) {
				vc := 0
				for _, d := range docs {
					if len(d.Vector) > 0 {
						vc++
					}
				}
				m.mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"count": vc})
				return
			}
			count := len(docs)
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"count": count})

		case strings.HasSuffix(path, "/_search"):
			name := strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/_search")
			name, _ = url.PathUnescape(name)
			target, ok := resolveTarget(name)
			if !ok {
				m.mu.Unlock()
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var q map[string]any
			_ = json.Unmarshal(bodyBytes, &q)
			hits := evalMockSearch(m.indices[target], q)
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"hits": map[string]any{"hits": hits},
			})

		case strings.Contains(path, "/_doc/") && r.Method == http.MethodDelete:
			parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/_doc/", 2)
			name, _ := url.PathUnescape(parts[0])
			docID, _ := url.PathUnescape(parts[1])
			target, ok := resolveTarget(name)
			if !ok {
				m.mu.Unlock()
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if _, exists := m.indices[target][docID]; !exists {
				m.mu.Unlock()
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(m.indices[target], docID)
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "deleted"})

		case strings.HasSuffix(path, "/_refresh"):
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"_shards": map[string]any{"failed": 0}})

		case r.Method == http.MethodPut:
			name, _ := url.PathUnescape(strings.TrimPrefix(path, "/"))
			if _, exists := m.indices[name]; !exists {
				m.indices[name] = make(map[string]mockDoc)
			}
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"acknowledged": true})

		case r.Method == http.MethodDelete:
			name, _ := url.PathUnescape(strings.TrimPrefix(path, "/"))
			delete(m.indices, name)
			m.deleted = append(m.deleted, name)
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"acknowledged": true})

		default:
			m.mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return m
}

func evalMockSearch(docs map[string]mockDoc, body map[string]any) []map[string]any {
	raw, _ := json.Marshal(body)
	var filterObserver, textQuery string
	var filters struct {
		Query struct {
			Bool struct {
				Must   []map[string]any `json:"must"`
				Filter []map[string]any `json:"filter"`
			} `json:"bool"`
			KNN struct {
				Vector struct {
					Vector []float32 `json:"vector"`
					Filter struct {
						Bool struct {
							Filter []map[string]any `json:"filter"`
						} `json:"bool"`
					} `json:"filter"`
				} `json:"vector"`
			} `json:"knn"`
		} `json:"query"`
	}
	_ = json.Unmarshal(raw, &filters)

	applyFilterClauses := func(clauses []map[string]any) {
		for _, cl := range clauses {
			if t, ok := cl["term"].(map[string]any); ok {
				if v, ok := t["observed_by"].(string); ok {
					filterObserver = v
				}
			}
		}
	}
	applyFilterClauses(filters.Query.Bool.Filter)
	applyFilterClauses(filters.Query.KNN.Vector.Filter.Bool.Filter)
	for _, m := range filters.Query.Bool.Must {
		if match, ok := m["match"].(map[string]any); ok {
			if txt, ok := match["text"].(string); ok {
				textQuery = strings.ToLower(txt)
			}
		}
	}

	var out []map[string]any
	for _, d := range docs {
		if filterObserver != "" && !slices.Contains(d.ObservedBy, filterObserver) {
			continue
		}
		score := 1.0
		if textQuery != "" {
			if !strings.Contains(strings.ToLower(d.Text), textQuery) {
				continue
			}
		}
		if qv := filters.Query.KNN.Vector.Vector; len(qv) > 0 {
			if len(d.Vector) != len(qv) {
				continue
			}
			score = cosine(qv, d.Vector)
		}
		out = append(out, map[string]any{
			"_score":  score,
			"_source": d,
		})
	}
	_ = math.NaN()
	return out
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func TestOpenSearchContractOfflineMock(t *testing.T) {
	srv := newMockOpenSearchServer()
	t.Cleanup(srv.Close)

	contracttest.RunSearchContractTests(t, func(t *testing.T) storage.SearchIndex {
		t.Helper()
		idx, err := opensearch.Open(t.Context(), opensearch.Config{
			Endpoint:    srv.URL,
			IndexPrefix: "q_mock_" + strings.ReplaceAll(domain.NewID()[:8], "-", ""),
		})
		if err != nil {
			t.Fatalf("opensearch.Open: %v", err)
		}
		return idx
	})
}

func TestResetAndRebuildSwapErrorHandling(t *testing.T) {
	t.Run("lost_response_after_commit", func(t *testing.T) {
		mock := newControllableMockOpenSearchServer()
		t.Cleanup(mock.srv.Close)

		idx, err := opensearch.Open(t.Context(), opensearch.Config{
			Endpoint:    mock.srv.URL,
			IndexPrefix: "q_swap_drop",
		})
		if err != nil {
			t.Fatalf("opensearch.Open: %v", err)
		}
		if err := idx.Upsert(t.Context(), storage.SearchDocument{
			ID: "old-1", EntityType: "memory", Scope: "room:r1", Text: "legacy lantern", ObservedBy: []string{"alice"},
		}); err != nil {
			t.Fatal(err)
		}

		mock.mu.Lock()
		mock.swapMode = "commit_then_drop"
		mock.mu.Unlock()

		docsCh := make(chan storage.SearchDocument, 1)
		docsCh <- storage.SearchDocument{
			ID: "new-1", EntityType: "memory", Scope: "room:r1", Text: "rebuilt lighthouse", ObservedBy: []string{"alice"},
		}
		close(docsCh)

		if err := idx.ResetAndRebuild(t.Context(), docsCh); err != nil {
			t.Fatalf("expected ResetAndRebuild to succeed when alias swap committed before connection drop, got: %v", err)
		}

		mock.mu.Lock()
		activeTargets := slices.Clone(mock.aliases["q_swap_drop_records"])
		deleted := slices.Clone(mock.deleted)
		_, hasGen2 := mock.indices["q_swap_drop_records_2"]
		_, hasGen1 := mock.indices["q_swap_drop_records_1"]
		mock.mu.Unlock()

		if !slices.Equal(activeTargets, []string{"q_swap_drop_records_2"}) {
			t.Fatalf("expected alias pointing to gen 2, got %v", activeTargets)
		}
		if !hasGen2 {
			t.Fatal("expected new backing index q_swap_drop_records_2 to remain intact")
		}
		if hasGen1 || !slices.Contains(deleted, "q_swap_drop_records_1") {
			t.Fatalf("expected old backing index q_swap_drop_records_1 deleted (deleted=%v)", deleted)
		}

		hits, err := idx.Search(t.Context(), storage.SearchQuery{Mode: "lexical", Text: "rebuilt lighthouse"})
		if err != nil || len(hits) != 1 || hits[0].ID != "new-1" {
			t.Fatalf("expected rebuilt document searchable after recovered swap, hits=%+v err=%v", hits, err)
		}
	})

	t.Run("delayed_commit_after_recheck_does_not_delete_new_index", func(t *testing.T) {
		mock := newControllableMockOpenSearchServer()
		t.Cleanup(mock.srv.Close)

		idx, err := opensearch.Open(t.Context(), opensearch.Config{
			Endpoint:    mock.srv.URL,
			IndexPrefix: "q_swap_delay",
		})
		if err != nil {
			t.Fatalf("opensearch.Open: %v", err)
		}
		if err := idx.Upsert(t.Context(), storage.SearchDocument{
			ID: "old-1", EntityType: "memory", Scope: "room:r1", Text: "old harbor", ObservedBy: []string{"alice"},
		}); err != nil {
			t.Fatal(err)
		}

		mock.mu.Lock()
		mock.swapMode = "delayed_commit_after_recheck"
		mock.mu.Unlock()

		docsCh := make(chan storage.SearchDocument, 1)
		docsCh <- storage.SearchDocument{
			ID: "new-delay-1", EntityType: "memory", Scope: "room:r1", Text: "delayed rebuilt doc", ObservedBy: []string{"alice"},
		}
		close(docsCh)

		if err := idx.ResetAndRebuild(t.Context(), docsCh); err == nil {
			t.Fatal("expected ResetAndRebuild to report error when alias swap response dropped before re-check observed commit")
		}

		mock.mu.Lock()
		activeTargets := slices.Clone(mock.aliases["q_swap_delay_records"])
		deleted := slices.Clone(mock.deleted)
		_, hasGen2 := mock.indices["q_swap_delay_records_2"]
		mock.mu.Unlock()

		if !slices.Equal(activeTargets, []string{"q_swap_delay_records_2"}) {
			t.Fatalf("expected delayed commit alias pointing to gen 2, got %v", activeTargets)
		}
		if !hasGen2 || slices.Contains(deleted, "q_swap_delay_records_2") {
			t.Fatalf("expected newly activated index q_swap_delay_records_2 NOT deleted after TOCTOU re-check (deleted=%v)", deleted)
		}

		hits, err := idx.Search(t.Context(), storage.SearchQuery{Mode: "lexical", Text: "delayed rebuilt doc"})
		if err != nil || len(hits) != 1 || hits[0].ID != "new-delay-1" {
			t.Fatalf("expected delayed-commit rebuilt document searchable via alias, hits=%+v err=%v", hits, err)
		}
	})

	t.Run("failed_swap_cleans_up_staged_index", func(t *testing.T) {
		mock := newControllableMockOpenSearchServer()
		t.Cleanup(mock.srv.Close)

		idx, err := opensearch.Open(t.Context(), opensearch.Config{
			Endpoint:    mock.srv.URL,
			IndexPrefix: "q_swap_fail",
		})
		if err != nil {
			t.Fatalf("opensearch.Open: %v", err)
		}
		if err := idx.Upsert(t.Context(), storage.SearchDocument{
			ID: "orig-1", EntityType: "memory", Scope: "room:r1", Text: "steady anchor", ObservedBy: []string{"alice"},
		}); err != nil {
			t.Fatal(err)
		}

		mock.mu.Lock()
		mock.swapMode = "fail_400"
		mock.mu.Unlock()

		docsCh := make(chan storage.SearchDocument, 1)
		docsCh <- storage.SearchDocument{
			ID: "bad-2", EntityType: "memory", Scope: "room:r1", Text: "unreachable draft", ObservedBy: []string{"alice"},
		}
		close(docsCh)

		if err := idx.ResetAndRebuild(t.Context(), docsCh); err == nil {
			t.Fatal("expected ResetAndRebuild to return error on HTTP 400 swap rejection")
		}

		mock.mu.Lock()
		activeTargets := slices.Clone(mock.aliases["q_swap_fail_records"])
		deleted := slices.Clone(mock.deleted)
		_, hasGen2 := mock.indices["q_swap_fail_records_2"]
		_, hasGen1 := mock.indices["q_swap_fail_records_1"]
		mock.mu.Unlock()

		if !slices.Equal(activeTargets, []string{"q_swap_fail_records_1"}) {
			t.Fatalf("expected original alias target gen 1 preserved, got %v", activeTargets)
		}
		if !hasGen1 {
			t.Fatal("expected original backing index q_swap_fail_records_1 preserved")
		}
		if hasGen2 || !slices.Contains(deleted, "q_swap_fail_records_2") {
			t.Fatalf("expected failed staged index q_swap_fail_records_2 cleaned up (deleted=%v)", deleted)
		}

		hits, err := idx.Search(t.Context(), storage.SearchQuery{Mode: "lexical", Text: "steady anchor"})
		if err != nil || len(hits) != 1 || hits[0].ID != "orig-1" {
			t.Fatalf("expected original document still searchable, hits=%+v err=%v", hits, err)
		}
	})
}

func TestResetAndRebuildBlocksConcurrentWritesUntilSwap(t *testing.T) {
	mock := newControllableMockOpenSearchServer()
	t.Cleanup(mock.srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	var workers sync.WaitGroup
	defer workers.Wait()
	defer cancel()

	idx, err := opensearch.Open(ctx, opensearch.Config{Endpoint: mock.srv.URL, IndexPrefix: "q_rebuild"})
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Upsert(ctx, storage.SearchDocument{ID: "old", EntityType: "memory", Text: "old lantern"}); err != nil {
		t.Fatal(err)
	}

	docsCh := make(chan storage.SearchDocument)
	rebuildDone := make(chan error, 1)
	workers.Go(func() { rebuildDone <- idx.ResetAndRebuild(ctx, docsCh) })
	select {
	case docsCh <- storage.SearchDocument{ID: "rebuilt", EntityType: "memory", Text: "new lantern"}:
		// The rebuild has reached the document stream and cannot swap until it closes.
	case err := <-rebuildDone:
		t.Fatalf("rebuild ended before receiving documents: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	upsertStarted := make(chan struct{})
	upsertDone := make(chan error, 1)
	workers.Go(func() {
		close(upsertStarted)
		upsertDone <- idx.Upsert(ctx, storage.SearchDocument{ID: "concurrent", EntityType: "memory", Text: "new lantern"})
	})
	<-upsertStarted
	query := storage.SearchQuery{Mode: "lexical", Text: "lantern"}
	hits, err := idx.Search(ctx, query)
	if err != nil || len(hits) != 1 || hits[0].ID != "old" {
		t.Fatalf("expected only the old document during rebuild, got %+v err=%v", hits, err)
	}
	select {
	case err := <-upsertDone:
		t.Fatalf("upsert returned before the alias swap: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(docsCh)
	if err := <-rebuildDone; err != nil {
		t.Fatalf("ResetAndRebuild: %v", err)
	}
	if err := <-upsertDone; err != nil {
		t.Fatalf("concurrent Upsert: %v", err)
	}

	mock.mu.Lock()
	targets := slices.Clone(mock.aliases["q_rebuild_records"])
	mock.mu.Unlock()
	if !slices.Equal(targets, []string{"q_rebuild_records_2"}) {
		t.Fatalf("expected alias pointing to gen 2, got %v", targets)
	}
	hits, err = idx.Search(ctx, query)
	if err != nil || len(hits) != 2 {
		t.Fatalf("expected rebuilt and concurrent documents, got %+v err=%v", hits, err)
	}
	for _, id := range []string{"rebuilt", "concurrent"} {
		if !slices.ContainsFunc(hits, func(hit storage.SearchHit) bool { return hit.ID == id }) {
			t.Errorf("missing document %q after swap: %+v", id, hits)
		}
	}
}

func TestOpenSearchContractLiveEnvGated(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("Q_TEST_OPENSEARCH_URL"))
	if endpoint == "" {
		t.Skip("Q_TEST_OPENSEARCH_URL not set")
	}
	contracttest.RunSearchContractTests(t, func(t *testing.T) storage.SearchIndex {
		t.Helper()
		prefix := "q_test_" + strings.ReplaceAll(domain.NewID()[:8], "-", "")
		idx, err := opensearch.Open(t.Context(), opensearch.Config{
			Endpoint:    endpoint,
			IndexPrefix: prefix,
		})
		if err != nil {
			t.Fatalf("opensearch.Open: %v", err)
		}
		t.Cleanup(func() {
			req, _ := http.NewRequest(http.MethodDelete, strings.TrimRight(endpoint, "/")+"/"+prefix+"*", nil)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
		})
		return idx
	})
}
