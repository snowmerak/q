// Package archiveembed connects workspace Session Store records to a
// configured embedding provider. The Session Store remains the source of
// truth; its HNSW graph is always rebuildable from these record embeddings.
package archiveembed

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/embedding"
	"github.com/snowmerak/q/sessionstore"
)

const (
	maximumEmbeddingRunes = 16000
	backfillPageSize      = 100
)

var historyKinds = []string{
	sessionstore.KindMessage,
	sessionstore.KindTask,
	sessionstore.KindEvent,
	sessionstore.KindResult,
	sessionstore.KindQuestion,
	sessionstore.KindArtifact,
	sessionstore.KindSummary,
}

var semanticKinds = append(append([]string(nil), historyKinds...), sessionstore.KindSkill)

// Store is the workspace archive boundary used by semantic retrieval. The
// local sessionstore.Store and a remote workspace-memory client can both
// implement it. Archive embeds the interface so record operations remain
// available to Agent Skill synchronization and archive tools.
type Store interface {
	ConfigureVector(sessionstore.VectorConfig) error
	Save(sessionstore.Record) (sessionstore.Record, error)
	SaveBatch([]sessionstore.Record) ([]sessionstore.Record, error)
	ApplyEmbeddings([]sessionstore.EmbeddingUpdate) (sessionstore.EmbeddingUpdateResult, error)
	Get(string) (sessionstore.Record, error)
	Delete(string) error
	Search(context.Context, sessionstore.SearchOptions) (sessionstore.SearchResult, error)
}

// Archive adds semantic query and embedding preparation behavior to a Store.
// Save, Delete, and Get are promoted from the embedded Store so project Agent
// Skill synchronization continues to use the same archive.
type Archive struct {
	Store

	mu            sync.RWMutex
	vectorizer    *embedding.Vectorizer
	provider      embedding.Provider
	resolveVector func(context.Context) (sessionstore.VectorConfig, error)
	reconcile     chan struct{}
}

type BackfillStats struct {
	Scanned  int
	Embedded int
}

func New(store Store) *Archive {
	return &Archive{Store: store, reconcile: make(chan struct{}, 1)}
}

// Configure enables record and query embedding and switches the Store's
// rebuildable vector graph to the selected model.
func (a *Archive) Configure(provider embedding.Provider, model string, dimensions int) error {
	return a.ConfigureContext(context.Background(), provider, model, dimensions)
}

// ConfigureContext forwards the operation's cancellation and deadline to stores
// that support contextual vector configuration, including remote archives.
func (a *Archive) ConfigureContext(ctx context.Context, provider embedding.Provider, model string, dimensions int) error {
	if a == nil || a.Store == nil {
		return errors.New("archive embedding: store is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	configure := func(config sessionstore.VectorConfig) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if contextual, ok := a.Store.(interface {
			ConfigureVectorContext(context.Context, sessionstore.VectorConfig) error
		}); ok {
			return contextual.ConfigureVectorContext(ctx, config)
		}
		return a.ConfigureVector(config)
	}
	if model == "" && dimensions == 0 {
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := configure(sessionstore.VectorConfig{}); err != nil {
			return err
		}
		a.provider, a.vectorizer = provider, nil
		return nil
	}
	vectorizer, err := embedding.New(provider, model, dimensions)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := configure(sessionstore.VectorConfig{
		Model: vectorizer.Model(), Dimensions: vectorizer.Dimensions(),
	}); err != nil {
		return fmt.Errorf("archive embedding: configure vector index: %w", err)
	}
	a.vectorizer = vectorizer
	a.provider = provider
	return nil
}

// SetVectorConfigResolver lets a running archive follow its owner's latest
// settings on use without depending on a settings screen or a catalog.
func (a *Archive) SetVectorConfigResolver(resolve func(context.Context) (sessionstore.VectorConfig, error)) {
	a.mu.Lock()
	a.resolveVector = resolve
	a.mu.Unlock()
}

func (a *Archive) refreshConfiguration(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.RLock()
	resolve := a.resolveVector
	a.mu.RUnlock()
	if resolve == nil {
		return nil
	}
	wanted, err := resolve(ctx)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.vectorizer == nil && !wanted.Enabled() {
		return nil
	}
	if a.vectorizer != nil && a.vectorizer.Model() == wanted.Model && a.vectorizer.Dimensions() == wanted.Dimensions {
		return nil
	}
	var vectorizer *embedding.Vectorizer
	if wanted.Enabled() {
		vectorizer, err = embedding.New(a.provider, wanted.Model, wanted.Dimensions)
		if err != nil {
			return err
		}
	}
	if contextual, ok := a.Store.(interface {
		ConfigureVectorContext(context.Context, sessionstore.VectorConfig) error
	}); ok {
		err = contextual.ConfigureVectorContext(ctx, wanted)
	} else {
		err = a.ConfigureVector(wanted)
	}
	if err != nil {
		return err
	}
	a.vectorizer = vectorizer
	return nil
}

// Disable returns archive retrieval to BM25/metadata-only behavior.
func (a *Archive) Disable() error {
	if a == nil || a.Store == nil {
		return errors.New("archive embedding: store is unavailable")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.ConfigureVector(sessionstore.VectorConfig{}); err != nil {
		return fmt.Errorf("archive embedding: disable vector index: %w", err)
	}
	a.vectorizer = nil
	return nil
}

// Prepare embeds eligible history records in one provider request. It is
// suitable for sessionstore.WriterOptions.Prepare.
func (a *Archive) Prepare(ctx context.Context, records []sessionstore.Record) ([]sessionstore.Record, error) {
	if a == nil {
		return records, nil
	}
	if err := a.refreshConfiguration(ctx); err != nil {
		return records, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return prepare(ctx, records, a.vectorizer)
}

func prepare(ctx context.Context, records []sessionstore.Record, vectorizer *embedding.Vectorizer) ([]sessionstore.Record, error) {
	if vectorizer == nil {
		return records, nil
	}
	type pendingPart struct {
		position int
		id       string
		skill    bool
	}
	pending := make([]pendingPart, 0, len(records))
	texts := make([]string, 0, len(records))
	for index := range records {
		if !isSemanticRecord(records[index]) {
			continue
		}
		if records[index].Kind == sessionstore.KindSkill {
			parts, err := agentskills.EmbeddingParts(records[index].Summary, records[index].Content, records[index].Tags)
			if err != nil {
				return records, fmt.Errorf("archive embedding: skill %q: %w", records[index].Summary, err)
			}
			if agentskills.EmbeddingPartsMatch(records[index], parts, vectorizer.Model(), vectorizer.Dimensions()) {
				continue
			}
			for _, part := range parts {
				pending = append(pending, pendingPart{position: index, id: part.ID, skill: true})
				texts = append(texts, part.Text)
			}
			continue
		}
		if embeddingMatches(records[index].Embedding, vectorizer) {
			continue
		}
		text := recordText(records[index])
		if text == "" {
			continue
		}
		pending = append(pending, pendingPart{position: index})
		texts = append(texts, text)
	}
	if len(texts) == 0 {
		return records, nil
	}
	vectors := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += 32 {
		end := min(start+32, len(texts))
		chunk, err := vectorizer.Embed(ctx, texts[start:end])
		if err != nil {
			return records, fmt.Errorf("archive embedding: embed records: %w", err)
		}
		vectors = append(vectors, chunk...)
	}
	createdAt := time.Now().UTC()
	initializedSkills := make(map[int]bool)
	for index, part := range pending {
		embedded := sessionstore.Embedding{
			Model: vectorizer.Model(), Dimensions: vectorizer.Dimensions(),
			CreatedAt: createdAt, Vector: vectors[index],
		}
		if part.skill {
			if !initializedSkills[part.position] {
				records[part.position].Embedding = nil
				records[part.position].VectorProjections = nil
				initializedSkills[part.position] = true
			}
			records[part.position].VectorProjections = append(records[part.position].VectorProjections,
				sessionstore.VectorProjection{ID: part.id, Embedding: embedded})
		} else {
			records[part.position].Embedding = &embedded
		}
	}
	return records, nil
}

// Search repairs missing or outdated embeddings before semantic retrieval,
// including records imported after an earlier successful backfill. Failed
// repairs are returned and retried on the next search, rather than marking an
// incomplete index as ready.
// It automatically adds a vector query to relevance searches with text.
// Filter-only and explicitly chronological searches remain lexical/metadata
// operations and therefore do not call the embedding provider.
func (a *Archive) Search(ctx context.Context, options sessionstore.SearchOptions) (sessionstore.SearchResult, error) {
	if a == nil || a.Store == nil {
		return sessionstore.SearchResult{}, errors.New("archive embedding: store is unavailable")
	}
	if err := a.refreshConfiguration(ctx); err != nil {
		return sessionstore.SearchResult{}, err
	}
	semantic := options.Vector != nil || (strings.TrimSpace(options.Text) != "" &&
		(options.Sort == "" || options.Sort == sessionstore.SortRelevance) && searchesSemanticRecords(options.Filters.Kinds))
	if semantic {
		if err := a.lockReconciliation(ctx); err != nil {
			return sessionstore.SearchResult{}, err
		}
		defer a.unlockReconciliation()
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	vectorizer := a.vectorizer
	if vectorizer != nil && semantic {
		if _, err := a.backfill(ctx, vectorizer); err != nil {
			return sessionstore.SearchResult{}, fmt.Errorf("archive embedding: repair before search: %w", err)
		}
	}
	if vectorizer != nil && semantic && options.Vector == nil {
		vectors, err := vectorizer.Embed(ctx, []string{strings.TrimSpace(options.Text)})
		if err != nil {
			return sessionstore.SearchResult{}, fmt.Errorf("archive embedding: embed search query: %w", err)
		}
		if slices.Contains(options.Filters.Kinds, sessionstore.KindSkill) {
			options.Vector = agentskills.SkillVectorQuery(vectors[0])
		} else {
			options.Vector = &sessionstore.VectorQuery{Embedding: vectors[0]}
		}
	}
	return a.Store.Search(ctx, options)
}

func searchesSemanticRecords(kinds []string) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, wanted := range kinds {
		if slices.Contains(semanticKinds, wanted) {
			return true
		}
	}
	return false
}

// Backfill embeds existing history records whose embedding is missing or uses
// a different model/dimension. Each page is persisted as one vector rebuild.
func (a *Archive) Backfill(ctx context.Context) (BackfillStats, error) {
	if a == nil || a.Store == nil {
		return BackfillStats{}, errors.New("archive embedding: store is unavailable")
	}
	if err := a.refreshConfiguration(ctx); err != nil {
		return BackfillStats{}, err
	}
	if err := a.lockReconciliation(ctx); err != nil {
		return BackfillStats{}, err
	}
	defer a.unlockReconciliation()
	// Keep model assignment from changing halfway through a migration.
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.backfill(ctx, a.vectorizer)
}

func (a *Archive) backfill(ctx context.Context, vectorizer *embedding.Vectorizer) (BackfillStats, error) {
	if vectorizer == nil {
		return BackfillStats{}, nil
	}
	// The source records and the derived graph can drift independently.
	if store, ok := a.Store.(interface {
		VectorConfig() sessionstore.VectorConfig
	}); ok {
		current := store.VectorConfig()
		if current.Model != vectorizer.Model() || current.Dimensions != vectorizer.Dimensions() {
			wanted := sessionstore.VectorConfig{Model: vectorizer.Model(), Dimensions: vectorizer.Dimensions()}
			var err error
			if contextual, ok := a.Store.(interface {
				ConfigureVectorContext(context.Context, sessionstore.VectorConfig) error
			}); ok {
				err = contextual.ConfigureVectorContext(ctx, wanted)
			} else {
				err = a.ConfigureVector(wanted)
			}
			if err != nil {
				return BackfillStats{}, fmt.Errorf("archive embedding: repair vector index: %w", err)
			}
		}
	}
	stats := BackfillStats{}
	retries := 0
	for offset := 0; ; offset += backfillPageSize {
		result, err := a.Store.Search(ctx, sessionstore.SearchOptions{
			Filters: sessionstore.Filters{Kinds: semanticKinds},
			Sort:    sessionstore.SortOldest, Limit: backfillPageSize, Offset: offset,
		})
		if err != nil {
			return stats, fmt.Errorf("archive embedding: scan records: %w", err)
		}
		if len(result.Hits) == 0 {
			return stats, nil
		}
		stats.Scanned += len(result.Hits)
		records := make([]sessionstore.Record, len(result.Hits))
		for index, hit := range result.Hits {
			records[index] = hit.Record
		}
		prepared, err := prepare(ctx, append([]sessionstore.Record(nil), records...), vectorizer)
		if err != nil {
			return stats, err
		}
		changed := make([]sessionstore.EmbeddingUpdate, 0, len(prepared))
		for index, record := range prepared {
			if !sameEmbedding(records[index].Embedding, record.Embedding) ||
				!reflect.DeepEqual(records[index].VectorProjections, record.VectorProjections) {
				digest, err := sessionstore.RecordSourceDigest(records[index])
				if err != nil {
					return stats, err
				}
				changed = append(changed, sessionstore.EmbeddingUpdate{
					ID: record.ID, SourceDigest: digest, Embedding: record.Embedding,
					VectorProjections: record.VectorProjections,
				})
			}
		}
		if len(changed) > 0 {
			var applied sessionstore.EmbeddingUpdateResult
			if contextual, ok := a.Store.(interface {
				ApplyEmbeddingsContext(context.Context, []sessionstore.EmbeddingUpdate) (sessionstore.EmbeddingUpdateResult, error)
			}); ok {
				applied, err = contextual.ApplyEmbeddingsContext(ctx, changed)
			} else {
				applied, err = a.ApplyEmbeddings(changed)
			}
			if err != nil {
				return stats, fmt.Errorf("archive embedding: save backfill: %w", err)
			}
			stats.Embedded += applied.Updated
			if applied.Skipped > 0 {
				// Deletes change pagination and edits need fresh embedding input.
				// Restart from the source, with a bound for continuously changing data.
				retries++
				if retries > 3 {
					return stats, errors.New("archive embedding: source kept changing during repair")
				}
				offset = -backfillPageSize
				continue
			}
		}
		if len(result.Hits) < backfillPageSize {
			return stats, nil
		}
	}
}

func (a *Archive) lockReconciliation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case a.reconcile <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Archive) unlockReconciliation() {
	<-a.reconcile
}

func isSemanticRecord(record sessionstore.Record) bool {
	if slices.Contains(record.Tags, "archive-read") {
		return false
	}
	return slices.Contains(semanticKinds, record.Kind)
}

func recordText(record sessionstore.Record) string {
	parts := make([]string, 0, 3)
	for _, value := range []string{record.Summary, record.Content, record.SearchText} {
		value = strings.TrimSpace(value)
		if value == "" || containsString(parts, value) {
			continue
		}
		parts = append(parts, value)
	}
	text := strings.Join(parts, "\n\n")
	runes := []rune(text)
	if len(runes) > maximumEmbeddingRunes {
		text = string(runes[:maximumEmbeddingRunes])
	}
	return text
}

func containsString(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}

func embeddingMatches(value *sessionstore.Embedding, vectorizer *embedding.Vectorizer) bool {
	return value != nil && value.Model == vectorizer.Model() &&
		value.Dimensions == vectorizer.Dimensions() && len(value.Vector) == vectorizer.Dimensions()
}

func sameEmbedding(left, right *sessionstore.Embedding) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Model == right.Model && left.Dimensions == right.Dimensions &&
		left.CreatedAt.Equal(right.CreatedAt)
}
