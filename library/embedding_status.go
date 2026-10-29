package library

import (
	"context"

	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/sessionstore"
)

// InspectEmbeddingStatus reports global skill coverage without
// starting the Library service or changing its vector configuration.
func InspectEmbeddingStatus(ctx context.Context, directory string, config sessionstore.VectorConfig) (sessionstore.EmbeddingStatus, error) {
	return sessionstore.InspectEmbeddings(ctx, directory, config, func(record sessionstore.Record, config sessionstore.VectorConfig) (sessionstore.EmbeddingRecordState, error) {
		if record.Scope != "global" {
			return sessionstore.EmbeddingRecordState{}, nil
		}
		var parts []agentskills.EmbeddingPart
		var err error
		switch record.Kind {
		case sessionstore.KindSkill:
			parts, err = agentskills.EmbeddingParts(record.Summary, record.Content, record.Tags)
		default:
			return sessionstore.EmbeddingRecordState{}, nil
		}
		return sessionstore.EmbeddingRecordState{Eligible: len(parts) > 0, Current: config.Enabled() && agentskills.EmbeddingPartsMatch(record, parts, config.Model, config.Dimensions)}, err
	})
}
