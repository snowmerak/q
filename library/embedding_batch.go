package library

import (
	"encoding/json"
	"fmt"
)

const (
	maximumEmbeddingBatchProjections = 128
	maximumEmbeddingBatchBytes       = 72 * (1 << 20) / 10 // 7.2 MiB, below the server's 8 MiB limit.
)

// Keep each record's projections together: the server validates and applies
// complete records atomically. Include the JSON envelope and commas in the
// byte budget; vector dimensions alone cannot predict JSON number lengths.
func nextEmbeddingBatch(model string, dimensions int, items []SkillEmbeddingItem) (SkillEmbeddingApplyRequest, error) {
	batch := SkillEmbeddingApplyRequest{Model: model, Dimensions: dimensions, Items: []SkillEmbeddingItem{}}
	envelope, err := json.Marshal(batch)
	if err != nil {
		return batch, fmt.Errorf("library: encode embedding batch: %w", err)
	}
	size, projections := len(envelope), 0
	for _, item := range items {
		if len(batch.Items) == maximumSkillEmbedBatch {
			break
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return batch, fmt.Errorf("library: encode embedding item %q: %w", item.ID, err)
		}
		nextSize := size + len(encoded)
		if len(batch.Items) > 0 {
			nextSize++ // Comma separating this item from the previous one.
		}
		nextProjections := projections + len(item.Projections)
		if nextProjections > maximumEmbeddingBatchProjections || nextSize > maximumEmbeddingBatchBytes {
			if len(batch.Items) == 0 {
				return batch, fmt.Errorf("library: embedding item %q requires %d projections and %d request bytes; limits are %d projections and %d bytes",
					item.ID, nextProjections, nextSize, maximumEmbeddingBatchProjections, maximumEmbeddingBatchBytes)
			}
			break
		}
		batch.Items = append(batch.Items, item)
		size, projections = nextSize, nextProjections
	}
	return batch, nil
}
