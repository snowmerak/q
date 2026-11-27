package opensearchindex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/sessionstore"
)

type indexState struct {
	client  *Client
	alias   string
	mu      sync.RWMutex
	backing string
	vector  sessionstore.VectorConfig
	closed  bool
}

func (s *indexState) requireOpenLocked() error {
	if s == nil || s.closed {
		return errors.New("opensearchindex: index is closed")
	}
	return nil
}

type recordWriter struct{ state *indexState }
type recordReader struct{ state *indexState }
type recordSearcher struct{ state *indexState }
type vectorManager struct{ state *indexState }

// Index is a sessionstore.SearchIndex backed by an OpenSearch alias and its
// active backing index. It composes narrow writer, reader, searcher, and vector
// adapters via struct embedding while leaving shared Client ownership intact on
// Close.
type Index struct {
	*recordWriter
	*recordReader
	*recordSearcher
	*vectorManager
	state *indexState
}

var _ sessionstore.SearchIndex = (*Index)(nil)

// OpenIndex opens or creates an OpenSearch alias for logicalName. When the
// alias already exists and vector.Model is empty but the backing index has a
// knn_vector mapping, existing dimensions are preserved.
func OpenIndex(ctx context.Context, client *Client, logicalName string, vector sessionstore.VectorConfig) (*Index, bool, error) {
	if client == nil {
		return nil, false, errors.New("opensearchindex: client is required")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
	}
	normalized, err := sessionstore.NormalizeVectorConfig(vector)
	if err != nil {
		return nil, false, err
	}
	alias := client.IndexName(logicalName)
	backing, exists, err := client.resolveAlias(ctx, alias)
	if err != nil {
		return nil, false, err
	}
	created := false
	if !exists {
		backing = alias + "-0"
		if err := client.createIndex(ctx, backing, normalized); err != nil {
			return nil, false, err
		}
		if err := client.putAlias(ctx, backing, alias); err != nil {
			_ = client.deleteIndex(ctx, backing)
			return nil, false, err
		}
		created = true
	} else if normalized.Enabled() {
		dims, err := client.detectVectorDimensions(ctx, backing)
		if err != nil {
			return nil, false, err
		}
		if dims != normalized.Dimensions {
			nextBacking := nextBackingName(alias, backing)
			state := &indexState{client: client, alias: alias, backing: backing, vector: normalized}
			vm := &vectorManager{state: state}
			if err := vm.recreateIndex(ctx, nextBacking, normalized); err != nil {
				return nil, false, err
			}
			backing = state.backing
		}
	}
	state := &indexState{
		client:  client,
		alias:   alias,
		backing: backing,
		vector:  normalized,
	}
	return &Index{
		recordWriter:   &recordWriter{state: state},
		recordReader:   &recordReader{state: state},
		recordSearcher: &recordSearcher{state: state},
		vectorManager:  &vectorManager{state: state},
		state:          state,
	}, created, nil
}

// Alias returns the OpenSearch alias managed by this Index.
func (i *Index) Alias() string {
	if i == nil || i.state == nil {
		return ""
	}
	return i.state.alias
}

// Close marks this Index handle closed without closing the shared *Client.
func (i *Index) Close() error {
	if i == nil || i.state == nil {
		return nil
	}
	i.state.mu.Lock()
	i.state.closed = true
	i.state.mu.Unlock()
	return nil
}

func nextBackingName(alias, current string) string {
	prefix := alias + "-"
	if rest, ok := strings.CutPrefix(current, prefix); ok {
		if n, err := strconv.Atoi(rest); err == nil {
			return fmt.Sprintf("%s%d", prefix, n+1)
		}
	}
	return fmt.Sprintf("%s%d", prefix, time.Now().UTC().UnixNano())
}

type projectionDoc struct {
	ID     string    `json:"id"`
	Vector []float32 `json:"vector"`
}

type openSearchDoc struct {
	ID                string          `json:"id"`
	Kind              string          `json:"kind"`
	RunID             string          `json:"run_id,omitempty"`
	TaskID            string          `json:"task_id,omitempty"`
	ParentID          string          `json:"parent_id,omitempty"`
	Role              string          `json:"role,omitempty"`
	Model             string          `json:"model,omitempty"`
	Effort            string          `json:"effort,omitempty"`
	Status            string          `json:"status,omitempty"`
	Scope             string          `json:"scope,omitempty"`
	Refs              []string        `json:"refs,omitempty"`
	Tags              []string        `json:"tags,omitempty"`
	WorkspaceID       string          `json:"workspace_id,omitempty"`
	EntityID          string          `json:"entity_id,omitempty"`
	PostID            string          `json:"post_id,omitempty"`
	Summary           string          `json:"summary,omitempty"`
	Content           string          `json:"content,omitempty"`
	SearchText        string          `json:"search_text,omitempty"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
	RecordRaw         string          `json:"record_raw"`
	EmbeddingVec      []float32       `json:"embedding_vec,omitempty"`
	VectorProjections []projectionDoc `json:"vector_projections,omitempty"`
}

func encodeRecordDoc(record sessionstore.Record, vectorCfg sessionstore.VectorConfig) (openSearchDoc, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return openSearchDoc{}, err
	}
	doc := openSearchDoc{
		ID:          record.ID,
		Kind:        record.Kind,
		RunID:       record.RunID,
		TaskID:      record.TaskID,
		ParentID:    record.ParentID,
		Role:        record.Role,
		Model:       record.Model,
		Effort:      record.Effort,
		Status:      record.Status,
		Scope:       record.Scope,
		Refs:        record.Refs,
		Tags:        record.Tags,
		WorkspaceID: record.WorkspaceID,
		EntityID:    record.EntityID,
		PostID:      record.PostID,
		Summary:     record.Summary,
		Content:     record.Content,
		SearchText:  record.SearchText,
		CreatedAt:   record.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:   record.UpdatedAt.UTC().Format(time.RFC3339Nano),
		RecordRaw:   string(raw),
	}
	if vectorCfg.Enabled() {
		if record.Embedding != nil && record.Embedding.Model == vectorCfg.Model &&
			record.Embedding.Dimensions == vectorCfg.Dimensions && len(record.Embedding.Vector) == vectorCfg.Dimensions {
			doc.EmbeddingVec = record.Embedding.Vector
		}
		for _, proj := range record.VectorProjections {
			if proj.Embedding.Model == vectorCfg.Model && proj.Embedding.Dimensions == vectorCfg.Dimensions &&
				len(proj.Embedding.Vector) == vectorCfg.Dimensions {
				doc.VectorProjections = append(doc.VectorProjections, projectionDoc{
					ID:     proj.ID,
					Vector: proj.Embedding.Vector,
				})
			}
		}
	}
	return doc, nil
}

func decodeRecordRaw(raw string) (sessionstore.Record, error) {
	var record sessionstore.Record
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return sessionstore.Record{}, err
	}
	return record, nil
}

func (s *indexState) mgetRecords(ctx context.Context, backing string, ids []string) (map[string]sessionstore.Record, error) {
	out := make(map[string]sessionstore.Record, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return out, nil
	}
	var resp struct {
		Docs []struct {
			ID     string `json:"_id"`
			Found  bool   `json:"found"`
			Source struct {
				RecordRaw string `json:"record_raw"`
			} `json:"_source"`
		} `json:"docs"`
	}
	req := map[string]any{"ids": unique}
	if err := s.client.doJSON(ctx, http.MethodPost, "/"+url.PathEscape(backing)+"/_mget?_source_includes=record_raw", req, &resp); err != nil {
		return nil, err
	}
	for _, d := range resp.Docs {
		if !d.Found {
			continue
		}
		rec, err := decodeRecordRaw(d.Source.RecordRaw)
		if err != nil {
			return nil, fmt.Errorf("opensearchindex: decode record %q: %w", d.ID, err)
		}
		out[rec.ID] = rec
	}
	return out, nil
}

func (s *indexState) bulkUpsert(ctx context.Context, backing string, records []sessionstore.Record, vector sessionstore.VectorConfig) error {
	if len(records) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, rec := range records {
		metaLine, err := json.Marshal(map[string]any{
			"index": map[string]any{"_index": backing, "_id": rec.ID},
		})
		if err != nil {
			return err
		}
		doc, err := encodeRecordDoc(rec, vector)
		if err != nil {
			return err
		}
		docLine, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		buf.Write(metaLine)
		buf.WriteByte('\n')
		buf.Write(docLine)
		buf.WriteByte('\n')
	}
	var resp struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			ID     string `json:"_id"`
			Status int    `json:"status"`
			Error  any    `json:"error"`
		} `json:"items"`
	}
	path := "/_bulk?refresh=" + url.QueryEscape(s.client.refresh)
	if err := s.client.doNDJSON(ctx, path, buf.Bytes(), &resp); err != nil {
		return err
	}
	if resp.Errors {
		for _, item := range resp.Items {
			for action, detail := range item {
				if detail.Error != nil {
					return fmt.Errorf("opensearchindex: bulk %s failed for %q (status %d): %v", action, detail.ID, detail.Status, detail.Error)
				}
			}
		}
		return errors.New("opensearchindex: bulk operation reported item errors")
	}
	return nil
}

func (w *recordWriter) Save(ctx context.Context, record sessionstore.Record) (sessionstore.Record, error) {
	saved, err := w.SaveBatch(ctx, []sessionstore.Record{record})
	if err != nil {
		if len(saved) == 1 {
			return saved[0], err
		}
		return sessionstore.Record{}, err
	}
	return saved[0], nil
}

func (w *recordWriter) SaveBatch(ctx context.Context, records []sessionstore.Record) ([]sessionstore.Record, error) {
	if len(records) == 0 {
		return nil, nil
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	w.state.mu.RLock()
	defer w.state.mu.RUnlock()
	if err := w.state.requireOpenLocked(); err != nil {
		return nil, err
	}

	lookupIDs := make([]string, 0, len(records))
	for _, r := range records {
		if id := strings.TrimSpace(r.ID); id != "" {
			lookupIDs = append(lookupIDs, id)
		}
	}
	existing, err := w.state.mgetRecords(ctx, w.state.backing, lookupIDs)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	seen := make(map[string]sessionstore.Record, len(records))
	prepared := make([]sessionstore.Record, 0, len(records))
	for _, r := range records {
		var prevPtr *sessionstore.Record
		if prev, ok := seen[strings.TrimSpace(r.ID)]; ok {
			p := prev
			prevPtr = &p
		} else if prev, ok := existing[strings.TrimSpace(r.ID)]; ok {
			p := prev
			prevPtr = &p
		}
		item, err := sessionstore.PrepareRecord(r, prevPtr, now)
		if err != nil {
			return nil, err
		}
		seen[item.ID] = item
		prepared = append(prepared, item)
	}
	if err := w.state.bulkUpsert(ctx, w.state.backing, prepared, w.state.vector); err != nil {
		return prepared, &sessionstore.IndexingError{RecordID: prepared[0].ID, Err: err}
	}
	return prepared, nil
}

func (w *recordWriter) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("opensearchindex: record ID is required")
	}
	w.state.mu.RLock()
	defer w.state.mu.RUnlock()
	if err := w.state.requireOpenLocked(); err != nil {
		return err
	}
	path := "/" + url.PathEscape(w.state.backing) + "/_doc/" + url.PathEscape(id) + "?refresh=" + url.QueryEscape(w.state.client.refresh)
	err := w.state.client.doJSON(ctx, http.MethodDelete, path, nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (r *recordReader) Get(ctx context.Context, id string) (sessionstore.Record, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return sessionstore.Record{}, sessionstore.ErrNotFound
	}
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	if err := r.state.requireOpenLocked(); err != nil {
		return sessionstore.Record{}, err
	}
	path := "/" + url.PathEscape(r.state.backing) + "/_doc/" + url.PathEscape(id) + "?_source_includes=record_raw"
	var resp struct {
		Found  bool `json:"found"`
		Source struct {
			RecordRaw string `json:"record_raw"`
		} `json:"_source"`
	}
	err := r.state.client.doJSON(ctx, http.MethodGet, path, nil, &resp)
	if isNotFound(err) {
		return sessionstore.Record{}, sessionstore.ErrNotFound
	}
	if err != nil {
		return sessionstore.Record{}, err
	}
	if !resp.Found {
		return sessionstore.Record{}, sessionstore.ErrNotFound
	}
	return decodeRecordRaw(resp.Source.RecordRaw)
}

func (r *recordReader) IndexCounts(ctx context.Context) (uint64, int, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	if err := r.state.requireOpenLocked(); err != nil {
		return 0, 0, err
	}
	var totalResp struct {
		Count uint64 `json:"count"`
	}
	if err := r.state.client.doJSON(ctx, http.MethodGet, "/"+url.PathEscape(r.state.backing)+"/_count", nil, &totalResp); err != nil {
		return 0, 0, err
	}
	if !r.state.vector.Enabled() {
		return totalResp.Count, 0, nil
	}
	vecQuery := map[string]any{
		"query": map[string]any{
			"bool": map[string]any{
				"should": []map[string]any{
					{"exists": map[string]any{"field": "embedding_vec"}},
					{"nested": map[string]any{
						"path":  "vector_projections",
						"query": map[string]any{"exists": map[string]any{"field": "vector_projections.vector"}},
					}},
				},
				"minimum_should_match": 1,
			},
		},
	}
	var vecResp struct {
		Count int `json:"count"`
	}
	if err := r.state.client.doJSON(ctx, http.MethodPost, "/"+url.PathEscape(r.state.backing)+"/_count", vecQuery, &vecResp); err != nil {
		return totalResp.Count, 0, err
	}
	return totalResp.Count, vecResp.Count, nil
}

func (v *vectorManager) VectorConfig() sessionstore.VectorConfig {
	if v == nil || v.state == nil {
		return sessionstore.VectorConfig{}
	}
	v.state.mu.RLock()
	defer v.state.mu.RUnlock()
	return v.state.vector
}

func (v *vectorManager) ApplyEmbeddings(ctx context.Context, updates []sessionstore.EmbeddingUpdate) (sessionstore.EmbeddingUpdateResult, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return sessionstore.EmbeddingUpdateResult{}, err
		}
	}
	v.state.mu.RLock()
	defer v.state.mu.RUnlock()
	if err := v.state.requireOpenLocked(); err != nil {
		return sessionstore.EmbeddingUpdateResult{}, err
	}
	result := sessionstore.EmbeddingUpdateResult{}
	if len(updates) == 0 {
		return result, nil
	}
	ids := make([]string, 0, len(updates))
	seen := make(map[string]bool, len(updates))
	for _, u := range updates {
		if u.ID == "" || u.SourceDigest == "" || seen[u.ID] {
			return result, errors.New("sessionstore: embedding update needs a unique ID and source digest")
		}
		seen[u.ID] = true
		ids = append(ids, u.ID)
	}
	existing, err := v.state.mgetRecords(ctx, v.state.backing, ids)
	if err != nil {
		return result, err
	}
	toSave := make([]sessionstore.Record, 0, len(updates))
	for _, u := range updates {
		record, ok := existing[u.ID]
		if !ok {
			result.Skipped++
			continue
		}
		digest, err := sessionstore.RecordSourceDigest(record)
		if err != nil {
			return result, err
		}
		if digest != u.SourceDigest {
			result.Skipped++
			continue
		}
		record.Embedding = u.Embedding
		record.VectorProjections = u.VectorProjections
		if err := validateRecordEmbeddings(record, v.state.vector); err != nil {
			return result, err
		}
		toSave = append(toSave, record)
	}
	if len(toSave) == 0 {
		return result, nil
	}
	if err := v.state.bulkUpsert(ctx, v.state.backing, toSave, v.state.vector); err != nil {
		return result, err
	}
	result.Updated = len(toSave)
	return result, nil
}

func validateRecordEmbeddings(record sessionstore.Record, cfg sessionstore.VectorConfig) error {
	check := func(emb sessionstore.Embedding) error {
		if !cfg.Enabled() || emb.Model != cfg.Model || emb.Dimensions != cfg.Dimensions || len(emb.Vector) != cfg.Dimensions {
			return fmt.Errorf("sessionstore: embedding update for %q does not match active model", record.ID)
		}
		return nil
	}
	if record.Embedding != nil {
		if err := check(*record.Embedding); err != nil {
			return err
		}
	}
	for _, proj := range record.VectorProjections {
		if err := check(proj.Embedding); err != nil {
			return err
		}
	}
	return nil
}

func (v *vectorManager) ConfigureVector(ctx context.Context, config sessionstore.VectorConfig) error {
	normalized, err := sessionstore.NormalizeVectorConfig(config)
	if err != nil {
		return err
	}
	v.state.mu.Lock()
	defer v.state.mu.Unlock()
	if err := v.state.requireOpenLocked(); err != nil {
		return err
	}
	if normalized.Model == v.state.vector.Model && normalized.Dimensions == v.state.vector.Dimensions {
		v.state.vector = normalized
		return nil
	}
	nextBacking := nextBackingName(v.state.alias, v.state.backing)
	return v.recreateIndex(ctx, nextBacking, normalized)
}

func (v *vectorManager) recreateIndex(ctx context.Context, nextBacking string, normalized sessionstore.VectorConfig) error {
	_ = v.state.client.deleteIndex(ctx, nextBacking)
	if err := v.state.client.createIndex(ctx, nextBacking, normalized); err != nil {
		return err
	}
	keepNext := false
	defer func() {
		if !keepNext {
			_ = v.state.client.deleteIndex(ctx, nextBacking)
		}
	}()

	var searchAfter []any
	for {
		body := map[string]any{
			"size":            500,
			"_source":         []string{"record_raw"},
			"sort":            []map[string]any{{"id": "asc"}},
			"track_total_hits": false,
		}
		if len(searchAfter) > 0 {
			body["search_after"] = searchAfter
		}
		var resp searchHitEnvelope
		if err := v.state.client.doJSON(ctx, http.MethodPost, "/"+url.PathEscape(v.state.backing)+"/_search", body, &resp); err != nil {
			return err
		}
		if len(resp.Hits.Hits) == 0 {
			break
		}
		batch := make([]sessionstore.Record, 0, len(resp.Hits.Hits))
		for _, h := range resp.Hits.Hits {
			rec, err := decodeRecordRaw(h.Source.RecordRaw)
			if err != nil {
				return err
			}
			batch = append(batch, rec)
			searchAfter = h.Sort
		}
		if err := v.state.bulkUpsert(ctx, nextBacking, batch, normalized); err != nil {
			return err
		}
	}

	oldBacking := v.state.backing
	if err := v.state.client.swapAlias(ctx, oldBacking, nextBacking, v.state.alias); err != nil {
		return err
	}
	keepNext = true
	v.state.backing = nextBacking
	v.state.vector = normalized
	_ = v.state.client.deleteIndex(ctx, oldBacking)
	return nil
}

type searchHitEnvelope struct {
	Hits struct {
		Total struct {
			Value uint64 `json:"value"`
		} `json:"total"`
		Hits []struct {
			ID     string  `json:"_id"`
			Score  float64 `json:"_score"`
			Sort   []any   `json:"sort"`
			Source struct {
				RecordRaw string `json:"record_raw"`
			} `json:"_source"`
			InnerHits map[string]struct {
				Hits struct {
					Hits []struct {
						Score  float64 `json:"_score"`
						Source struct {
							ID string `json:"id"`
						} `json:"_source"`
					} `json:"hits"`
				} `json:"hits"`
			} `json:"inner_hits"`
		} `json:"hits"`
	} `json:"hits"`
}

func (s *recordSearcher) Search(ctx context.Context, options sessionstore.SearchOptions) (sessionstore.SearchResult, error) {
	if ctx == nil {
		return sessionstore.SearchResult{}, errors.New("opensearchindex: search context is nil")
	}
	if err := sessionstore.ValidateSearchOptions(&options); err != nil {
		return sessionstore.SearchResult{}, err
	}
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	if err := s.state.requireOpenLocked(); err != nil {
		return sessionstore.SearchResult{}, err
	}
	if options.Vector != nil {
		return s.searchHybrid(ctx, options)
	}
	return s.searchTextOnly(ctx, options)
}

func buildFilterClauses(options sessionstore.SearchOptions) []map[string]any {
	clauses := make([]map[string]any, 0, 16)
	for _, item := range []struct {
		field  string
		values []string
	}{
		{"workspace_id", options.Filters.WorkspaceIDs},
		{"entity_id", options.Filters.EntityIDs},
		{"post_id", options.Filters.PostIDs},
		{"run_id", options.Filters.RunIDs},
		{"task_id", options.Filters.TaskIDs},
		{"parent_id", options.Filters.ParentIDs},
		{"kind", options.Filters.Kinds},
		{"role", options.Filters.Roles},
		{"model", options.Filters.Models},
		{"effort", options.Filters.Efforts},
		{"status", options.Filters.Statuses},
		{"scope", options.Filters.Scopes},
		{"refs", options.Filters.Refs},
		{"tags", options.Filters.Tags},
	} {
		if len(item.values) == 1 {
			clauses = append(clauses, map[string]any{"term": map[string]any{item.field: item.values[0]}})
		} else if len(item.values) > 1 {
			clauses = append(clauses, map[string]any{"terms": map[string]any{item.field: item.values}})
		}
	}
	if options.CreatedAfter != nil || options.CreatedBefore != nil {
		rng := map[string]any{}
		if options.CreatedAfter != nil {
			rng["gte"] = options.CreatedAfter.UTC().Format(time.RFC3339Nano)
		}
		if options.CreatedBefore != nil {
			rng["lte"] = options.CreatedBefore.UTC().Format(time.RFC3339Nano)
		}
		clauses = append(clauses, map[string]any{"range": map[string]any{"created_at": rng}})
	}
	return clauses
}

func buildTextMustClause(options sessionstore.SearchOptions) map[string]any {
	text := strings.TrimSpace(options.Text)
	if text == "" {
		return nil
	}
	boosts := sessionstore.TextFieldBoosts{Summary: 1, Content: 1, SearchText: 1}
	if options.TextBoosts != nil {
		boosts = *options.TextBoosts
	}
	should := make([]map[string]any, 0, 3)
	for _, f := range []struct {
		name  string
		boost float64
	}{
		{"summary", boosts.Summary},
		{"content", boosts.Content},
		{"search_text", boosts.SearchText},
	} {
		if f.boost == 0 {
			continue
		}
		should = append(should, map[string]any{
			"match": map[string]any{
				f.name: map[string]any{
					"query": text,
					"boost": f.boost,
				},
			},
		})
	}
	return map[string]any{"bool": map[string]any{"should": should, "minimum_should_match": 1}}
}

func buildBoolQuery(options sessionstore.SearchOptions, includeText bool) map[string]any {
	filters := buildFilterClauses(options)
	var must []map[string]any
	if includeText {
		if textClause := buildTextMustClause(options); textClause != nil {
			must = []map[string]any{textClause}
		}
	}
	if len(must) == 0 && len(filters) == 0 {
		return map[string]any{"match_all": map[string]any{}}
	}
	boolMap := map[string]any{}
	if len(must) > 0 {
		boolMap["must"] = must
	}
	if len(filters) > 0 {
		boolMap["filter"] = filters
	}
	return map[string]any{"bool": boolMap}
}

func sortClause(order sessionstore.SortOrder) []any {
	switch order {
	case sessionstore.SortNewest:
		return []any{map[string]any{"created_at": "desc"}, "_score", map[string]any{"id": "asc"}}
	case sessionstore.SortOldest:
		return []any{map[string]any{"created_at": "asc"}, "_score", map[string]any{"id": "asc"}}
	default:
		return []any{"_score", map[string]any{"created_at": "desc"}, map[string]any{"id": "asc"}}
	}
}

func (s *recordSearcher) searchTextOnly(ctx context.Context, options sessionstore.SearchOptions) (sessionstore.SearchResult, error) {
	size := options.Limit
	from := options.Offset
	if options.ExcludeMessage != nil {
		from = 0
		size = options.Offset + options.Limit + 1
	}
	if options.Recency != nil {
		from = 0
		size = options.Recency.CandidateLimit
		if size == 0 {
			size = min(5000, max(50, (options.Offset+options.Limit)*5))
		}
		if options.ExcludeMessage != nil {
			size = min(5000, max(size, options.Offset+options.Limit+1))
		}
	}
	body := map[string]any{
		"from":             from,
		"size":             size,
		"track_total_hits": true,
		"_source":          []string{"record_raw"},
		"query":            buildBoolQuery(options, true),
		"sort":             sortClause(options.Sort),
	}
	var resp searchHitEnvelope
	if err := s.state.client.doJSON(ctx, http.MethodPost, "/"+url.PathEscape(s.state.backing)+"/_search", body, &resp); err != nil {
		return sessionstore.SearchResult{}, err
	}
	hits := make([]sessionstore.Hit, 0, len(resp.Hits.Hits))
	for _, h := range resp.Hits.Hits {
		rec, err := decodeRecordRaw(h.Source.RecordRaw)
		if err != nil {
			return sessionstore.SearchResult{}, err
		}
		hits = append(hits, sessionstore.Hit{Record: rec, Score: h.Score, BaseScore: h.Score})
	}
	if options.Recency != nil {
		sessionstore.RerankByRecency(hits, *options.Recency)
	}
	total := resp.Hits.Total.Value
	if options.ExcludeMessage != nil {
		filtered := sessionstore.OmitToolCallMessage(hits, options.ExcludeMessage)
		total -= uint64(len(hits) - len(filtered))
		hits = filtered
	}
	if options.Recency != nil || options.ExcludeMessage != nil {
		start := min(options.Offset, len(hits))
		end := min(start+options.Limit, len(hits))
		hits = hits[start:end]
	}
	return sessionstore.SearchResult{Total: total, Hits: hits}, nil
}

func (s *recordSearcher) searchHybrid(ctx context.Context, options sessionstore.SearchOptions) (sessionstore.SearchResult, error) {
	if !s.state.vector.Enabled() {
		return sessionstore.SearchResult{}, errors.New("sessionstore: vector search is not configured")
	}
	if err := sessionstore.ValidateQueryVector(options.Vector.Embedding, s.state.vector.Dimensions); err != nil {
		return sessionstore.SearchResult{}, err
	}
	candidateLimit := options.Vector.CandidateLimit
	if candidateLimit == 0 {
		candidateLimit = min(5000, max(100, (options.Offset+options.Limit)*8))
	}
	if options.Recency != nil && options.Recency.CandidateLimit > candidateLimit {
		candidateLimit = options.Recency.CandidateLimit
	}
	if options.ExcludeMessage != nil {
		candidateLimit = min(5000, max(candidateLimit, options.Offset+options.Limit+1))
	}

	filters := buildFilterClauses(options)
	topKNN := map[string]any{
		"vector": options.Vector.Embedding,
		"k":      candidateLimit,
	}
	if len(filters) > 0 {
		topKNN["filter"] = map[string]any{"bool": map[string]any{"filter": filters}}
	}
	topReq := map[string]any{
		"size":             candidateLimit,
		"_source":          []string{"record_raw"},
		"track_total_hits": false,
		"query":            map[string]any{"knn": map[string]any{"embedding_vec": topKNN}},
	}

	nestedBool := map[string]any{
		"must": []map[string]any{
			{
				"nested": map[string]any{
					"path":       "vector_projections",
					"score_mode": "max",
					"inner_hits": map[string]any{
						"size":    64,
						"_source": []string{"vector_projections.id"},
					},
					"query": map[string]any{
						"knn": map[string]any{
							"vector_projections.vector": map[string]any{
								"vector": options.Vector.Embedding,
								"k":      min(10000, candidateLimit*16),
							},
						},
					},
				},
			},
		},
	}
	if len(filters) > 0 {
		nestedBool["filter"] = filters
	}
	nestedReq := map[string]any{
		"size":             candidateLimit,
		"_source":          []string{"record_raw"},
		"track_total_hits": false,
		"query":            map[string]any{"bool": nestedBool},
	}

	requests := []map[string]any{topReq, nestedReq}
	text := strings.TrimSpace(options.Text)
	if text != "" {
		requests = append(requests, map[string]any{
			"size":             candidateLimit,
			"_source":          []string{"record_raw"},
			"track_total_hits": false,
			"query":            buildBoolQuery(options, true),
			"sort":             sortClause(sessionstore.SortRelevance),
		})
	}

	var buf bytes.Buffer
	headerLine, _ := json.Marshal(map[string]any{"index": s.state.backing})
	for _, reqBody := range requests {
		bodyLine, err := json.Marshal(reqBody)
		if err != nil {
			return sessionstore.SearchResult{}, err
		}
		buf.Write(headerLine)
		buf.WriteByte('\n')
		buf.Write(bodyLine)
		buf.WriteByte('\n')
	}
	var msearchResp struct {
		Responses []searchHitEnvelope `json:"responses"`
	}
	if err := s.state.client.doNDJSON(ctx, "/_msearch", buf.Bytes(), &msearchResp); err != nil {
		return sessionstore.SearchResult{}, err
	}
	if len(msearchResp.Responses) < 2 {
		return sessionstore.SearchResult{}, errors.New("opensearchindex: incomplete _msearch response")
	}

	type vecCandidate struct {
		record     sessionstore.Record
		similarity float64
		weighted   float64
	}
	vecBest := make(map[string]vecCandidate, candidateLimit)
	weights := options.Vector.ProjectionWeights
	mergeVector := func(rec sessionstore.Record, sim float64, projID string) {
		var weighted float64
		if len(weights) == 0 {
			weighted = (sim + 1) / 2
		} else {
			weighted = max(0, sim) * sessionstore.ProjectionWeight(weights, projID)
		}
		if prev, ok := vecBest[rec.ID]; !ok || weighted > prev.weighted {
			vecBest[rec.ID] = vecCandidate{record: rec, similarity: sim, weighted: weighted}
		}
	}

	for _, h := range msearchResp.Responses[0].Hits.Hits {
		rec, err := decodeRecordRaw(h.Source.RecordRaw)
		if err != nil {
			return sessionstore.SearchResult{}, err
		}
		sim := min(1, max(-1, 2*h.Score-1))
		mergeVector(rec, sim, "$record")
	}
	for _, h := range msearchResp.Responses[1].Hits.Hits {
		rec, err := decodeRecordRaw(h.Source.RecordRaw)
		if err != nil {
			return sessionstore.SearchResult{}, err
		}
		inner := h.InnerHits["vector_projections"].Hits.Hits
		if len(inner) == 0 {
			sim := min(1, max(-1, 2*h.Score-1))
			mergeVector(rec, sim, "")
			continue
		}
		for _, ih := range inner {
			sim := min(1, max(-1, 2*ih.Score-1))
			mergeVector(rec, sim, ih.Source.ID)
		}
	}

	vecRanked := make([]vecCandidate, 0, len(vecBest))
	for _, vc := range vecBest {
		vecRanked = append(vecRanked, vc)
	}
	sort.Slice(vecRanked, func(i, j int) bool {
		if vecRanked[i].weighted != vecRanked[j].weighted {
			return vecRanked[i].weighted > vecRanked[j].weighted
		}
		return vecRanked[i].record.ID < vecRanked[j].record.ID
	})
	if len(vecRanked) > candidateLimit {
		vecRanked = vecRanked[:candidateLimit]
	}

	type fusedCandidate struct {
		record              sessionstore.Record
		textScore           float64
		vectorScore         float64
		weightedVectorScore float64
		hasVector           bool
		vectorRank          int
		baseScore           float64
	}
	candidates := make(map[string]*fusedCandidate, len(vecRanked)+candidateLimit)
	bestVectorScore := 0.0
	for idx, vr := range vecRanked {
		vectorScore := (vr.similarity + 1) / 2
		bestVectorScore = max(bestVectorScore, vectorScore)
		candidates[vr.record.ID] = &fusedCandidate{
			record:              vr.record,
			vectorScore:         vectorScore,
			weightedVectorScore: vr.weighted,
			hasVector:           true,
			vectorRank:          idx + 1,
		}
	}

	if text != "" && len(msearchResp.Responses) >= 3 {
		minTextScore := 0.0
		if gate := options.HybridTextGate; gate != nil && bestVectorScore >= gate.MinimumVectorScore {
			minTextScore = gate.MinimumTextScore
		}
		for rank, h := range msearchResp.Responses[2].Hits.Hits {
			if h.Score < minTextScore {
				break
			}
			c := candidates[h.ID]
			if c == nil {
				rec, err := decodeRecordRaw(h.Source.RecordRaw)
				if err != nil {
					return sessionstore.SearchResult{}, err
				}
				c = &fusedCandidate{record: rec}
				candidates[h.ID] = c
			}
			c.textScore = h.Score
			c.baseScore += 1 / float64(60+rank+1)
		}
		weight := options.Vector.Weight
		for _, c := range candidates {
			if c.hasVector {
				c.baseScore += weight / float64(60+c.vectorRank)
			}
		}
	} else {
		for _, c := range candidates {
			c.baseScore = c.weightedVectorScore
		}
	}

	hits := make([]sessionstore.Hit, 0, len(candidates))
	for _, c := range candidates {
		hits = append(hits, sessionstore.Hit{
			Record:      c.record,
			Score:       c.baseScore,
			BaseScore:   c.baseScore,
			TextScore:   c.textScore,
			VectorScore: c.vectorScore,
		})
	}
	sort.SliceStable(hits, func(left, right int) bool {
		if hits[left].Score != hits[right].Score {
			return hits[left].Score > hits[right].Score
		}
		if !hits[left].Record.CreatedAt.Equal(hits[right].Record.CreatedAt) {
			return hits[left].Record.CreatedAt.After(hits[right].Record.CreatedAt)
		}
		return hits[left].Record.ID < hits[right].Record.ID
	})
	if options.Recency != nil {
		sessionstore.RerankByRecency(hits, *options.Recency)
	}
	hits = sessionstore.OmitToolCallMessage(hits, options.ExcludeMessage)
	total := uint64(len(hits))
	start := min(options.Offset, len(hits))
	end := min(start+options.Limit, len(hits))
	return sessionstore.SearchResult{Total: total, Hits: hits[start:end]}, nil
}
