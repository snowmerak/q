// Package archiveembed connects workspace Session Store records to a
// configured embedding provider. The Session Store remains the source of
// truth; its HNSW graph is always rebuildable from these record embeddings.
package archiveembed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

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

// Archive adds semantic query and embedding preparation behavior to a Store.
// Save, Delete, and Get are promoted from the embedded Store so project Agent
// Skill synchronization continues to use the same archive.
type Archive struct {
	*sessionstore.Store

	mu         sync.RWMutex
	vectorizer *embedding.Vectorizer
}

type BackfillStats struct {
	Scanned  int
	Embedded int
}

func New(store *sessionstore.Store) *Archive {
	return &Archive{Store: store}
}

// Configure enables record and query embedding and switches the Store's
// rebuildable vector graph to the selected model.
func (a *Archive) Configure(provider embedding.Provider, model string, dimensions int) error {
	if a == nil || a.Store == nil {
		return errors.New("archive embedding: store is unavailable")
	}
	vectorizer, err := embedding.New(provider, model, dimensions)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.Store.ConfigureVector(sessionstore.VectorConfig{
		Model: vectorizer.Model(), Dimensions: vectorizer.Dimensions(),
	}); err != nil {
		return fmt.Errorf("archive embedding: configure vector index: %w", err)
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
	if err := a.Store.ConfigureVector(sessionstore.VectorConfig{}); err != nil {
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
	a.mu.RLock()
	defer a.mu.RUnlock()
	vectorizer := a.vectorizer
	if vectorizer == nil {
		return records, nil
	}
	positions := make([]int, 0, len(records))
	texts := make([]string, 0, len(records))
	for index := range records {
		if embeddingMatches(records[index].Embedding, vectorizer) || !isHistoryRecord(records[index]) {
			continue
		}
		text := recordText(records[index])
		if text == "" {
			continue
		}
		positions = append(positions, index)
		texts = append(texts, text)
	}
	if len(texts) == 0 {
		return records, nil
	}
	vectors, err := vectorizer.Embed(ctx, texts)
	if err != nil {
		return records, fmt.Errorf("archive embedding: embed records: %w", err)
	}
	createdAt := time.Now().UTC()
	for index, position := range positions {
		records[position].Embedding = &sessionstore.Embedding{
			Model: vectorizer.Model(), Dimensions: vectorizer.Dimensions(),
			CreatedAt: createdAt, Vector: vectors[index],
		}
	}
	return records, nil
}

// Search automatically adds a vector query to relevance searches with text.
// Filter-only and explicitly chronological searches remain lexical/metadata
// operations and therefore do not call the embedding provider.
func (a *Archive) Search(ctx context.Context, options sessionstore.SearchOptions) (sessionstore.SearchResult, error) {
	if a == nil || a.Store == nil {
		return sessionstore.SearchResult{}, errors.New("archive embedding: store is unavailable")
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	vectorizer := a.vectorizer
	if vectorizer != nil && options.Vector == nil && strings.TrimSpace(options.Text) != "" &&
		(options.Sort == "" || options.Sort == sessionstore.SortRelevance) && searchesHistory(options.Filters.Kinds) {
		vectors, err := vectorizer.Embed(ctx, []string{strings.TrimSpace(options.Text)})
		if err == nil {
			options.Vector = &sessionstore.VectorQuery{Embedding: vectors[0]}
		}
	}
	return a.Store.Search(ctx, options)
}

func searchesHistory(kinds []string) bool {
	for _, wanted := range kinds {
		for _, kind := range historyKinds {
			if wanted == kind {
				return true
			}
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
	if a.configuredVectorizer() == nil {
		return BackfillStats{}, nil
	}
	stats := BackfillStats{}
	for offset := 0; ; offset += backfillPageSize {
		result, err := a.Store.Search(ctx, sessionstore.SearchOptions{
			Filters: sessionstore.Filters{Kinds: historyKinds},
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
		prepared, err := a.Prepare(ctx, append([]sessionstore.Record(nil), records...))
		if err != nil {
			return stats, err
		}
		changed := make([]sessionstore.Record, 0, len(prepared))
		for index, record := range prepared {
			if !sameEmbedding(records[index].Embedding, record.Embedding) {
				changed = append(changed, record)
			}
		}
		if len(changed) > 0 {
			if _, err := a.Store.SaveBatch(changed); err != nil {
				return stats, fmt.Errorf("archive embedding: save backfill: %w", err)
			}
			stats.Embedded += len(changed)
		}
		if len(result.Hits) < backfillPageSize {
			return stats, nil
		}
	}
}

func (a *Archive) configuredVectorizer() *embedding.Vectorizer {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.vectorizer
}

func isHistoryRecord(record sessionstore.Record) bool {
	for _, tag := range record.Tags {
		if tag == "archive-read" {
			return false
		}
	}
	for _, kind := range historyKinds {
		if record.Kind == kind {
			return true
		}
	}
	return false
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
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
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
