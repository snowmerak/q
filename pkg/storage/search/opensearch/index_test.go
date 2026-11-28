package opensearch_test

import (
	"bufio"
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

func newMockOpenSearchServer() *httptest.Server {
	var (
		mu      sync.Mutex
		indices = make(map[string]map[string]mockDoc)
		aliases = make(map[string][]string)
	)

	resolveTarget := func(name string) (string, bool) {
		if _, ok := indices[name]; ok {
			return name, true
		}
		if list := aliases[name]; len(list) > 0 {
			return list[len(list)-1], true
		}
		return "", false
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		bodyBytes, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()

		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/_alias/"):
			aliasName, _ := url.PathUnescape(strings.TrimPrefix(path, "/_alias/"))
			targets := aliases[aliasName]
			if len(targets) == 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			resp := make(map[string]any, len(targets))
			for _, idx := range targets {
				resp[idx] = map[string]any{"aliases": map[string]any{aliasName: map[string]any{}}}
			}
			_ = json.NewEncoder(w).Encode(resp)

		case path == "/_aliases" && r.Method == http.MethodPost:
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
					w.WriteHeader(http.StatusNotFound)
					return
				}
				indices[target][meta.Index.ID] = doc
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": false})

		case strings.HasSuffix(path, "/_count"):
			name := strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/_count")
			name, _ = url.PathUnescape(name)
			target, ok := resolveTarget(name)
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			docs := indices[target]
			if strings.Contains(string(bodyBytes), `"vector"`) {
				vc := 0
				for _, d := range docs {
					if len(d.Vector) > 0 {
						vc++
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"count": vc})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"count": len(docs)})

		case strings.HasSuffix(path, "/_search"):
			name := strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/_search")
			name, _ = url.PathUnescape(name)
			target, ok := resolveTarget(name)
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var q map[string]any
			_ = json.Unmarshal(bodyBytes, &q)
			hits := evalMockSearch(indices[target], q)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"hits": map[string]any{"hits": hits},
			})

		case strings.Contains(path, "/_doc/") && r.Method == http.MethodDelete:
			parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/_doc/", 2)
			name, _ := url.PathUnescape(parts[0])
			docID, _ := url.PathUnescape(parts[1])
			target, ok := resolveTarget(name)
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if _, exists := indices[target][docID]; !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(indices[target], docID)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "deleted"})

		case strings.HasSuffix(path, "/_refresh"):
			_ = json.NewEncoder(w).Encode(map[string]any{"_shards": map[string]any{"failed": 0}})

		case r.Method == http.MethodPut:
			name, _ := url.PathUnescape(strings.TrimPrefix(path, "/"))
			if _, exists := indices[name]; !exists {
				indices[name] = make(map[string]mockDoc)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"acknowledged": true})

		case r.Method == http.MethodDelete:
			name, _ := url.PathUnescape(strings.TrimPrefix(path, "/"))
			delete(indices, name)
			_ = json.NewEncoder(w).Encode(map[string]any{"acknowledged": true})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
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
