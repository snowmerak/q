// Package opensearch implements Q v2's storage.SearchIndex over OpenSearch
// with alias-managed blue/green index rebuilds and observer_by isolation.
package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/v2/pkg/storage"
)

// Config configures an OpenSearch SearchIndex client.
type Config struct {
	Endpoint    string
	IndexPrefix string
	Username    string
	Password    string
	BearerToken string
	Timeout     time.Duration
	HTTPClient  *http.Client
}

// Index implements storage.SearchIndex backed by an OpenSearch alias.
type Index struct {
	endpoint    string
	prefix      string
	alias       string
	username    string
	password    string
	bearerToken string
	client      *http.Client

	mu          sync.Mutex
	gen         int64
	vectorModel string
	vectorDim   int
}

// Open initializes the OpenSearch SearchIndex, creating the initial backing index
// and <prefix>_records alias when they do not yet exist.
func Open(ctx context.Context, cfg Config) (*Index, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, fmt.Errorf("%w: opensearch endpoint is required", storage.ErrInvalidInput)
	}
	prefix := sanitizePrefix(cfg.IndexPrefix)
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}

	idx := &Index{
		endpoint:    endpoint,
		prefix:      prefix,
		alias:       prefix + "_records",
		username:    cfg.Username,
		password:    cfg.Password,
		bearerToken: cfg.BearerToken,
		client:      hc,
	}
	if err := idx.ensureAlias(ctx); err != nil {
		return nil, err
	}
	return idx, nil
}

func sanitizePrefix(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return "q"
	}
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func (i *Index) Close() error { return nil }

func (i *Index) backingIndexName(gen int64) string {
	return fmt.Sprintf("%s_records_%d", i.prefix, gen)
}

func (i *Index) ensureAlias(ctx context.Context) error {
	status, body, err := i.doJSON(ctx, http.MethodGet, "/_alias/"+url.PathEscape(i.alias), nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		var aliasResp map[string]struct {
			Aliases map[string]any `json:"aliases"`
		}
		if json.Unmarshal(body, &aliasResp) == nil && len(aliasResp) > 0 {
			var maxGen int64
			for name := range aliasResp {
				if g := parseGen(i.prefix, name); g > maxGen {
					maxGen = g
				}
			}
			i.mu.Lock()
			if maxGen == 0 {
				maxGen = 1
			}
			i.gen = maxGen
			i.mu.Unlock()
			return nil
		}
	}
	if status != http.StatusNotFound && status != http.StatusOK {
		return fmt.Errorf("opensearch get alias %s: status %d: %s", i.alias, status, string(body))
	}

	i.mu.Lock()
	i.gen = 1
	first := i.backingIndexName(1)
	i.mu.Unlock()

	if err := i.createBackingIndex(ctx, first, 0); err != nil {
		return err
	}
	return i.swapAlias(ctx, nil, first)
}

func parseGen(prefix, indexName string) int64 {
	tag := prefix + "_records_"
	if !strings.HasPrefix(indexName, tag) {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimPrefix(indexName, tag), 10, 64)
	return n
}

func (i *Index) createBackingIndex(ctx context.Context, name string, vectorDim int) error {
	props := map[string]any{
		"id":          map[string]any{"type": "keyword"},
		"entity_type": map[string]any{"type": "keyword"},
		"scope":       map[string]any{"type": "keyword"},
		"owner_id":    map[string]any{"type": "keyword"},
		"observed_by": map[string]any{"type": "keyword"},
		"text":        map[string]any{"type": "text"},
		"occurred_at": map[string]any{"type": "long"},
		"revision":    map[string]any{"type": "long"},
	}
	settings := map[string]any{}
	if vectorDim > 0 {
		settings["index"] = map[string]any{"knn": true}
		props["vector"] = map[string]any{
			"type":      "knn_vector",
			"dimension": vectorDim,
		}
	}
	payload := map[string]any{
		"settings": settings,
		"mappings": map[string]any{"properties": props},
	}
	status, body, err := i.doJSON(ctx, http.MethodPut, "/"+url.PathEscape(name), payload)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("opensearch create index %s: status %d: %s", name, status, string(body))
	}
	return nil
}

func (i *Index) currentAliasTargets(ctx context.Context) ([]string, error) {
	status, body, err := i.doJSON(ctx, http.MethodGet, "/_alias/"+url.PathEscape(i.alias), nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status >= 300 {
		return nil, fmt.Errorf("opensearch get alias: status %d: %s", status, string(body))
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

type httpStatusError struct {
	op     string
	status int
	body   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s: status %d: %s", e.op, e.status, e.body)
}

func (i *Index) swapAlias(ctx context.Context, removeIndices []string, addIndex string) error {
	actions := make([]map[string]any, 0, len(removeIndices)+1)
	for _, old := range removeIndices {
		if old == "" || old == addIndex {
			continue
		}
		actions = append(actions, map[string]any{
			"remove": map[string]any{"index": old, "alias": i.alias},
		})
	}
	actions = append(actions, map[string]any{
		"add": map[string]any{"index": addIndex, "alias": i.alias},
	})
	status, body, err := i.doJSON(ctx, http.MethodPost, "/_aliases", map[string]any{"actions": actions})
	if err != nil {
		return err
	}
	if status >= 300 {
		return &httpStatusError{op: "opensearch swap alias", status: status, body: string(body)}
	}
	return nil
}

func (i *Index) ConfigureVector(ctx context.Context, model string, dim int) error {
	if dim <= 0 {
		return fmt.Errorf("%w: vector dim must be > 0", storage.ErrInvalidInput)
	}
	i.mu.Lock()
	if i.vectorDim == dim {
		i.vectorModel = model
		i.mu.Unlock()
		return nil
	}
	i.vectorModel = model
	i.vectorDim = dim
	i.gen++
	nextIdx := i.backingIndexName(i.gen)
	i.mu.Unlock()

	oldTargets, _ := i.currentAliasTargets(ctx)
	if err := i.createBackingIndex(ctx, nextIdx, dim); err != nil {
		return err
	}
	if err := i.swapAlias(ctx, oldTargets, nextIdx); err != nil {
		return err
	}
	for _, old := range oldTargets {
		if old != nextIdx {
			_, _, _ = i.doJSON(ctx, http.MethodDelete, "/"+url.PathEscape(old), nil)
		}
	}
	return nil
}

func docKey(entityType, id string) string {
	return entityType + ":" + id
}

type osRecord struct {
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

func (i *Index) Upsert(ctx context.Context, doc storage.SearchDocument) error {
	return i.UpsertBatch(ctx, []storage.SearchDocument{doc})
}

func (i *Index) UpsertBatch(ctx context.Context, docs []storage.SearchDocument) error {
	if len(docs) == 0 {
		return nil
	}
	return i.bulkUpsert(ctx, i.alias, docs)
}

func (i *Index) bulkUpsert(ctx context.Context, target string, docs []storage.SearchDocument) error {
	i.mu.Lock()
	vDim := i.vectorDim
	i.mu.Unlock()

	var buf bytes.Buffer
	for _, d := range docs {
		if d.ID == "" || d.EntityType == "" {
			return fmt.Errorf("%w: id and entity_type are required", storage.ErrInvalidInput)
		}
		meta := map[string]any{
			"index": map[string]any{
				"_index": target,
				"_id":    docKey(d.EntityType, d.ID),
			},
		}
		bMeta, _ := json.Marshal(meta)
		rec := osRecord{
			ID:         d.ID,
			EntityType: d.EntityType,
			Scope:      d.Scope,
			OwnerID:    d.OwnerID,
			ObservedBy: d.ObservedBy,
			Text:       d.Text,
			OccurredAt: d.OccurredAt,
			Revision:   d.Revision,
		}
		if vDim > 0 && len(d.Vector) == vDim {
			rec.Vector = d.Vector
		}
		bRec, _ := json.Marshal(rec)
		buf.Write(bMeta)
		buf.WriteByte('\n')
		buf.Write(bRec)
		buf.WriteByte('\n')
	}

	req, err := i.newRequest(ctx, http.MethodPost, "/_bulk?refresh=wait_for", &buf, "application/x-ndjson")
	if err != nil {
		return err
	}
	resp, err := i.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("opensearch bulk status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (i *Index) Delete(ctx context.Context, entityType, id string) error {
	path := "/" + url.PathEscape(i.alias) + "/_doc/" + url.PathEscape(docKey(entityType, id)) + "?refresh=wait_for"
	status, body, err := i.doJSON(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound || status < 300 {
		return nil
	}
	return fmt.Errorf("opensearch delete status %d: %s", status, string(body))
}

type osSearchResponse struct {
	Hits struct {
		Hits []struct {
			Score  float64  `json:"_score"`
			Source osRecord `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

func (i *Index) Search(ctx context.Context, q storage.SearchQuery) ([]storage.SearchHit, error) {
	mode := strings.ToLower(strings.TrimSpace(q.Mode))
	if mode == "" {
		mode = "hybrid"
	}
	i.mu.Lock()
	vDim := i.vectorDim
	i.mu.Unlock()

	if mode == "semantic" && (vDim == 0 || len(q.Vector) == 0) {
		return nil, storage.ErrUnavailable
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}

	filters := make([]map[string]any, 0, 3)
	if len(q.EntityTypes) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"entity_type": q.EntityTypes}})
	}
	if len(q.Scopes) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"scope": q.Scopes}})
	}
	if obs := strings.TrimSpace(q.ObserverID); obs != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"observed_by": obs}})
	}

	useVector := (mode == "semantic" || mode == "hybrid") && vDim > 0 && len(q.Vector) == vDim
	useLexical := mode == "lexical" || (mode == "hybrid" && strings.TrimSpace(q.Text) != "")

	if mode == "hybrid" && !useVector {
		useLexical = true
	}

	var lexHits, vecHits []storage.SearchHit
	if useLexical {
		must := []map[string]any{{"match_all": map[string]any{}}}
		if txt := strings.TrimSpace(q.Text); txt != "" {
			must = []map[string]any{{"match": map[string]any{"text": txt}}}
		}
		payload := map[string]any{
			"size": limit * 2,
			"query": map[string]any{
				"bool": map[string]any{
					"must":   must,
					"filter": filters,
				},
			},
		}
		hits, err := i.runSearch(ctx, payload)
		if err != nil {
			return nil, err
		}
		lexHits = hits
	}

	if useVector {
		knnBody := map[string]any{
			"vector": q.Vector,
			"k":      limit * 2,
		}
		if len(filters) > 0 {
			knnBody["filter"] = map[string]any{"bool": map[string]any{"filter": filters}}
		}
		payload := map[string]any{
			"size": limit * 2,
			"query": map[string]any{
				"knn": map[string]any{
					"vector": knnBody,
				},
			},
		}
		hits, err := i.runSearch(ctx, payload)
		if err != nil {
			return nil, err
		}
		vecHits = hits
	}

	if useLexical && useVector {
		return fuseRRF(lexHits, vecHits, q.KeywordWeight, limit), nil
	}
	if useVector {
		if len(vecHits) > limit {
			return vecHits[:limit], nil
		}
		return vecHits, nil
	}
	if len(lexHits) > limit {
		return lexHits[:limit], nil
	}
	return lexHits, nil
}

func (i *Index) runSearch(ctx context.Context, payload map[string]any) ([]storage.SearchHit, error) {
	status, body, err := i.doJSON(ctx, http.MethodPost, "/"+url.PathEscape(i.alias)+"/_search", payload)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, fmt.Errorf("opensearch search status %d: %s", status, string(body))
	}
	var parsed osSearchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]storage.SearchHit, 0, len(parsed.Hits.Hits))
	for _, h := range parsed.Hits.Hits {
		out = append(out, storage.SearchHit{
			ID:         h.Source.ID,
			EntityType: h.Source.EntityType,
			Score:      h.Score,
			Revision:   h.Source.Revision,
		})
	}
	return out, nil
}

func fuseRRF(lex, vec []storage.SearchHit, kwWeight float64, limit int) []storage.SearchHit {
	if kwWeight <= 0 || kwWeight >= 1 {
		kwWeight = 0.5
	}
	vw := 1.0 - kwWeight
	const rrfK = 60.0

	byKey := make(map[string]storage.SearchHit, len(lex)+len(vec))
	scores := make(map[string]float64, len(lex)+len(vec))
	for rank, h := range lex {
		k := docKey(h.EntityType, h.ID)
		byKey[k] = h
		scores[k] += kwWeight * (1.0 / (rrfK + float64(rank+1)))
	}
	for rank, h := range vec {
		k := docKey(h.EntityType, h.ID)
		if _, ok := byKey[k]; !ok {
			byKey[k] = h
		}
		scores[k] += vw * (1.0 / (rrfK + float64(rank+1)))
	}

	merged := make([]storage.SearchHit, 0, len(byKey))
	for k, h := range byKey {
		h.Score = scores[k]
		merged = append(merged, h)
	}
	sort.Slice(merged, func(a, b int) bool {
		if merged[a].Score == merged[b].Score {
			return merged[a].ID < merged[b].ID
		}
		return merged[a].Score > merged[b].Score
	})
	if len(merged) > limit {
		return merged[:limit]
	}
	return merged
}

func (i *Index) ResetAndRebuild(ctx context.Context, docs <-chan storage.SearchDocument) error {
	oldTargets, _ := i.currentAliasTargets(ctx)

	i.mu.Lock()
	i.gen++
	nextIdx := i.backingIndexName(i.gen)
	vDim := i.vectorDim
	i.mu.Unlock()

	if err := i.createBackingIndex(ctx, nextIdx, vDim); err != nil {
		return err
	}

	cleanupNext := func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, _ = i.doJSON(cleanCtx, http.MethodDelete, "/"+url.PathEscape(nextIdx), nil)
	}

	batch := make([]storage.SearchDocument, 0, 200)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := i.bulkUpsert(ctx, nextIdx, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	if docs != nil {
		for {
			select {
			case <-ctx.Done():
				cleanupNext()
				return ctx.Err()
			case d, ok := <-docs:
				if !ok {
					goto drained
				}
				batch = append(batch, d)
				if len(batch) >= 200 {
					if err := flush(); err != nil {
						cleanupNext()
						return err
					}
				}
			}
		}
	}
drained:
	if err := ctx.Err(); err != nil {
		cleanupNext()
		return err
	}
	if err := flush(); err != nil {
		cleanupNext()
		return err
	}

	if err := i.swapAlias(ctx, oldTargets, nextIdx); err != nil {
		checkCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		targets, targetErr := i.currentAliasTargets(checkCtx)
		if targetErr == nil && slices.Contains(targets, nextIdx) {
			// Swap already committed on OpenSearch despite the lost/errored HTTP response.
			for _, old := range oldTargets {
				if old != nextIdx && !slices.Contains(targets, old) {
					_, _, _ = i.doJSON(checkCtx, http.MethodDelete, "/"+url.PathEscape(old), nil)
				}
			}
			_, _, _ = i.doJSON(checkCtx, http.MethodPost, "/"+url.PathEscape(nextIdx)+"/_refresh", nil)
			return nil
		}
		var statusErr *httpStatusError
		if targetErr == nil && errors.As(err, &statusErr) &&
			statusErr.status >= 400 && statusErr.status < 500 && statusErr.status != http.StatusRequestTimeout {
			cleanupNext()
		}
		return err
	}
	for _, old := range oldTargets {
		if old != nextIdx {
			_, _, _ = i.doJSON(ctx, http.MethodDelete, "/"+url.PathEscape(old), nil)
		}
	}
	_, _, _ = i.doJSON(ctx, http.MethodPost, "/"+url.PathEscape(nextIdx)+"/_refresh", nil)
	return nil
}

func (i *Index) Counts(ctx context.Context) (uint64, int, error) {
	status, body, err := i.doJSON(ctx, http.MethodGet, "/"+url.PathEscape(i.alias)+"/_count", nil)
	if err != nil {
		return 0, 0, err
	}
	if status >= 300 {
		return 0, 0, fmt.Errorf("opensearch count status %d: %s", status, string(body))
	}
	var all struct {
		Count uint64 `json:"count"`
	}
	if err := json.Unmarshal(body, &all); err != nil {
		return 0, 0, err
	}

	i.mu.Lock()
	vDim := i.vectorDim
	i.mu.Unlock()
	if vDim == 0 {
		return all.Count, 0, nil
	}

	vStatus, vBody, err := i.doJSON(ctx, http.MethodPost, "/"+url.PathEscape(i.alias)+"/_count", map[string]any{
		"query": map[string]any{"exists": map[string]any{"field": "vector"}},
	})
	if err != nil || vStatus >= 300 {
		return all.Count, 0, nil
	}
	var vec struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(vBody, &vec)
	return all.Count, vec.Count, nil
}

func (i *Index) newRequest(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, i.endpoint+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if i.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+i.bearerToken)
	} else if i.username != "" {
		req.SetBasicAuth(i.username, i.password)
	}
	return req, nil
}

func (i *Index) doJSON(ctx context.Context, method, path string, payload any) (int, []byte, error) {
	var bodyReader io.Reader
	ct := ""
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		bodyReader = bytes.NewReader(b)
		ct = "application/json"
	}
	req, err := i.newRequest(ctx, method, path, bodyReader, ct)
	if err != nil {
		return 0, nil, err
	}
	resp, err := i.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}
