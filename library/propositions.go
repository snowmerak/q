package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
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
)

// PropositionSearchRequest searches durable global propositions. Nil recency
// values select the Library defaults; an explicit zero weight disables the
// created_at boost for one request.
type PropositionSearchRequest struct {
	Query                string     `json:"query"`
	Tags                 []string   `json:"tags,omitempty"`
	CreatedAfter         *time.Time `json:"created_after,omitempty"`
	CreatedBefore        *time.Time `json:"created_before,omitempty"`
	Limit                int        `json:"limit,omitempty"`
	Offset               int        `json:"offset,omitempty"`
	RecencyWeight        *float64   `json:"recency_weight,omitempty"`
	RecencyHalfLifeHours *float64   `json:"recency_half_life_hours,omitempty"`
}

type PropositionPayload struct {
	Queries          []string `json:"queries,omitempty"`
	Confidence       *float64 `json:"confidence,omitempty"`
	ExtractorModel   string   `json:"extractor_model,omitempty"`
	ExtractorVersion string   `json:"extractor_version,omitempty"`
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
	var payload PropositionPayload
	if len(record.Payload) > 0 {
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return Proposition{}, fmt.Errorf("library: decode proposition payload: %w", err)
		}
	}
	return Proposition{
		ID: record.ID, Content: record.Content, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		Refs: append([]string(nil), record.Refs...), Tags: append([]string(nil), record.Tags...), Payload: payload,
	}, nil
}

func registerPropositionRoutes(mux *http.ServeMux, authenticator func(http.Handler) http.Handler, propositions *propositionService) {
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
}

func truncateRunes(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]), true
}
