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
	propositionJudgeTimeout               = 90 * time.Second
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
	ID        string `json:"id,omitempty"`
	Action    string `json:"action"`
	Created   bool   `json:"created,omitempty"`
	Merged    bool   `json:"merged,omitempty"`
	Discarded bool   `json:"discarded,omitempty"`
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
	archive  *sessionstore.Store
	queue    *propositionQueue
	judge    PropositionJudge
	judgeErr error
	now      func() time.Time
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
}

func (s *propositionService) register(ctx context.Context, idempotencyKey string, request PropositionRegisterRequest) (PropositionRegisterResponse, error) {
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
	if s.queue == nil {
		return PropositionRegisterResponse{}, errors.New("library: proposition queue is unavailable")
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
	_, _, requestDigest, err := propositionRegistrationIdentity(idempotencyKey, normalized)
	if err != nil {
		return PropositionRegisterResponse{}, err
	}
	return s.queue.submit(ctx, idempotencyKey, requestDigest, normalized)
}

func propositionRegistrationIdentity(idempotencyKey string, request PropositionRegisterRequest) (string, string, string, error) {
	// Embeddings are a derived projection and may vary slightly across retries.
	// Idempotency is defined by the logical proposition request, not its vectors.
	digestInput := request
	digestInput.Embeddings = nil
	body, err := json.Marshal(digestInput)
	if err != nil {
		return "", "", "", fmt.Errorf("library: encode proposition registration: %w", err)
	}
	keySum := sha256.Sum256([]byte("q-library-proposition-v1\x00" + idempotencyKey))
	requestSum := sha256.Sum256(body)
	return "prop-" + hex.EncodeToString(keySum[:16]), hex.EncodeToString(keySum[:]), hex.EncodeToString(requestSum[:]), nil
}

func (s *propositionService) create(job propositionJob) (PropositionRegisterResponse, error) {
	id, idempotencyHash, requestDigest, err := propositionRegistrationIdentity(job.Key, job.Request)
	if err != nil {
		return PropositionRegisterResponse{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, getErr := s.archive.Get(id); getErr == nil {
		var payload propositionStoredPayload
		if existing.Kind != sessionstore.KindProposition || existing.Scope != "global" ||
			json.Unmarshal(existing.Payload, &payload) != nil || payload.IdempotencyHash != idempotencyHash ||
			payload.RequestDigest != requestDigest {
			return PropositionRegisterResponse{}, errPropositionIdempotencyConflict
		}
		return PropositionRegisterResponse{ID: existing.ID, Action: PropositionActionCreate, Created: false}, nil
	} else if !errors.Is(getErr, sessionstore.ErrNotFound) {
		return PropositionRegisterResponse{}, getErr
	}
	payload, err := json.Marshal(propositionStoredPayload{
		PropositionPayload: PropositionPayload{
			Queries: job.Request.Queries, Confidence: &job.Request.Confidence,
			ExtractorModel: job.Request.ExtractorModel, ExtractorVersion: job.Request.ExtractorVersion,
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
	if job.Request.Embeddings != nil {
		projections = make([]sessionstore.VectorProjection, 0, len(job.Request.Embeddings.Vectors))
		for index, vector := range job.Request.Embeddings.Vectors {
			projectionID := "content"
			if index > 0 {
				projectionID = fmt.Sprintf("query-%d", index-1)
			}
			projections = append(projections, sessionstore.VectorProjection{
				ID: projectionID,
				Embedding: sessionstore.Embedding{
					Model: job.Request.Embeddings.Model, Dimensions: job.Request.Embeddings.Dimensions,
					CreatedAt: now, Vector: append([]float32(nil), vector...),
				},
			})
		}
	}
	_, err = s.archive.Save(sessionstore.Record{
		ID: id, Kind: sessionstore.KindProposition, Scope: "global",
		Content: job.Request.Content, SearchText: strings.Join(job.Request.Queries, "\n"),
		CreatedAt: now, UpdatedAt: now, Refs: job.Request.Refs, Tags: job.Request.Tags,
		VectorProjections: projections, Payload: payload,
	})
	if err != nil {
		return PropositionRegisterResponse{}, err
	}
	return PropositionRegisterResponse{ID: id, Action: PropositionActionCreate, Created: true}, nil
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

func newPropositionService(parent context.Context, archive *sessionstore.Store, queue *propositionQueue, judge PropositionJudge, judgeErr error) *propositionService {
	ctx, cancel := context.WithCancel(parent)
	service := &propositionService{
		archive: archive, queue: queue, judge: judge, judgeErr: judgeErr, now: time.Now,
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
	go service.runQueue()
	return service
}

func (s *propositionService) close() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
}

func (s *propositionService) runQueue() {
	defer close(s.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		processed, err := s.processNext(s.ctx)
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			continue
		}
		if processed {
			continue
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.queue.notify:
		case <-ticker.C:
			_ = s.queue.cleanup(s.ctx)
		}
	}
}

func (s *propositionService) processNext(ctx context.Context) (bool, error) {
	job, found, err := s.queue.claim(ctx)
	if err != nil || !found {
		return found, err
	}
	decision := job.Decision
	if job.State == "running" {
		if s.judgeErr != nil {
			err = fmt.Errorf("library: initialize proposition judge: %w", s.judgeErr)
		} else if s.judge == nil {
			err = errors.New("library: proposition judge is not configured")
		} else {
			var candidates []PropositionSearchHit
			candidates, err = s.similarCandidates(ctx, job.Request)
			if err == nil {
				judgeContext, cancelJudge := context.WithTimeout(ctx, propositionJudgeTimeout)
				decision, err = s.judge.JudgeProposition(judgeContext, job.Request, candidates)
				cancelJudge()
			}
			if err == nil {
				err = validatePropositionDecision(decision, candidates)
			}
		}
		if err == nil {
			err = s.queue.saveDecision(ctx, job.Key, decision)
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		_ = s.queue.fail(context.Background(), job.Key, err)
		return true, nil
	}
	job.Decision = decision
	response, err := s.applyDecision(job)
	if err != nil {
		_ = s.queue.fail(context.Background(), job.Key, err)
		return true, nil
	}
	if err := s.queue.complete(ctx, job, response); err != nil {
		return true, err
	}
	_ = s.queue.cleanup(ctx)
	return true, nil
}

func (s *propositionService) similarCandidates(ctx context.Context, request PropositionRegisterRequest) ([]PropositionSearchHit, error) {
	zero := 0.0
	search := PropositionSearchRequest{Query: request.Content, Limit: 5, RecencyWeight: &zero}
	if request.Embeddings != nil && len(request.Embeddings.Vectors) > 0 {
		search.Embedding = append([]float32(nil), request.Embeddings.Vectors[0]...)
	}
	result, err := s.search(ctx, search)
	if err != nil {
		return nil, err
	}
	return result.Hits, nil
}

func (s *propositionService) applyDecision(job propositionJob) (PropositionRegisterResponse, error) {
	switch job.Decision.Action {
	case PropositionActionCreate:
		return s.create(job)
	case PropositionActionMerge:
		return s.merge(job.Decision.TargetID, job.Request)
	case PropositionActionDiscard:
		return PropositionRegisterResponse{
			ID: job.Decision.TargetID, Action: PropositionActionDiscard, Discarded: true,
		}, nil
	default:
		return PropositionRegisterResponse{}, fmt.Errorf("library: unsupported queued proposition action %q", job.Decision.Action)
	}
}

func (s *propositionService) merge(id string, request PropositionRegisterRequest) (PropositionRegisterResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.archive.Get(id)
	if err != nil {
		return PropositionRegisterResponse{}, err
	}
	if record.Kind != sessionstore.KindProposition || record.Scope != "global" {
		return PropositionRegisterResponse{}, sessionstore.ErrNotFound
	}
	var payload propositionStoredPayload
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return PropositionRegisterResponse{}, fmt.Errorf("library: decode proposition payload: %w", err)
	}
	existingQueries := append([]string(nil), payload.Queries...)
	payload.Queries = mergeBoundedStrings(payload.Queries, request.Queries, maximumPropositionQueries)
	if payload.Confidence == nil || request.Confidence > *payload.Confidence {
		confidence := request.Confidence
		payload.Confidence = &confidence
	}
	record.Refs = mergeBoundedStrings(record.Refs, request.Refs, maximumPropositionRefs)
	record.Tags = mergeBoundedStrings(record.Tags, request.Tags, maximumPropositionTags)
	record.SearchText = strings.Join(payload.Queries, "\n")
	record.VectorProjections = mergeQueryProjections(record.VectorProjections, existingQueries, payload.Queries, request)
	record.UpdatedAt = time.Time{}
	record.Payload, err = json.Marshal(payload)
	if err != nil {
		return PropositionRegisterResponse{}, err
	}
	if _, err := s.archive.Save(record); err != nil {
		return PropositionRegisterResponse{}, err
	}
	return PropositionRegisterResponse{ID: id, Action: PropositionActionMerge, Merged: true}, nil
}

func mergeBoundedStrings(existing, additions []string, maximum int) []string {
	result := append([]string(nil), existing...)
	seen := make(map[string]struct{}, len(result))
	for _, value := range result {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if len(result) >= maximum {
			break
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func mergeQueryProjections(
	existing []sessionstore.VectorProjection,
	existingQueries, mergedQueries []string,
	request PropositionRegisterRequest,
) []sessionstore.VectorProjection {
	byQuery := make(map[string]sessionstore.Embedding)
	var content *sessionstore.VectorProjection
	for _, projection := range existing {
		if projection.ID == "content" {
			copy := projection
			content = &copy
			continue
		}
		var index int
		if _, err := fmt.Sscanf(projection.ID, "query-%d", &index); err == nil && index >= 0 && index < len(existingQueries) {
			byQuery[existingQueries[index]] = projection.Embedding
		}
	}
	if request.Embeddings != nil {
		for index, query := range request.Queries {
			if _, exists := byQuery[query]; exists {
				continue
			}
			vectorIndex := index + 1
			if vectorIndex < len(request.Embeddings.Vectors) {
				byQuery[query] = sessionstore.Embedding{
					Model: request.Embeddings.Model, Dimensions: request.Embeddings.Dimensions,
					Vector: append([]float32(nil), request.Embeddings.Vectors[vectorIndex]...),
				}
			}
		}
	}
	result := make([]sessionstore.VectorProjection, 0, len(mergedQueries)+1)
	if content != nil {
		result = append(result, *content)
	}
	for index, query := range mergedQueries {
		if embedding, ok := byQuery[query]; ok {
			result = append(result, sessionstore.VectorProjection{ID: fmt.Sprintf("query-%d", index), Embedding: embedding})
		}
	}
	return result
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
		output, err := propositions.register(request.Context(), request.Header.Get("Idempotency-Key"), input)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errPropositionIdempotencyConflict) {
				status = http.StatusConflict
			} else {
				var jobFailure *propositionJobFailure
				if errors.As(err, &jobFailure) {
					status = http.StatusServiceUnavailable
				}
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
