package archiveembed

import (
	"context"

	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/sessionstore"
)

// InspectStatus uses the same eligibility and projection rules as Backfill,
// while reading source files without opening or repairing the archive.
func InspectStatus(ctx context.Context, directory string, config sessionstore.VectorConfig) (sessionstore.EmbeddingStatus, error) {
	return sessionstore.InspectEmbeddings(ctx, directory, config, func(record sessionstore.Record, config sessionstore.VectorConfig) (sessionstore.EmbeddingRecordState, error) {
		if !isSemanticRecord(record) {
			return sessionstore.EmbeddingRecordState{}, nil
		}
		if record.Kind == sessionstore.KindSkill {
			parts, err := agentskills.EmbeddingParts(record.Summary, record.Content, record.Tags)
			return sessionstore.EmbeddingRecordState{Eligible: len(parts) > 0, Current: config.Enabled() && agentskills.EmbeddingPartsMatch(record, parts, config.Model, config.Dimensions)}, err
		}
		value := record.Embedding
		return sessionstore.EmbeddingRecordState{
			Eligible: recordText(record) != "",
			Current:  config.Enabled() && value != nil && value.Model == config.Model && value.Dimensions == config.Dimensions && len(value.Vector) == config.Dimensions,
		}, nil
	})
}
