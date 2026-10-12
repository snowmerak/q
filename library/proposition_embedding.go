package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/sessionstore"
)

// Proposition migrations reuse the bounded source/projection batch format used
// by skills. Content and query projections retain their registration IDs.
func propositionEmbeddingSource(record sessionstore.Record) (SkillEmbeddingSource, error) {
	var payload PropositionPayload
	if len(record.Payload) > 0 {
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return SkillEmbeddingSource{}, fmt.Errorf("library: decode proposition %q: %w", record.ID, err)
		}
	}
	parts := []agentskills.EmbeddingPart{{ID: "content", Text: record.Content}}
	for index, query := range payload.Queries {
		parts = append(parts, agentskills.EmbeddingPart{ID: fmt.Sprintf("query-%d", index), Text: query})
	}
	body, err := json.Marshal(parts)
	if err != nil {
		return SkillEmbeddingSource{}, err
	}
	digest := sha256.Sum256(body)
	return SkillEmbeddingSource{ID: record.ID, Digest: hex.EncodeToString(digest[:]), Parts: parts}, nil
}

func (s *propositionService) embeddingSources(ctx context.Context, request SkillEmbeddingSourceRequest) (SkillEmbeddingSourceResponse, error) {
	request.Model = strings.TrimSpace(request.Model)
	if err := validateEmbeddingModel(s.embeddingConfigDir, s.archive, request.Model, request.Dimensions); err != nil {
		return SkillEmbeddingSourceResponse{}, err
	}
	if request.Limit == 0 {
		request.Limit = defaultSkillEmbedBatch
	}
	if request.Limit < 1 || request.Limit > maximumSkillEmbedBatch {
		return SkillEmbeddingSourceResponse{}, errors.New("library: proposition embedding batch limit is invalid")
	}
	response := SkillEmbeddingSourceResponse{}
	for offset := 0; ; offset += 100 {
		result, err := s.archive.Search(ctx, sessionstore.SearchOptions{
			Filters: sessionstore.Filters{Kinds: []string{sessionstore.KindProposition}, Scopes: []string{"global"}},
			Sort:    sessionstore.SortOldest, Limit: 100, Offset: offset,
		})
		if err != nil {
			return SkillEmbeddingSourceResponse{}, err
		}
		for _, hit := range result.Hits {
			source, err := propositionEmbeddingSource(hit.Record)
			if err != nil {
				return SkillEmbeddingSourceResponse{}, err
			}
			if agentskills.EmbeddingPartsMatch(hit.Record, source.Parts, request.Model, request.Dimensions) {
				continue
			}
			response.Remaining++
			if len(response.Sources) < request.Limit {
				response.Sources = append(response.Sources, source)
			}
		}
		if len(result.Hits) < 100 {
			return response, nil
		}
	}
}

func (s *propositionService) applyEmbeddings(request SkillEmbeddingApplyRequest) (SkillEmbeddingApplyResponse, error) {
	if len(request.Items) < 1 || len(request.Items) > maximumSkillEmbedBatch {
		return SkillEmbeddingApplyResponse{}, errors.New("library: proposition embedding batch size is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !skillVectorMatches(s.archive.VectorConfig(), request.Model, request.Dimensions) {
		return SkillEmbeddingApplyResponse{}, errors.New("library: proposition embedding model changed before the batch was applied")
	}
	updates := make([]sessionstore.EmbeddingUpdate, 0, len(request.Items))
	seen := make(map[string]bool, len(request.Items))
	for _, item := range request.Items {
		if seen[item.ID] {
			return SkillEmbeddingApplyResponse{}, fmt.Errorf("library: duplicate proposition %q", item.ID)
		}
		seen[item.ID] = true
		record, err := s.archive.Get(item.ID)
		if err != nil {
			return SkillEmbeddingApplyResponse{}, err
		}
		if record.Kind != sessionstore.KindProposition || record.Scope != "global" {
			return SkillEmbeddingApplyResponse{}, fmt.Errorf("library: unknown global proposition %q", item.ID)
		}
		source, err := propositionEmbeddingSource(record)
		if err != nil {
			return SkillEmbeddingApplyResponse{}, err
		}
		if item.Digest != source.Digest || len(item.Projections) != len(source.Parts) {
			return SkillEmbeddingApplyResponse{}, fmt.Errorf("library: proposition %q changed during embedding", item.ID)
		}
		digest, err := sessionstore.RecordSourceDigest(record)
		if err != nil {
			return SkillEmbeddingApplyResponse{}, err
		}
		record.Embedding = nil
		record.VectorProjections = make([]sessionstore.VectorProjection, 0, len(source.Parts))
		for index, part := range source.Parts {
			projection := item.Projections[index]
			if projection.ID != part.ID || len(projection.Vector) != request.Dimensions {
				return SkillEmbeddingApplyResponse{}, fmt.Errorf("library: proposition %q projection does not match %q", item.ID, part.ID)
			}
			record.VectorProjections = append(record.VectorProjections, sessionstore.VectorProjection{
				ID: part.ID, Embedding: sessionstore.Embedding{
					Model: request.Model, Dimensions: request.Dimensions, CreatedAt: time.Now().UTC(),
					Vector: append([]float32(nil), projection.Vector...),
				},
			})
		}
		// Embeddings are derived data: preserve content, provenance, timestamps,
		// and registration idempotency without submitting another judgment job.
		updates = append(updates, sessionstore.EmbeddingUpdate{ID: record.ID, SourceDigest: digest, VectorProjections: record.VectorProjections})
	}
	if err := validateEmbeddingModel(s.embeddingConfigDir, s.archive, request.Model, request.Dimensions); err != nil {
		return SkillEmbeddingApplyResponse{}, err
	}
	applied, err := s.archive.ApplyEmbeddings(updates)
	if err != nil {
		return SkillEmbeddingApplyResponse{}, err
	}
	return SkillEmbeddingApplyResponse{Updated: applied.Updated}, nil
}

func registerPropositionEmbeddingRoutes(mux *http.ServeMux, propositions *propositionService) {
	mux.HandleFunc("POST /v1/propositions/embeddings/sources", func(writer http.ResponseWriter, request *http.Request) {
		var input SkillEmbeddingSourceRequest
		if err := decodeLibraryJSON(writer, request, &input); err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		output, err := propositions.embeddingSources(request.Context(), input)
		if err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSON(writer, http.StatusOK, output)
	})
	mux.HandleFunc("POST /v1/propositions/embeddings", func(writer http.ResponseWriter, request *http.Request) {
		var input SkillEmbeddingApplyRequest
		if err := decodeLibraryJSONWithLimit(writer, request, &input, maximumSkillEmbedBody); err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		output, err := propositions.applyEmbeddings(input)
		if err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSON(writer, http.StatusOK, output)
	})
}
