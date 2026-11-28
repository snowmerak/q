// Package blevehnsw implements storage.SearchIndex combining bleve/v2 (BM25)
// and goformersearch (HNSW) under a single-instance generational directory.
package blevehnsw

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	goformersearch "github.com/MichaelAyles/goformersearch"
	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"

	"github.com/snowmerak/q/v2/internal/fsutil"
	"github.com/snowmerak/q/v2/pkg/storage"
)

const (
	defaultHNSWM          = 16
	defaultEfConstruction = 200
	defaultEfSearch       = 64
)

type currentManifest struct {
	Generation  string `json:"generation"`
	VectorModel string `json:"vector_model,omitempty"`
	VectorDim   int    `json:"vector_dim,omitempty"`
}

type generationState struct {
	Version     int    `json:"version"`
	VectorModel string `json:"vector_model,omitempty"`
	VectorDim   int    `json:"vector_dim,omitempty"`
	DocCount    uint64 `json:"doc_count"`
	VectorCount int    `json:"vector_count"`
	UpdatedAtMS int64  `json:"updated_at_ms"`
}

type storedVector struct {
	Key        string    `json:"key"`
	ID         string    `json:"id"`
	EntityType string    `json:"entity_type"`
	Scope      string    `json:"scope"`
	OwnerID    string    `json:"owner_id"`
	ObservedBy []string  `json:"observed_by,omitempty"`
	OccurredAt int64     `json:"occurred_at"`
	Revision   int64     `json:"revision"`
	Vector     []float32 `json:"vector"`
}

type vectorIDMap struct {
	NextID  uint64            `json:"next_id"`
	KeyToID map[string]uint64 `json:"key_to_id"`
}

type bleveDoc struct {
	ID         string   `json:"id"`
	EntityType string   `json:"entity_type"`
	Scope      string   `json:"scope"`
	OwnerID    string   `json:"owner_id"`
	ObservedBy []string `json:"observed_by"`
	Text       string   `json:"text"`
	OccurredAt int64    `json:"occurred_at"`
	Revision   int64    `json:"revision"`
}

type Index struct {
	mu       sync.RWMutex
	root     string
	gen      string
	genDir   string
	bleveIdx bleve.Index

	vecModel   string
	vecDim     int
	graph      *goformersearch.HNSWIndex
	appendable bool
	vectors    map[string]storedVector
	idToKey    map[uint64]string
	keyToID    map[string]uint64
	nextVecID  uint64
	closed     bool
}

var _ storage.SearchIndex = (*Index)(nil)

func docKey(entityType, id string) string {
	return entityType + ":" + id
}

func buildIndexMapping() mapping.IndexMapping {
	im := bleve.NewIndexMapping()
	doc := bleve.NewDocumentMapping()

	kw := bleve.NewTextFieldMapping()
	kw.Analyzer = "keyword"
	kw.Store = false

	txt := bleve.NewTextFieldMapping()
	txt.Store = false

	num := bleve.NewNumericFieldMapping()
	num.Store = false

	doc.AddFieldMappingsAt("id", kw)
	doc.AddFieldMappingsAt("entity_type", kw)
	doc.AddFieldMappingsAt("scope", kw)
	doc.AddFieldMappingsAt("owner_id", kw)
	doc.AddFieldMappingsAt("observed_by", kw)
	doc.AddFieldMappingsAt("text", txt)
	doc.AddFieldMappingsAt("occurred_at", num)
	doc.AddFieldMappingsAt("revision", num)

	im.DefaultMapping = doc
	return im
}

// Open initializes or reopens a generational Bleve + HNSW index under root.
func Open(root string) (*Index, error) {
	abs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return nil, fmt.Errorf("%w: resolve index root: %v", storage.ErrInvalidInput, err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "generations"), 0o755); err != nil {
		return nil, fmt.Errorf("create index root: %w", err)
	}

	manifest, err := readManifest(abs)
	if err != nil {
		manifest = currentManifest{Generation: "gen-1"}
		if writeErr := writeManifest(abs, manifest); writeErr != nil {
			return nil, writeErr
		}
	}
	if manifest.Generation == "" {
		manifest.Generation = "gen-1"
	}

	idx := &Index{
		root:      abs,
		gen:       manifest.Generation,
		genDir:    filepath.Join(abs, "generations", manifest.Generation),
		vecModel:  manifest.VectorModel,
		vecDim:    manifest.VectorDim,
		vectors:   make(map[string]storedVector),
		idToKey:   make(map[uint64]string),
		keyToID:   make(map[string]uint64),
		nextVecID: 1,
	}
	if err := idx.openGeneration(); err != nil {
		return nil, err
	}
	return idx, nil
}

func (idx *Index) openGeneration() error {
	if err := os.MkdirAll(idx.genDir, 0o755); err != nil {
		return err
	}
	blevePath := filepath.Join(idx.genDir, "bleve")
	var bIdx bleve.Index
	var err error
	if _, statErr := os.Stat(blevePath); statErr == nil {
		bIdx, err = bleve.Open(blevePath)
	} else {
		bIdx, err = bleve.New(blevePath, buildIndexMapping())
	}
	if err != nil {
		return fmt.Errorf("open bleve: %w", err)
	}
	idx.bleveIdx = bIdx

	if err := idx.loadVectorsFromDisk(); err != nil {
		_ = idx.bleveIdx.Close()
		idx.bleveIdx = nil
		return err
	}
	return nil
}

func (idx *Index) loadVectorsFromDisk() error {
	vecsPath := filepath.Join(idx.genDir, "vectors.json")
	if body, err := os.ReadFile(vecsPath); err == nil {
		var entries []storedVector
		if err := json.Unmarshal(body, &entries); err != nil {
			return fmt.Errorf("decode vectors.json: %w", err)
		}
		for _, e := range entries {
			idx.vectors[e.Key] = e
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if idx.vecDim <= 0 {
		return nil
	}

	hnswPath := filepath.Join(idx.genDir, "vectors.hnsw")
	idsPath := filepath.Join(idx.genDir, "vectors.ids.json")
	if f, err := os.Open(hnswPath); err == nil {
		graph, loadErr := goformersearch.LoadHNSW(bufio.NewReader(f))
		_ = f.Close()
		if loadErr == nil && graph.Dims() == idx.vecDim {
			if idBody, idErr := os.ReadFile(idsPath); idErr == nil {
				var idMap vectorIDMap
				if json.Unmarshal(idBody, &idMap) == nil && len(idMap.KeyToID) == graph.Len() {
					graph.SetEfSearch(defaultEfSearch)
					idx.graph = graph
					idx.keyToID = idMap.KeyToID
					idx.idToKey = make(map[uint64]string, len(idMap.KeyToID))
					for k, id := range idMap.KeyToID {
						idx.idToKey[id] = k
					}
					idx.nextVecID = max(idMap.NextID, 1)
					// Loaded goformersearch v0.1.0 graphs have a nil internal RNG and cannot accept Add.
					idx.appendable = false
					return nil
				}
			}
		}
	}

	return idx.rebuildGraphFromMemoryLocked()
}

func (idx *Index) rebuildGraphFromMemoryLocked() error {
	if idx.vecDim <= 0 {
		idx.graph = nil
		idx.appendable = false
		idx.idToKey = make(map[uint64]string)
		idx.keyToID = make(map[string]uint64)
		idx.nextVecID = 1
		return nil
	}
	graph := goformersearch.NewHNSWIndex(
		idx.vecDim,
		goformersearch.WithM(defaultHNSWM),
		goformersearch.WithEfConstruction(defaultEfConstruction),
		goformersearch.WithEfSearch(defaultEfSearch),
	)
	idx.idToKey = make(map[uint64]string, len(idx.vectors))
	idx.keyToID = make(map[string]uint64, len(idx.vectors))
	idx.nextVecID = 1

	keys := make([]string, 0, len(idx.vectors))
	for k := range idx.vectors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sv := idx.vectors[k]
		if len(sv.Vector) != idx.vecDim || !isFiniteNonZero(sv.Vector) {
			continue
		}
		id := idx.nextVecID
		idx.nextVecID++
		graph.Add(id, normalize(sv.Vector))
		idx.idToKey[id] = k
		idx.keyToID[k] = id
	}
	idx.graph = graph
	idx.appendable = true
	return nil
}

func (idx *Index) persistVectorsLocked() error {
	entries := make([]storedVector, 0, len(idx.vectors))
	for _, sv := range idx.vectors {
		entries = append(entries, sv)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	vecJSON, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(idx.genDir, "vectors.json"), vecJSON, 0o600); err != nil {
		return err
	}

	if idx.graph != nil && idx.vecDim > 0 {
		hnswPath := filepath.Join(idx.genDir, "vectors.hnsw")
		tmp, err := os.CreateTemp(idx.genDir, ".vectors-*")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		bw := bufio.NewWriter(tmp)
		saveErr := goformersearch.Save(bw, idx.graph)
		flushErr := bw.Flush()
		closeErr := tmp.Close()
		if err := errors.Join(saveErr, flushErr, closeErr); err != nil {
			_ = os.Remove(tmpName)
			return err
		}
		if err := os.Rename(tmpName, hnswPath); err != nil {
			_ = os.Remove(tmpName)
			return err
		}
		idMapBody, err := json.Marshal(vectorIDMap{NextID: idx.nextVecID, KeyToID: idx.keyToID})
		if err != nil {
			return err
		}
		if err := fsutil.WriteFileAtomic(filepath.Join(idx.genDir, "vectors.ids.json"), idMapBody, 0o600); err != nil {
			return err
		}
	}

	docCount, _ := idx.bleveIdx.DocCount()
	stateBody, err := json.MarshalIndent(generationState{
		Version:     1,
		VectorModel: idx.vecModel,
		VectorDim:   idx.vecDim,
		DocCount:    docCount,
		VectorCount: len(idx.keyToID),
		UpdatedAtMS: time.Now().UTC().UnixMilli(),
	}, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(idx.genDir, "state.json"), append(stateBody, '\n'), 0o600)
}

func (idx *Index) ConfigureVector(ctx context.Context, model string, dim int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if model == "" && dim == 0 {
		return nil
	}
	if model == "" || dim <= 0 {
		return fmt.Errorf("%w: vector model and positive dimension are required", storage.ErrInvalidInput)
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return storage.ErrUnavailable
	}
	if idx.vecModel == model && idx.vecDim == dim {
		return nil
	}
	if idx.vecDim != dim {
		// Dimension changed; drop incompatible vectors until reindexed.
		idx.vectors = make(map[string]storedVector)
	}
	idx.vecModel = model
	idx.vecDim = dim
	if err := idx.rebuildGraphFromMemoryLocked(); err != nil {
		return err
	}
	if err := idx.persistVectorsLocked(); err != nil {
		return err
	}
	return writeManifest(idx.root, currentManifest{
		Generation:  idx.gen,
		VectorModel: idx.vecModel,
		VectorDim:   idx.vecDim,
	})
}

func (idx *Index) Upsert(ctx context.Context, doc storage.SearchDocument) error {
	return idx.UpsertBatch(ctx, []storage.SearchDocument{doc})
}

func (idx *Index) UpsertBatch(ctx context.Context, docs []storage.SearchDocument) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(docs) == 0 {
		return nil
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return storage.ErrUnavailable
	}

	batch := idx.bleveIdx.NewBatch()
	needRebuild := false
	hasVecChanges := false

	for _, doc := range docs {
		if strings.TrimSpace(doc.ID) == "" || strings.TrimSpace(doc.EntityType) == "" {
			return fmt.Errorf("%w: entity_type and id are required", storage.ErrInvalidInput)
		}
		key := docKey(doc.EntityType, doc.ID)
		if err := batch.Index(key, bleveDoc{
			ID:         doc.ID,
			EntityType: doc.EntityType,
			Scope:      doc.Scope,
			OwnerID:    doc.OwnerID,
			ObservedBy: append([]string(nil), doc.ObservedBy...),
			Text:       doc.Text,
			OccurredAt: doc.OccurredAt,
			Revision:   doc.Revision,
		}); err != nil {
			return err
		}

		if len(doc.Vector) > 0 {
			if idx.vecDim > 0 && len(doc.Vector) != idx.vecDim {
				return fmt.Errorf("%w: vector dimension %d does not match configured %d", storage.ErrInvalidInput, len(doc.Vector), idx.vecDim)
			}
			if !isFiniteNonZero(doc.Vector) {
				return fmt.Errorf("%w: invalid zero or non-finite vector", storage.ErrInvalidInput)
			}
			if idx.vecDim == 0 {
				idx.vecDim = len(doc.Vector)
			}
			if _, existed := idx.keyToID[key]; existed || !idx.appendable {
				needRebuild = true
			}
			idx.vectors[key] = storedVector{
				Key:        key,
				ID:         doc.ID,
				EntityType: doc.EntityType,
				Scope:      doc.Scope,
				OwnerID:    doc.OwnerID,
				ObservedBy: append([]string(nil), doc.ObservedBy...),
				OccurredAt: doc.OccurredAt,
				Revision:   doc.Revision,
				Vector:     append([]float32(nil), doc.Vector...),
			}
			hasVecChanges = true
		} else if _, existed := idx.vectors[key]; existed {
			delete(idx.vectors, key)
			needRebuild = true
			hasVecChanges = true
		}
	}

	if err := idx.bleveIdx.Batch(batch); err != nil {
		return err
	}
	if !hasVecChanges {
		return nil
	}
	if needRebuild || idx.graph == nil {
		if err := idx.rebuildGraphFromMemoryLocked(); err != nil {
			return err
		}
	} else {
		for _, doc := range docs {
			if len(doc.Vector) == 0 {
				continue
			}
			key := docKey(doc.EntityType, doc.ID)
			id := idx.nextVecID
			idx.nextVecID++
			idx.graph.Add(id, normalize(doc.Vector))
			idx.idToKey[id] = key
			idx.keyToID[key] = id
		}
	}
	return idx.persistVectorsLocked()
}

func (idx *Index) Delete(ctx context.Context, entityType, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := docKey(entityType, id)

	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return storage.ErrUnavailable
	}
	if err := idx.bleveIdx.Delete(key); err != nil {
		return err
	}
	if _, existed := idx.vectors[key]; existed {
		delete(idx.vectors, key)
		if err := idx.rebuildGraphFromMemoryLocked(); err != nil {
			return err
		}
		return idx.persistVectorsLocked()
	}
	return nil
}

func (idx *Index) Search(ctx context.Context, q storage.SearchQuery) ([]storage.SearchHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mode := strings.ToLower(strings.TrimSpace(q.Mode))
	if mode == "" {
		mode = "hybrid"
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 16
	}
	keywordWeight := q.KeywordWeight
	if keywordWeight <= 0 || keywordWeight > 1 {
		keywordWeight = 0.5
	}
	minDecay := q.MinDecay
	if minDecay <= 0 || minDecay > 1 {
		minDecay = 0.2
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if idx.closed {
		return nil, storage.ErrUnavailable
	}

	vectorReady := idx.graph != nil && idx.vecDim > 0 && len(q.Vector) > 0
	if len(q.Vector) > 0 && idx.vecDim > 0 && len(q.Vector) != idx.vecDim {
		return nil, fmt.Errorf("%w: query vector dimension %d != %d", storage.ErrInvalidInput, len(q.Vector), idx.vecDim)
	}
	if mode == "semantic" && !vectorReady {
		return nil, fmt.Errorf("%w: semantic search requires configured vectors and query embedding", storage.ErrUnavailable)
	}
	if mode == "hybrid" && !vectorReady {
		mode = "lexical"
	}

	type candidate struct {
		id         string
		entityType string
		lexical    float64
		semantic   float64
		occurredAt int64
		revision   int64
	}
	candidates := make(map[string]*candidate)

	if mode == "lexical" || mode == "hybrid" {
		bleveQ := buildBleveQuery(q)
		req := bleve.NewSearchRequestOptions(bleveQ, limit*4, 0, false)
		res, err := idx.bleveIdx.SearchInContext(ctx, req)
		if err != nil {
			return nil, err
		}
		maxScore := 0.0
		for _, hit := range res.Hits {
			if hit.Score > maxScore {
				maxScore = hit.Score
			}
		}
		for _, hit := range res.Hits {
			entityType, docID, ok := strings.Cut(hit.ID, ":")
			if !ok {
				continue
			}
			normScore := 1.0
			if maxScore > 0 {
				normScore = hit.Score / maxScore
			}
			c := &candidate{id: docID, entityType: entityType, lexical: normScore}
			if sv, hasVec := idx.vectors[hit.ID]; hasVec {
				c.occurredAt = sv.OccurredAt
				c.revision = sv.Revision
			}
			candidates[hit.ID] = c
		}
	}

	if mode == "semantic" || mode == "hybrid" {
		if !isFiniteNonZero(q.Vector) {
			return nil, fmt.Errorf("%w: query vector must be finite and non-zero", storage.ErrInvalidInput)
		}
		if idx.graph.Len() > 0 {
			results := idx.graph.Search(normalize(q.Vector), idx.graph.Len())
			for _, res := range results {
				key, ok := idx.idToKey[res.ID]
				if !ok {
					continue
				}
				sv, ok := idx.vectors[key]
				if !ok || !matchesFilters(sv, q) {
					continue
				}
				sim := math.Max(0, float64(res.Similarity))
				c, exists := candidates[key]
				if !exists {
					c = &candidate{
						id:         sv.ID,
						entityType: sv.EntityType,
						occurredAt: sv.OccurredAt,
						revision:   sv.Revision,
					}
					candidates[key] = c
				}
				c.semantic = sim
			}
		}
	}

	var maxOccurred int64
	for _, c := range candidates {
		if c.occurredAt > maxOccurred {
			maxOccurred = c.occurredAt
		}
	}

	hits := make([]storage.SearchHit, 0, len(candidates))
	for _, c := range candidates {
		var combined float64
		switch mode {
		case "lexical":
			combined = c.lexical
		case "semantic":
			combined = c.semantic
		default:
			combined = keywordWeight*c.lexical + (1-keywordWeight)*c.semantic
		}
		decay := 1.0
		if q.HalfLifeTurns > 0 && maxOccurred > 0 && c.occurredAt > 0 {
			ageUnits := float64(maxOccurred-c.occurredAt) / float64(3_600_000)
			if ageUnits > 0 {
				decay = math.Max(minDecay, math.Pow(0.5, ageUnits/float64(q.HalfLifeTurns)))
			}
		}
		hits = append(hits, storage.SearchHit{
			ID:         c.id,
			EntityType: c.entityType,
			Score:      combined * decay,
			Revision:   c.revision,
		})
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func buildBleveQuery(q storage.SearchQuery) query.Query {
	var must []query.Query
	if strings.TrimSpace(q.Text) != "" {
		mq := bleve.NewMatchQuery(q.Text)
		mq.SetField("text")
		must = append(must, mq)
	} else {
		must = append(must, bleve.NewMatchAllQuery())
	}
	if len(q.EntityTypes) > 0 {
		should := make([]query.Query, 0, len(q.EntityTypes))
		for _, et := range q.EntityTypes {
			tq := bleve.NewTermQuery(et)
			tq.SetField("entity_type")
			should = append(should, tq)
		}
		must = append(must, bleve.NewDisjunctionQuery(should...))
	}
	if len(q.Scopes) > 0 {
		should := make([]query.Query, 0, len(q.Scopes))
		for _, sc := range q.Scopes {
			tq := bleve.NewTermQuery(sc)
			tq.SetField("scope")
			should = append(should, tq)
		}
		must = append(must, bleve.NewDisjunctionQuery(should...))
	}
	if strings.TrimSpace(q.ObserverID) != "" {
		tq := bleve.NewTermQuery(q.ObserverID)
		tq.SetField("observed_by")
		must = append(must, tq)
	}
	return bleve.NewConjunctionQuery(must...)
}

func matchesFilters(sv storedVector, q storage.SearchQuery) bool {
	if len(q.EntityTypes) > 0 && !slices.Contains(q.EntityTypes, sv.EntityType) {
		return false
	}
	if len(q.Scopes) > 0 && !slices.Contains(q.Scopes, sv.Scope) {
		return false
	}
	if q.ObserverID != "" && !slices.Contains(sv.ObservedBy, q.ObserverID) {
		return false
	}
	return true
}

func (idx *Index) ResetAndRebuild(ctx context.Context, docs <-chan storage.SearchDocument) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return storage.ErrUnavailable
	}

	nextGen := fmt.Sprintf("gen-%d", time.Now().UTC().UnixNano())
	nextDir := filepath.Join(idx.root, "generations", nextGen)
	nextIdx := &Index{
		root:      idx.root,
		gen:       nextGen,
		genDir:    nextDir,
		vecModel:  idx.vecModel,
		vecDim:    idx.vecDim,
		vectors:   make(map[string]storedVector),
		idToKey:   make(map[uint64]string),
		keyToID:   make(map[string]uint64),
		nextVecID: 1,
	}
	if err := nextIdx.openGeneration(); err != nil {
		_ = os.RemoveAll(nextDir)
		return err
	}

	var batch []storage.SearchDocument
	for doc := range docs {
		if err := ctx.Err(); err != nil {
			_ = nextIdx.bleveIdx.Close()
			_ = os.RemoveAll(nextDir)
			return err
		}
		batch = append(batch, doc)
		if len(batch) >= 64 {
			if err := nextIdx.UpsertBatch(ctx, batch); err != nil {
				_ = nextIdx.bleveIdx.Close()
				_ = os.RemoveAll(nextDir)
				return err
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := nextIdx.UpsertBatch(ctx, batch); err != nil {
			_ = nextIdx.bleveIdx.Close()
			_ = os.RemoveAll(nextDir)
			return err
		}
	}
	if err := nextIdx.persistVectorsLocked(); err != nil {
		_ = nextIdx.bleveIdx.Close()
		_ = os.RemoveAll(nextDir)
		return err
	}
	if err := writeManifest(idx.root, currentManifest{
		Generation:  nextGen,
		VectorModel: nextIdx.vecModel,
		VectorDim:   nextIdx.vecDim,
	}); err != nil {
		_ = nextIdx.bleveIdx.Close()
		_ = os.RemoveAll(nextDir)
		return err
	}

	oldBleve := idx.bleveIdx
	oldDir := idx.genDir
	idx.gen = nextIdx.gen
	idx.genDir = nextIdx.genDir
	idx.bleveIdx = nextIdx.bleveIdx
	idx.vecModel = nextIdx.vecModel
	idx.vecDim = nextIdx.vecDim
	idx.graph = nextIdx.graph
	idx.appendable = nextIdx.appendable
	idx.vectors = nextIdx.vectors
	idx.idToKey = nextIdx.idToKey
	idx.keyToID = nextIdx.keyToID
	idx.nextVecID = nextIdx.nextVecID

	if oldBleve != nil {
		_ = oldBleve.Close()
	}
	if oldDir != "" && oldDir != nextDir {
		_ = os.RemoveAll(oldDir)
	}
	return nil
}

func (idx *Index) Counts(_ context.Context) (uint64, int, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if idx.closed || idx.bleveIdx == nil {
		return 0, 0, storage.ErrUnavailable
	}
	docs, err := idx.bleveIdx.DocCount()
	if err != nil {
		return 0, 0, err
	}
	return docs, len(idx.keyToID), nil
}

func (idx *Index) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return nil
	}
	idx.closed = true
	if idx.bleveIdx != nil {
		err := idx.bleveIdx.Close()
		idx.bleveIdx = nil
		return err
	}
	return nil
}

func readManifest(root string) (currentManifest, error) {
	data, err := os.ReadFile(filepath.Join(root, "current.json"))
	if err != nil {
		return currentManifest{}, err
	}
	var m currentManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return currentManifest{}, err
	}
	return m, nil
}

func writeManifest(root string, m currentManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(root, "current.json"), append(data, '\n'), 0o600)
}

func isFiniteNonZero(vec []float32) bool {
	if len(vec) == 0 {
		return false
	}
	var sq float64
	for _, v := range vec {
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return false
		}
		sq += f * f
	}
	return sq > 0
}

func normalize(vec []float32) []float32 {
	out := make([]float32, len(vec))
	var sq float64
	for _, v := range vec {
		sq += float64(v) * float64(v)
	}
	norm := math.Sqrt(sq)
	for i, v := range vec {
		out[i] = float32(float64(v) / norm)
	}
	return out
}
