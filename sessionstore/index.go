package sessionstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/snowmerak/q/internal/closeutil"
)

// RecordWriter mutates records in a derived index.
type RecordWriter interface {
	Save(ctx context.Context, record Record) (Record, error)
	SaveBatch(ctx context.Context, records []Record) ([]Record, error)
	Delete(ctx context.Context, id string) error
}

// RecordReader retrieves individual records and index document/vector counts.
type RecordReader interface {
	Get(ctx context.Context, id string) (Record, error)
	IndexCounts(ctx context.Context) (documents uint64, vectors int, err error)
}

// RecordSearcher executes lexical, vector, and hybrid queries over records.
type RecordSearcher interface {
	Search(ctx context.Context, options SearchOptions) (SearchResult, error)
}

// VectorStore manages derived embedding updates and vector index configuration.
type VectorStore interface {
	ApplyEmbeddings(ctx context.Context, updates []EmbeddingUpdate) (EmbeddingUpdateResult, error)
	ConfigureVector(ctx context.Context, config VectorConfig) error
	VectorConfig() VectorConfig
}

// SearchIndex is the derived record, lexical, and vector index contract used
// by library and workspacememory. Network-backed implementations (such as
// OpenSearch) must honor ctx cancellation and preserve ErrNotFound and
// IndexingError distinctions where applicable.
type SearchIndex interface {
	RecordWriter
	RecordReader
	RecordSearcher
	VectorStore
	io.Closer
}

// ComposedIndex assembles a SearchIndex via struct embedding of narrow
// capability interfaces while owning a deduplicated set of io.Closer handles.
type ComposedIndex struct {
	RecordWriter
	RecordReader
	RecordSearcher
	VectorStore

	closers   []io.Closer
	closeOnce sync.Once
	closeErr  error
}

var _ SearchIndex = (*ComposedIndex)(nil)

// ComposeIndex combines split record/search/vector collaborators into one
// SearchIndex. Shared closers are closed at most once.
func ComposeIndex(
	writer RecordWriter,
	reader RecordReader,
	searcher RecordSearcher,
	vectors VectorStore,
	closers ...io.Closer,
) (*ComposedIndex, error) {
	if writer == nil || reader == nil || searcher == nil || vectors == nil {
		return nil, errors.New("sessionstore: composed index requires writer, reader, searcher, and vector store")
	}
	return &ComposedIndex{
		RecordWriter:   writer,
		RecordReader:   reader,
		RecordSearcher: searcher,
		VectorStore:    vectors,
		closers:        closeutil.Deduplicate(closers),
	}, nil
}

// Close closes every registered closer once and returns any joined errors.
func (c *ComposedIndex) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.closeErr = closeutil.CloseAll(c.closers)
	})
	return c.closeErr
}

// PrepareRecord normalizes and validates record metadata against an optional
// previous record before indexing.
func PrepareRecord(record Record, previous *Record, now time.Time) (Record, error) {
	return prepareRecord(record, previous, now)
}

// ValidateSearchOptions validates and normalizes search parameters.
func ValidateSearchOptions(options *SearchOptions) error {
	return validateSearchOptions(options)
}

// NormalizeVectorConfig validates and defaults vector index settings.
func NormalizeVectorConfig(config VectorConfig) (VectorConfig, error) {
	return normalizeVectorConfig(config)
}

// ValidateQueryVector validates query vector dimensions and finite non-zero values.
func ValidateQueryVector(vector []float32, dimensions int) error {
	if len(vector) != dimensions {
		return fmt.Errorf("sessionstore: query vector has %d dimensions; want %d", len(vector), dimensions)
	}
	if err := validateFiniteNonzeroVector(vector); err != nil {
		return fmt.Errorf("sessionstore: query vector: %w", err)
	}
	return nil
}

// RerankByRecency applies exponential half-life decay and sorts hits in place.
func RerankByRecency(hits []Hit, options Recency) {
	rerankByRecency(hits, options)
}

// OmitToolCallMessage filters hits matching exclude without mutating records.
func OmitToolCallMessage(hits []Hit, exclude *MessageToolCall) []Hit {
	return omitToolCallMessage(hits, exclude)
}

// RecordMatchesSearch evaluates all non-text SearchOptions filters against record.
func RecordMatchesSearch(record Record, options SearchOptions) bool {
	return recordMatchesSearch(record, options)
}

// ProjectionWeight resolves the weight for projection id using exact match
// first, then prefix before '-'.
func ProjectionWeight(weights map[string]float64, id string) float64 {
	return projectionWeight(weights, id)
}

// LocalIndex adapts a local Bleve+HNSW *Store to the context-aware SearchIndex
// contract.
type LocalIndex struct {
	store *Store
}

var _ SearchIndex = (*LocalIndex)(nil)

// NewLocalIndex wraps store as a SearchIndex, returning an untyped nil when
// store is nil.
func NewLocalIndex(store *Store) SearchIndex {
	if store == nil {
		return nil
	}
	return &LocalIndex{store: store}
}

// OpenLocalIndex opens a local Bleve+HNSW Store at root and wraps it as a
// SearchIndex.
func OpenLocalIndex(ctx context.Context, root string, options OpenOptions) (SearchIndex, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	store, err := OpenWithOptions(root, options)
	if err != nil {
		return nil, err
	}
	return NewLocalIndex(store), nil
}

// Store returns the underlying local *Store.
func (l *LocalIndex) Store() *Store {
	if l == nil {
		return nil
	}
	return l.store
}

func (l *LocalIndex) Save(ctx context.Context, record Record) (Record, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Record{}, err
		}
	}
	return l.store.Save(record)
}

func (l *LocalIndex) SaveBatch(ctx context.Context, records []Record) ([]Record, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return l.store.SaveBatch(records)
}

func (l *LocalIndex) ApplyEmbeddings(ctx context.Context, updates []EmbeddingUpdate) (EmbeddingUpdateResult, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return EmbeddingUpdateResult{}, err
		}
	}
	return l.store.ApplyEmbeddings(updates)
}

func (l *LocalIndex) Get(ctx context.Context, id string) (Record, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Record{}, err
		}
	}
	return l.store.Get(id)
}

func (l *LocalIndex) Delete(ctx context.Context, id string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return l.store.Delete(id)
}

func (l *LocalIndex) Search(ctx context.Context, options SearchOptions) (SearchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return l.store.Search(ctx, options)
}

func (l *LocalIndex) IndexCounts(ctx context.Context) (uint64, int, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
	}
	return l.store.IndexCounts()
}

func (l *LocalIndex) ConfigureVector(ctx context.Context, config VectorConfig) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return l.store.ConfigureVector(config)
}

func (l *LocalIndex) VectorConfig() VectorConfig {
	return l.store.VectorConfig()
}

func (l *LocalIndex) Close() error {
	return l.store.Close()
}
