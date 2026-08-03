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

var ErrNotFound = errors.New("sessionstore: record not found")

// Record is the common envelope stored for messages, agent lifecycle events,
// task projections, results, questions, artifacts, and summaries.
type Record struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Version   int             `json:"version"`
	RunID     string          `json:"run_id,omitempty"`
	TaskID    string          `json:"task_id,omitempty"`
	ParentID  string          `json:"parent_id,omitempty"`
	Role      string          `json:"role,omitempty"`
	Model     string          `json:"model,omitempty"`
	Effort    string          `json:"effort,omitempty"`
	Status    string          `json:"status,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Summary   string          `json:"summary,omitempty"`
	Content   string          `json:"content,omitempty"`
	Refs      []string        `json:"refs,omitempty"`
	Tags      []string        `json:"tags,omitempty"`
	Embedding *Embedding      `json:"embedding,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
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

const (
	KindMessage  = "message"
	KindTask     = "task"
	KindEvent    = "event"
	KindResult   = "result"
	KindQuestion = "question"
	KindArtifact = "artifact"
	KindSummary  = "summary"
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
		embedding := *record.Embedding
		embedding.Model = strings.TrimSpace(embedding.Model)
		if embedding.Model == "" {
			return Record{}, errors.New("sessionstore: embedding model is required")
		}
		if embedding.Dimensions == 0 {
			embedding.Dimensions = len(embedding.Vector)
		}
		if embedding.Dimensions < 1 || len(embedding.Vector) != embedding.Dimensions {
			return Record{}, errors.New("sessionstore: embedding dimensions do not match vector length")
		}
		var magnitude float64
		for _, value := range embedding.Vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return Record{}, errors.New("sessionstore: embedding contains a non-finite value")
			}
			magnitude += float64(value) * float64(value)
		}
		if magnitude == 0 {
			return Record{}, errors.New("sessionstore: embedding vector must not be zero")
		}
		if embedding.CreatedAt.IsZero() {
			embedding.CreatedAt = now
		}
		embedding.CreatedAt = embedding.CreatedAt.UTC()
		embedding.Vector = cloneFloats(embedding.Vector)
		record.Embedding = &embedding
	}
	record.Payload = cloneBytes(record.Payload)
	return record, nil
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
	record.Payload = cloneBytes(record.Payload)
	return record
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
