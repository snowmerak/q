package agentskills

import (
	"fmt"
	"strings"

	"github.com/snowmerak/q/sessionstore"
)

const maximumSkillEmbeddingTags = 62 // name and description use the other two projection slots.

type EmbeddingPart struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// EmbeddingParts keeps each metadata field independently searchable. Tag IDs
// follow the registry's sorted tag order and are stable until the skill changes.
func EmbeddingParts(name, description string, tags []string) ([]EmbeddingPart, error) {
	if len(tags) > maximumSkillEmbeddingTags {
		return nil, fmt.Errorf("agent skills: %d tags exceed %d embedding projections", len(tags), maximumSkillEmbeddingTags)
	}
	parts := make([]EmbeddingPart, 0, 2+len(tags))
	for _, field := range []EmbeddingPart{
		{ID: "name", Text: strings.TrimSpace(name)},
		{ID: "description", Text: strings.TrimSpace(description)},
	} {
		if field.Text != "" {
			parts = append(parts, field)
		}
	}
	for index, tag := range tags {
		if text := strings.TrimSpace(tag); text != "" {
			parts = append(parts, EmbeddingPart{ID: fmt.Sprintf("tag-%d", index), Text: text})
		}
	}
	return parts, nil
}

func EmbeddingPartsMatch(record sessionstore.Record, parts []EmbeddingPart, model string, dimensions int) bool {
	if record.Embedding != nil || len(record.VectorProjections) != len(parts) {
		return false
	}
	for index, part := range parts {
		projection := record.VectorProjections[index]
		if projection.ID != part.ID || projection.Embedding.Model != model ||
			projection.Embedding.Dimensions != dimensions || len(projection.Embedding.Vector) != dimensions {
			return false
		}
	}
	return true
}

// SkillVectorQuery favors description meaning over tags and names. The
// Session Store uses these weights to rank projections within the vector list.
func SkillVectorQuery(embedding []float32) *sessionstore.VectorQuery {
	return &sessionstore.VectorQuery{
		Embedding: embedding,
		ProjectionWeights: map[string]float64{
			"description": 4,
			"tag":         3,
			"name":        2,
		},
	}
}
