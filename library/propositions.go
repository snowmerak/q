package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/sessionstore"
)

const (
	defaultPropositionSearchLimit         = 10
	maximumPropositionSearchLimit         = 100
	maximumPropositionSearchOffset        = 5000
	maximumPropositionQueryRunes          = 16_000
	maximumPropositionRecencyWeight       = 100
	maximumPropositionRecencyHalfLifeHour = 24 * 365 * 100
	defaultPropositionRecencyWeight       = 0.25
	defaultPropositionRecencyHalfLifeHour = 24 * 30
	maximumPropositionSearchContentRunes  = 4_096
	maximumPropositionContentRunes        = 2_048
	maximumPropositionQueries             = 8
	maximumPropositionQueryRunesPerItem   = 512
	maximumPropositionTags                = 16
	maximumPropositionTagRunes            = 64
	maximumPropositionRefs                = 32
	maximumPropositionRefRunes            = 2_048
	maximumPropositionIdempotencyKeyBytes = 256
)

var errPropositionIdempotencyConflict = errors.New("library: proposition idempotency key was already used with different input")

var propositionSensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bqk_[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`(?i)\b(api[_ -]?key|access[_ -]?token|password|secret)\s*[:=]\s*[^\s]{8,}`),
}

// PropositionSearchRequest searches durable global propositions. Nil recency
// values select the Library defaults; an explicit zero weight disables the
// created_at boost for one request.
type PropositionSearchRequest struct {
	Query                string     `json:"query"`
	Embedding            []float32  `json:"embedding,omitempty"`
	Tags                 []string   `json:"tags,omitempty"`
	CreatedAfter         *time.Time `json:"created_after,omitempty"`
	CreatedBefore        *time.Time `json:"created_before,omitempty"`
	Limit                int        `json:"limit,omitempty"`
	Offset               int        `json:"offset,omitempty"`
	RecencyWeight        *float64   `json:"recency_weight,omitempty"`
	RecencyHalfLifeHours *float64   `json:"recency_half_life_hours,omitempty"`
}

// PropositionEmbeddings carries a bounded batch in canonical order: content
// first, followed by one vector for each normalized query. The Library assigns
// stable projection IDs and persists the vectors as rebuildable source data.
type PropositionEmbeddings struct {
	Model      string      `json:"model"`
	Dimensions int         `json:"dimensions,omitempty"`
	Vectors    [][]float32 `json:"vectors"`
}

type PropositionPayload struct {
	Queries          []string `json:"queries,omitempty"`
	Confidence       *float64 `json:"confidence,omitempty"`
	ExtractorModel   string   `json:"extractor_model,omitempty"`
	ExtractorVersion string   `json:"extractor_version,omitempty"`
}

type propositionStoredPayload struct {
	PropositionPayload
	IdempotencyHash string `json:"idempotency_hash,omitempty"`
	RequestDigest   string `json:"request_digest,omitempty"`
}

type PropositionRegisterRequest struct {
	Content          string                 `json:"content"`
	Queries          []string               `json:"queries,omitempty"`
	Confidence       float64                `json:"confidence"`
	Tags             []string               `json:"tags,omitempty"`
	Refs             []string               `json:"refs,omitempty"`
	ExtractorModel   string                 `json:"extractor_model"`
	ExtractorVersion string                 `json:"extractor_version"`
	Embeddings       *PropositionEmbeddings `json:"embeddings,omitempty"`
}

type PropositionRegisterResponse struct {
	ID      string `json:"id"`
	Created bool   `json:"created"`
}

type PropositionDeleteResponse struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

type PropositionSearchHit struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Truncated bool      `json:"truncated,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Refs      []string  `json:"refs,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	Score     float64   `json:"score"`
}

type PropositionSearchResponse struct {
	Total                uint64                 `json:"total"`
	Hits                 []PropositionSearchHit `json:"hits"`
	RecencyWeight        float64                `json:"recency_weight"`
	RecencyHalfLifeHours float64                `json:"recency_half_life_hours"`
}

type Proposition struct {
	ID        string             `json:"id"`
	Content   string             `json:"content"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
	Refs      []string           `json:"refs,omitempty"`
	Tags      []string           `json:"tags,omitempty"`
	Payload   PropositionPayload `json:"payload,omitempty"`
}

type propositionService struct {
	archive *sessionstore.Store
	now     func() time.Time
	mu      sync.Mutex
}

func (s *propositionService) register(idempotencyKey string, request PropositionRegisterRequest) (PropositionRegisterResponse, error) {
	if s == nil || s.archive == nil {
		return PropositionRegisterResponse{}, errors.New("library: propositions are unavailable")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return PropositionRegisterResponse{}, errors.New("library: Idempotency-Key is required")
	}
	if len(idempotencyKey) > maximumPropositionIdempotencyKeyBytes {
		return PropositionRegisterResponse{}, fmt.Errorf("library: Idempotency-Key exceeds %d bytes", maximumPropositionIdempotencyKeyBytes)
	}
	normalized, err := normalizePropositionRegisterRequest(request)
	if err != nil {
		return PropositionRegisterResponse{}, err
	}
	if normalized.Embeddings != nil {
		vector := s.archive.VectorConfig()
		if !vector.Enabled() {
			return PropositionRegisterResponse{}, errors.New("library: proposition vector index is not configured")
		}
		if normalized.Embeddings.Model != vector.Model || normalized.Embeddings.Dimensions != vector.Dimensions {
			return PropositionRegisterResponse{}, fmt.Errorf(
				"library: proposition embeddings use %s/%d; Library index uses %s/%d",
				normalized.Embeddings.Model, normalized.Embeddings.Dimensions, vector.Model, vector.Dimensions,
			)
		}
	}
	body, err := json.Marshal(normalized)
	if err != nil {
		return PropositionRegisterResponse{}, fmt.Errorf("library: encode proposition registration: %w", err)
	}
	keySum := sha256.Sum256([]byte("q-library-proposition-v1\x00" + idempotencyKey))
	requestSum := sha256.Sum256(body)
	id := "prop-" + hex.EncodeToString(keySum[:16])
	idempotencyHash := hex.EncodeToString(keySum[:])
	requestDigest := hex.EncodeToString(requestSum[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, getErr := s.archive.Get(id); getErr == nil {
		var payload propositionStoredPayload
		if existing.Kind != sessionstore.KindProposition || existing.Scope != "global" ||
			json.Unmarshal(existing.Payload, &payload) != nil || payload.IdempotencyHash != idempotencyHash ||
			payload.RequestDigest != requestDigest {
			return PropositionRegisterResponse{}, errPropositionIdempotencyConflict
		}
		return PropositionRegisterResponse{ID: existing.ID, Created: false}, nil
	} else if !errors.Is(getErr, sessionstore.ErrNotFound) {
		return PropositionRegisterResponse{}, getErr
	}
	payload, err := json.Marshal(propositionStoredPayload{
		PropositionPayload: PropositionPayload{
			Queries: normalized.Queries, Confidence: &normalized.Confidence,
			ExtractorModel: normalized.ExtractorModel, ExtractorVersion: normalized.ExtractorVersion,
		},
		IdempotencyHash: idempotencyHash, RequestDigest: requestDigest,
	})
	if err != nil {
		return PropositionRegisterResponse{}, fmt.Errorf("library: encode proposition payload: %w", err)
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	projections := make([]sessionstore.VectorProjection, 0)
	if normalized.Embeddings != nil {
		projections = make([]sessionstore.VectorProjection, 0, len(normalized.Embeddings.Vectors))
		for index, vector := range normalized.Embeddings.Vectors {
			projectionID := "content"
			if index > 0 {
				projectionID = fmt.Sprintf("query-%d", index-1)
			}
			projections = append(projections, sessionstore.VectorProjection{
				ID: projectionID,
				Embedding: sessionstore.Embedding{
					Model: normalized.Embeddings.Model, Dimensions: normalized.Embeddings.Dimensions,
					CreatedAt: now, Vector: append([]float32(nil), vector...),
				},
			})
		}
	}
	_, err = s.archive.Save(sessionstore.Record{
		ID: id, Kind: sessionstore.KindProposition, Scope: "global",
		Content: normalized.Content, SearchText: strings.Join(normalized.Queries, "\n"),
		CreatedAt: now, UpdatedAt: now, Refs: normalized.Refs, Tags: normalized.Tags,
		VectorProjections: projections, Payload: payload,
	})
	if err != nil {
		return PropositionRegisterResponse{}, err
	}
	return PropositionRegisterResponse{ID: id, Created: true}, nil
}

func normalizePropositionRegisterRequest(request PropositionRegisterRequest) (PropositionRegisterRequest, error) {
	request.Content = strings.TrimSpace(request.Content)
	request.ExtractorModel = strings.TrimSpace(request.ExtractorModel)
	request.ExtractorVersion = strings.TrimSpace(request.ExtractorVersion)
	if request.Content == "" {
		return PropositionRegisterRequest{}, errors.New("library: proposition content is required")
	}
	if len([]rune(request.Content)) > maximumPropositionContentRunes {
		return PropositionRegisterRequest{}, fmt.Errorf("library: proposition content exceeds %d characters", maximumPropositionContentRunes)
	}
	if math.IsNaN(request.Confidence) || math.IsInf(request.Confidence, 0) || request.Confidence < 0 || request.Confidence > 1 {
		return PropositionRegisterRequest{}, errors.New("library: proposition confidence must be between 0 and 1")
	}
	if request.ExtractorModel == "" || request.ExtractorVersion == "" {
		return PropositionRegisterRequest{}, errors.New("library: proposition extractor model and version are required")
	}
	if len([]rune(request.ExtractorModel)) > 256 || len([]rune(request.ExtractorVersion)) > 128 {
		return PropositionRegisterRequest{}, errors.New("library: proposition extractor metadata is too long")
	}
	var err error
	request.Queries, err = normalizeBoundedStrings(request.Queries, maximumPropositionQueries, maximumPropositionQueryRunesPerItem, "query")
	if err != nil {
		return PropositionRegisterRequest{}, err
	}
	request.Tags, err = normalizeBoundedStrings(request.Tags, maximumPropositionTags, maximumPropositionTagRunes, "tag")
	if err != nil {
		return PropositionRegisterRequest{}, err
	}
	request.Refs, err = normalizeBoundedStrings(request.Refs, maximumPropositionRefs, maximumPropositionRefRunes, "ref")
	if err != nil {
		return PropositionRegisterRequest{}, err
	}
	for _, value := range append([]string{request.Content}, request.Queries...) {
		for _, pattern := range propositionSensitivePatterns {
			if pattern.MatchString(value) {
				return PropositionRegisterRequest{}, errors.New("library: proposition contains credential-like sensitive data")
			}
		}
	}
	if request.Embeddings != nil {
		request.Embeddings.Model = strings.TrimSpace(request.Embeddings.Model)
		if request.Embeddings.Model == "" {
			return PropositionRegisterRequest{}, errors.New("library: proposition embedding model is required")
		}
		if len(request.Embeddings.Vectors) != len(request.Queries)+1 {
			return PropositionRegisterRequest{}, errors.New("library: proposition embeddings must contain content followed by one vector per query")
		}
		if request.Embeddings.Dimensions == 0 && len(request.Embeddings.Vectors) > 0 {
			request.Embeddings.Dimensions = len(request.Embeddings.Vectors[0])
		}
		if request.Embeddings.Dimensions < 1 || request.Embeddings.Dimensions > 4096 {
			return PropositionRegisterRequest{}, errors.New("library: proposition embedding dimensions must be between 1 and 4096")
		}
		vectors := make([][]float32, len(request.Embeddings.Vectors))
		for index, vector := range request.Embeddings.Vectors {
			if len(vector) != request.Embeddings.Dimensions {
				return PropositionRegisterRequest{}, fmt.Errorf("library: proposition embedding %d has %d dimensions; want %d", index, len(vector), request.Embeddings.Dimensions)
			}
			var magnitude float64
			for _, value := range vector {
				if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
					return PropositionRegisterRequest{}, fmt.Errorf("library: proposition embedding %d contains a non-finite value", index)
				}
				magnitude += float64(value) * float64(value)
			}
			if magnitude == 0 {
				return PropositionRegisterRequest{}, fmt.Errorf("library: proposition embedding %d must not be zero", index)
			}
			vectors[index] = append([]float32(nil), vector...)
		}
		request.Embeddings.Vectors = vectors
	}
	return request, nil
}

func normalizeBoundedStrings(values []string, maximumItems, maximumRunes int, field string) ([]string, error) {
	if len(values) > maximumItems {
		return nil, fmt.Errorf("library: proposition %ss exceed %d items", field, maximumItems)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len([]rune(value)) > maximumRunes {
			return nil, fmt.Errorf("library: proposition %s exceeds %d characters", field, maximumRunes)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func newPropositionService(archive *sessionstore.Store) *propositionService {
	return &propositionService{archive: archive, now: time.Now}
}

func (s *propositionService) search(ctx context.Context, request PropositionSearchRequest) (PropositionSearchResponse, error) {
	if s == nil || s.archive == nil {
		return PropositionSearchResponse{}, errors.New("library: propositions are unavailable")
	}
	if len([]rune(request.Query)) > maximumPropositionQueryRunes {
		return PropositionSearchResponse{}, fmt.Errorf("library: proposition query exceeds %d characters", maximumPropositionQueryRunes)
	}
	if request.Limit == 0 {
		request.Limit = defaultPropositionSearchLimit
	}
	if request.Limit < 1 || request.Limit > maximumPropositionSearchLimit {
		return PropositionSearchResponse{}, fmt.Errorf("library: proposition search limit must be between 1 and %d", maximumPropositionSearchLimit)
	}
	if request.Offset < 0 || request.Offset > maximumPropositionSearchOffset-request.Limit {
		return PropositionSearchResponse{}, fmt.Errorf("library: proposition search result window must be between 0 and %d", maximumPropositionSearchOffset)
	}
	if request.CreatedAfter != nil && request.CreatedBefore != nil && request.CreatedAfter.After(*request.CreatedBefore) {
		return PropositionSearchResponse{}, errors.New("library: created_after is later than created_before")
	}

	weight := defaultPropositionRecencyWeight
	if request.RecencyWeight != nil {
		weight = *request.RecencyWeight
	}
	halfLifeHours := float64(defaultPropositionRecencyHalfLifeHour)
	if request.RecencyHalfLifeHours != nil {
		halfLifeHours = *request.RecencyHalfLifeHours
	}
	if math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 || weight > maximumPropositionRecencyWeight {
		return PropositionSearchResponse{}, fmt.Errorf("library: recency_weight must be between 0 and %d", maximumPropositionRecencyWeight)
	}
	if math.IsNaN(halfLifeHours) || math.IsInf(halfLifeHours, 0) || halfLifeHours <= 0 || halfLifeHours > maximumPropositionRecencyHalfLifeHour {
		return PropositionSearchResponse{}, fmt.Errorf("library: recency_half_life_hours must be greater than 0 and at most %d", maximumPropositionRecencyHalfLifeHour)
	}

	options := sessionstore.SearchOptions{
		Text: request.Query, Sort: sessionstore.SortRelevance, Limit: request.Limit, Offset: request.Offset,
		CreatedAfter: request.CreatedAfter, CreatedBefore: request.CreatedBefore,
		Filters: sessionstore.Filters{
			Kinds: []string{sessionstore.KindProposition}, Scopes: []string{"global"}, Tags: request.Tags,
		},
	}
	if len(request.Embedding) > 0 {
		options.Vector = &sessionstore.VectorQuery{Embedding: append([]float32(nil), request.Embedding...)}
	}
	if weight > 0 {
		now := time.Now().UTC()
		if s.now != nil {
			now = s.now().UTC()
		}
		options.Recency = &sessionstore.Recency{
			Weight: weight, HalfLife: time.Duration(halfLifeHours * float64(time.Hour)), Now: now,
		}
	}
	result, err := s.archive.Search(ctx, options)
	if err != nil {
		return PropositionSearchResponse{}, err
	}
	response := PropositionSearchResponse{
		Total: result.Total, Hits: make([]PropositionSearchHit, 0, len(result.Hits)),
		RecencyWeight: weight, RecencyHalfLifeHours: halfLifeHours,
	}
	for _, hit := range result.Hits {
		content, truncated := truncateRunes(hit.Record.Content, maximumPropositionSearchContentRunes)
		response.Hits = append(response.Hits, PropositionSearchHit{
			ID: hit.Record.ID, Content: content, Truncated: truncated, CreatedAt: hit.Record.CreatedAt,
			Refs: append([]string(nil), hit.Record.Refs...), Tags: append([]string(nil), hit.Record.Tags...), Score: hit.Score,
		})
	}
	return response, nil
}

func (s *propositionService) delete(id string) (PropositionDeleteResponse, error) {
	if s == nil || s.archive == nil {
		return PropositionDeleteResponse{}, errors.New("library: propositions are unavailable")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return PropositionDeleteResponse{}, errors.New("library: proposition ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.archive.Get(id)
	if errors.Is(err, sessionstore.ErrNotFound) {
		return PropositionDeleteResponse{ID: id, Deleted: false}, nil
	}
	if err != nil {
		return PropositionDeleteResponse{}, err
	}
	if record.Kind != sessionstore.KindProposition || record.Scope != "global" {
		return PropositionDeleteResponse{}, sessionstore.ErrNotFound
	}
	if err := s.archive.Delete(id); err != nil {
		return PropositionDeleteResponse{}, err
	}
	return PropositionDeleteResponse{ID: id, Deleted: true}, nil
}

func (s *propositionService) get(id string) (Proposition, error) {
	if s == nil || s.archive == nil {
		return Proposition{}, errors.New("library: propositions are unavailable")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Proposition{}, errors.New("library: proposition ID is required")
	}
	record, err := s.archive.Get(id)
	if err != nil {
		return Proposition{}, err
	}
	if record.Kind != sessionstore.KindProposition || record.Scope != "global" {
		return Proposition{}, sessionstore.ErrNotFound
	}
	var payload propositionStoredPayload
	if len(record.Payload) > 0 {
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return Proposition{}, fmt.Errorf("library: decode proposition payload: %w", err)
		}
	}
	return Proposition{
		ID: record.ID, Content: record.Content, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		Refs: append([]string(nil), record.Refs...), Tags: append([]string(nil), record.Tags...), Payload: payload.PropositionPayload,
	}, nil
}

func registerPropositionRoutes(mux *http.ServeMux, authenticator func(http.Handler) http.Handler, propositions *propositionService) {
	mux.Handle("POST /v1/propositions", authenticator(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input PropositionRegisterRequest
		if err := decodeLibraryJSON(writer, request, &input); err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		output, err := propositions.register(request.Header.Get("Idempotency-Key"), input)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errPropositionIdempotencyConflict) {
				status = http.StatusConflict
			}
			writeLibraryError(writer, status, err)
			return
		}
		status := http.StatusOK
		if output.Created {
			status = http.StatusCreated
		}
		writeJSON(writer, status, output)
	})))
	mux.Handle("POST /v1/propositions/search", authenticator(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input PropositionSearchRequest
		if err := decodeLibraryJSON(writer, request, &input); err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		output, err := propositions.search(request.Context(), input)
		if err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSON(writer, http.StatusOK, output)
	})))
	mux.Handle("GET /v1/propositions/{id}", authenticator(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		output, err := propositions.get(request.PathValue("id"))
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, sessionstore.ErrNotFound) {
				status = http.StatusNotFound
			}
			writeLibraryError(writer, status, err)
			return
		}
		writeJSON(writer, http.StatusOK, output)
	})))
	mux.Handle("DELETE /v1/propositions/{id}", authenticator(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		output, err := propositions.delete(request.PathValue("id"))
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, sessionstore.ErrNotFound) {
				status = http.StatusNotFound
			}
			writeLibraryError(writer, status, err)
			return
		}
		writeJSON(writer, http.StatusOK, output)
	})))
}

func truncateRunes(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]), true
}
