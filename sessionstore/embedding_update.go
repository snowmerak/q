package sessionstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// EmbeddingUpdate patches derived vectors only if the source still matches.
type EmbeddingUpdate struct {
	ID                string             `json:"id"`
	SourceDigest      string             `json:"source_digest"`
	Embedding         *Embedding         `json:"embedding,omitempty"`
	VectorProjections []VectorProjection `json:"vector_projections,omitempty"`
}

type EmbeddingUpdateResult struct {
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
}

// RecordSourceDigest includes source identity, content and metadata but excludes
// derived embeddings. Timestamps alone cannot distinguish all source changes.
func RecordSourceDigest(record Record) (string, error) {
	record.Embedding = nil
	record.VectorProjections = nil
	body, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

// ApplyEmbeddings compares and patches under the same lock as Save and Delete.
// A deleted record is never recreated, and an outdated worker cannot overwrite
// newer source fields or attach vectors for a superseded index model.
func (s *Store) ApplyEmbeddings(updates []EmbeddingUpdate) (EmbeddingUpdateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := EmbeddingUpdateResult{}
	if err := s.requireOpenLocked(); err != nil {
		return result, err
	}
	records := make([]Record, 0, len(updates))
	seen := make(map[string]bool, len(updates))
	for _, update := range updates {
		if update.ID == "" || update.SourceDigest == "" || seen[update.ID] {
			return result, errors.New("sessionstore: embedding update needs a unique ID and source digest")
		}
		seen[update.ID] = true
		record, err := s.loadRecordLocked(update.ID)
		if errors.Is(err, ErrNotFound) {
			result.Skipped++
			continue
		}
		if err != nil {
			return result, err
		}
		digest, err := RecordSourceDigest(record)
		if err != nil {
			return result, err
		}
		if digest != update.SourceDigest {
			result.Skipped++
			continue
		}
		record.Embedding = update.Embedding
		record.VectorProjections = update.VectorProjections
		for _, vector := range recordEmbeddings(record) {
			if !s.vectorCfg.Enabled() || vector.Embedding.Model != s.vectorCfg.Model || vector.Embedding.Dimensions != s.vectorCfg.Dimensions {
				return result, fmt.Errorf("sessionstore: embedding update for %q does not match active model", update.ID)
			}
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return result, nil
	}
	saved, err := s.saveBatchLocked(records)
	result.Updated = len(saved)
	return result, err
}
