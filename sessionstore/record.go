// Package sessionstore persists durable workspace records and maintains a
// rebuildable Bleve search index over them.
package sessionstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const RecordVersion = 1

const maximumVectorProjections = 64

var ErrNotFound = errors.New("sessionstore: record not found")

// Record is the common envelope stored for messages, agent lifecycle events,
// task projections, results, questions, artifacts, and summaries.
type Record struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Version    int        `json:"version"`
	RunID      string     `json:"run_id,omitempty"`
	TaskID     string     `json:"task_id,omitempty"`
	ParentID   string     `json:"parent_id,omitempty"`
	Role       string     `json:"role,omitempty"`
	Model      string     `json:"model,omitempty"`
	Effort     string     `json:"effort,omitempty"`
	Status     string     `json:"status,omitempty"`
	Scope      string     `json:"scope,omitempty"`
	Location   string     `json:"location,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	Summary    string     `json:"summary,omitempty"`
	Content    string     `json:"content,omitempty"`
	SearchText string     `json:"search_text,omitempty"`
	Refs       []string   `json:"refs,omitempty"`
	Tags       []string   `json:"tags,omitempty"`
	Embedding  *Embedding `json:"embedding,omitempty"`
	// VectorProjections are derived semantic views of this source record. They
	// remain in the source record so the HNSW graph can always be rebuilt.
	VectorProjections []VectorProjection `json:"vector_projections,omitempty"`
	Payload           json.RawMessage    `json:"payload,omitempty"`
}

// Embedding is the source-of-truth vector attached to a record. The HNSW
// graph is derived from records whose model and dimensions match the store's
// active VectorConfig.
type Embedding struct {
	Model      string    `json:"model"`
	Dimensions int       `json:"dimensions"`
	CreatedAt  time.Time `json:"created_at"`
	Vector     []float32 `json:"vector"`
}

// VectorProjection gives one record multiple independently searchable vector
// variants without creating synthetic Session Store records. ID is stable
// within the parent record (for example, "content" or "query-0").
type VectorProjection struct {
	ID        string    `json:"id"`
	Embedding Embedding `json:"embedding"`
}

const (
	KindMessage     = "message"
	KindTask        = "task"
	KindEvent       = "event"
	KindResult      = "result"
	KindQuestion    = "question"
	KindArtifact    = "artifact"
	KindSummary     = "summary"
	KindSkill       = "skill"
	KindProposition = "proposition"
)

const (
	StatusSubmitted = "submitted"
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// NewID returns an archive-safe, time-sortable identifier. Callers can use it
// for run and task IDs as well as explicit record IDs.
func NewID() (string, error) {
	return newRecordID(time.Now().UTC())
}

func prepareRecord(record Record, previous *Record, now time.Time) (Record, error) {
	record.ID = strings.TrimSpace(record.ID)
	record.Kind = strings.TrimSpace(record.Kind)
	if record.ID == "" {
		id, err := newRecordID(now)
		if err != nil {
			return Record{}, err
		}
		record.ID = id
	}
	if len(record.ID) > 1024 {
		return Record{}, errors.New("sessionstore: record ID exceeds 1024 bytes")
	}
	if strings.ContainsRune(record.ID, '\x00') {
		return Record{}, errors.New("sessionstore: record ID contains a reserved character")
	}
	if record.Kind == "" {
		return Record{}, errors.New("sessionstore: record kind is required")
	}
	if record.Version == 0 {
		record.Version = RecordVersion
	}
	if record.Version != RecordVersion {
		return Record{}, fmt.Errorf("sessionstore: unsupported record version %d", record.Version)
	}
	if previous != nil && previous.Kind != record.Kind {
		return Record{}, errors.New("sessionstore: record kind cannot be changed")
	}
	if previous != nil && !record.CreatedAt.IsZero() && !record.CreatedAt.Equal(previous.CreatedAt) {
		return Record{}, errors.New("sessionstore: created_at cannot be changed")
	}
	if record.CreatedAt.IsZero() {
		if previous != nil {
			record.CreatedAt = previous.CreatedAt
		} else {
			record.CreatedAt = now
		}
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	if record.UpdatedAt.Before(record.CreatedAt) {
		return Record{}, errors.New("sessionstore: updated_at precedes created_at")
	}
	if previous != nil && record.UpdatedAt.Before(previous.UpdatedAt) {
		return Record{}, errors.New("sessionstore: updated_at cannot move backwards")
	}
	record.Refs = cloneStrings(record.Refs)
	record.Tags = cloneStrings(record.Tags)
	if record.Embedding != nil {
		embedding, err := prepareEmbedding(*record.Embedding, now)
		if err != nil {
			return Record{}, err
		}
		record.Embedding = &embedding
	}
	if len(record.VectorProjections) > maximumVectorProjections {
		return Record{}, fmt.Errorf("sessionstore: vector projections exceed %d items", maximumVectorProjections)
	}
	projections := make([]VectorProjection, 0, len(record.VectorProjections))
	seenProjectionIDs := make(map[string]struct{}, len(record.VectorProjections))
	for _, projection := range record.VectorProjections {
		projection.ID = strings.TrimSpace(projection.ID)
		if projection.ID == "" {
			return Record{}, errors.New("sessionstore: vector projection ID is required")
		}
		if len(projection.ID) > 128 {
			return Record{}, errors.New("sessionstore: vector projection ID exceeds 128 bytes")
		}
		if projection.ID == "$record" || strings.ContainsAny(projection.ID, "\x00/#\\") {
			return Record{}, errors.New("sessionstore: vector projection ID contains a reserved character")
		}
		if _, duplicate := seenProjectionIDs[projection.ID]; duplicate {
			return Record{}, fmt.Errorf("sessionstore: duplicate vector projection ID %q", projection.ID)
		}
		seenProjectionIDs[projection.ID] = struct{}{}
		embedding, err := prepareEmbedding(projection.Embedding, now)
		if err != nil {
			return Record{}, fmt.Errorf("sessionstore: vector projection %q: %w", projection.ID, err)
		}
		projection.Embedding = embedding
		projections = append(projections, projection)
	}
	record.VectorProjections = projections
	record.Payload = cloneBytes(record.Payload)
	return record, nil
}

func prepareEmbedding(embedding Embedding, now time.Time) (Embedding, error) {
	embedding.Model = strings.TrimSpace(embedding.Model)
	if embedding.Model == "" {
		return Embedding{}, errors.New("embedding model is required")
	}
	if embedding.Dimensions == 0 {
		embedding.Dimensions = len(embedding.Vector)
	}
	if embedding.Dimensions < 1 || len(embedding.Vector) != embedding.Dimensions {
		return Embedding{}, errors.New("embedding dimensions do not match vector length")
	}
	var magnitude float64
	for _, value := range embedding.Vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return Embedding{}, errors.New("embedding contains a non-finite value")
		}
		magnitude += float64(value) * float64(value)
	}
	if magnitude == 0 {
		return Embedding{}, errors.New("embedding vector must not be zero")
	}
	if embedding.CreatedAt.IsZero() {
		embedding.CreatedAt = now
	}
	embedding.CreatedAt = embedding.CreatedAt.UTC()
	embedding.Vector = cloneFloats(embedding.Vector)
	return embedding, nil
}

func newRecordID(now time.Time) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("sessionstore: generate record ID: %w", err)
	}
	return fmt.Sprintf("%d-%s", now.UTC().UnixMilli(), hex.EncodeToString(random)), nil
}

func cloneRecord(record Record) Record {
	record.Refs = cloneStrings(record.Refs)
	record.Tags = cloneStrings(record.Tags)
	if record.Embedding != nil {
		embedding := *record.Embedding
		embedding.Vector = cloneFloats(embedding.Vector)
		record.Embedding = &embedding
	}
	record.VectorProjections = cloneVectorProjections(record.VectorProjections)
	record.Payload = cloneBytes(record.Payload)
	return record
}

func cloneVectorProjections(values []VectorProjection) []VectorProjection {
	result := make([]VectorProjection, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Embedding.Vector = cloneFloats(value.Embedding.Vector)
	}
	return result
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}

func cloneFloats(value []float32) []float32 {
	return append([]float32(nil), value...)
}
