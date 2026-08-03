package sessionstore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
	blevequery "github.com/blevesearch/bleve/v2/search/query"
)

type SortOrder string

const (
	SortRelevance SortOrder = "relevance"
	SortNewest    SortOrder = "newest"
	SortOldest    SortOrder = "oldest"
)

// Filters are ANDed across fields and ORed within each field.
type Filters struct {
	RunIDs    []string
	TaskIDs   []string
	ParentIDs []string
	Kinds     []string
	Roles     []string
	Models    []string
	Efforts   []string
	Statuses  []string
	Refs      []string
	Tags      []string
}

// Recency boosts text relevance using a half-life decay. CandidateLimit bounds
// the Bleve candidate set reranked in memory; zero selects a conservative
// default.
type Recency struct {
	Weight         float64
	HalfLife       time.Duration
	Now            time.Time
	CandidateLimit int
}

type SearchOptions struct {
	Text          string
	Vector        *VectorQuery
	Filters       Filters
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	Sort          SortOrder
	Limit         int
	Offset        int
	Recency       *Recency
}

// VectorQuery enables HNSW semantic search. CandidateLimit bounds the
// pre-filter candidate set; zero chooses an oversampled value from the result
// window. Weight controls the vector branch during reciprocal-rank fusion and
// defaults to 1.
type VectorQuery struct {
	Embedding      []float32
	Weight         float64
	CandidateLimit int
}

type Hit struct {
	Record      Record
	Score       float64
	BaseScore   float64
	TextScore   float64
	VectorScore float64
}

type SearchResult struct {
	Total uint64
	Hits  []Hit
}

func (s *Store) Search(ctx context.Context, options SearchOptions) (SearchResult, error) {
	if ctx == nil {
		return SearchResult{}, errors.New("sessionstore: search context is nil")
	}
	if err := validateSearchOptions(&options); err != nil {
		return SearchResult{}, err
	}
	if options.Vector != nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		if err := s.requireOpenLocked(); err != nil {
			return SearchResult{}, err
		}
		return s.searchWithVectorLocked(ctx, options)
	}
	query := buildQuery(options)
	requestSize := options.Limit
	requestFrom := options.Offset
	if options.Recency != nil {
		requestFrom = 0
		requestSize = options.Recency.CandidateLimit
		if requestSize == 0 {
			requestSize = max(50, (options.Offset+options.Limit)*5)
			requestSize = min(requestSize, 5000)
		}
	}
	request := bleve.NewSearchRequestOptions(query, requestSize, requestFrom, false)
	switch options.Sort {
	case SortNewest:
		request.SortBy([]string{"-created_at", "-_score", "_id"})
	case SortOldest:
		request.SortBy([]string{"created_at", "-_score", "_id"})
	default:
		request.SortBy([]string{"-_score", "-created_at", "_id"})
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.requireOpenLocked(); err != nil {
		return SearchResult{}, err
	}
	result, err := s.index.SearchInContext(ctx, request)
	if err != nil {
		return SearchResult{}, fmt.Errorf("sessionstore: search Bleve index: %w", err)
	}
	hits := make([]Hit, 0, len(result.Hits))
	for _, indexed := range result.Hits {
		record, err := s.loadRecordLocked(indexed.ID)
		if err != nil {
			return SearchResult{}, fmt.Errorf("sessionstore: load search hit %q: %w", indexed.ID, err)
		}
		hits = append(hits, Hit{Record: record, Score: indexed.Score, BaseScore: indexed.Score})
	}
	if options.Recency != nil {
		rerankByRecency(hits, *options.Recency)
		start := min(options.Offset, len(hits))
		end := min(start+options.Limit, len(hits))
		hits = hits[start:end]
	}
	return SearchResult{Total: result.Total, Hits: hits}, nil
}

func validateSearchOptions(options *SearchOptions) error {
	if options.Vector != nil {
		vector := *options.Vector
		vector.Embedding = cloneFloats(vector.Embedding)
		options.Vector = &vector
	}
	if options.Limit == 0 {
		options.Limit = 20
	}
	if options.Limit < 1 || options.Limit > 1000 {
		return errors.New("sessionstore: search limit must be between 1 and 1000")
	}
	if options.Offset < 0 {
		return errors.New("sessionstore: search offset must not be negative")
	}
	if options.Sort == "" {
		options.Sort = SortRelevance
	}
	if options.Sort != SortRelevance && options.Sort != SortNewest && options.Sort != SortOldest {
		return fmt.Errorf("sessionstore: unsupported sort order %q", options.Sort)
	}
	if options.CreatedAfter != nil && options.CreatedBefore != nil && options.CreatedAfter.After(*options.CreatedBefore) {
		return errors.New("sessionstore: created_after is later than created_before")
	}
	if options.Recency != nil {
		if options.Sort != SortRelevance {
			return errors.New("sessionstore: recency weighting requires relevance sort")
		}
		if options.Recency.Weight < 0 {
			return errors.New("sessionstore: recency weight must not be negative")
		}
		if options.Recency.HalfLife <= 0 {
			return errors.New("sessionstore: recency half-life must be positive")
		}
		if options.Recency.CandidateLimit < 0 || options.Recency.CandidateLimit > 5000 {
			return errors.New("sessionstore: recency candidate limit must be between 0 and 5000")
		}
		if options.Offset > 5000-options.Limit {
			return errors.New("sessionstore: recency result window exceeds 5000 candidates")
		}
		if options.Recency.CandidateLimit > 0 && options.Recency.CandidateLimit < options.Offset+options.Limit {
			return errors.New("sessionstore: recency candidate limit is smaller than requested result window")
		}
	}
	if options.Vector != nil {
		if options.Sort != SortRelevance {
			return errors.New("sessionstore: vector search requires relevance sort")
		}
		if len(options.Vector.Embedding) == 0 {
			return errors.New("sessionstore: vector search requires an embedding")
		}
		if options.Vector.Weight < 0 {
			return errors.New("sessionstore: vector weight must not be negative")
		}
		if options.Vector.Weight == 0 {
			options.Vector.Weight = 1
		}
		if options.Vector.CandidateLimit < 0 || options.Vector.CandidateLimit > 5000 {
			return errors.New("sessionstore: vector candidate limit must be between 0 and 5000")
		}
		if options.Vector.CandidateLimit > 0 && options.Vector.CandidateLimit < options.Offset+options.Limit {
			return errors.New("sessionstore: vector candidate limit is smaller than requested result window")
		}
	}
	return nil
}

type fusedCandidate struct {
	record      Record
	textScore   float64
	vectorScore float64
	hasVector   bool
	baseScore   float64
}

func (s *Store) searchWithVectorLocked(ctx context.Context, options SearchOptions) (SearchResult, error) {
	if s.vectors == nil {
		return SearchResult{}, errors.New("sessionstore: vector search is not configured")
	}
	candidateLimit := options.Vector.CandidateLimit
	if candidateLimit == 0 {
		candidateLimit = max(100, (options.Offset+options.Limit)*8)
		candidateLimit = min(candidateLimit, 5000)
	}
	if options.Recency != nil && options.Recency.CandidateLimit > candidateLimit {
		candidateLimit = options.Recency.CandidateLimit
	}

	vectorResults, err := s.vectors.search(options.Vector.Embedding, candidateLimit)
	if err != nil {
		return SearchResult{}, err
	}
	candidates := make(map[string]*fusedCandidate, len(vectorResults)+candidateLimit)
	vectorRank := 0
	for _, result := range vectorResults {
		if err := ctx.Err(); err != nil {
			return SearchResult{}, err
		}
		record, err := s.loadRecordLocked(result.RecordID)
		if err != nil {
			return SearchResult{}, fmt.Errorf("sessionstore: load vector hit %q: %w", result.RecordID, err)
		}
		if !recordMatchesSearch(record, options) {
			continue
		}
		vectorRank++
		similarity := min(1, max(-1, result.Similarity))
		candidate := &fusedCandidate{record: record, vectorScore: (similarity + 1) / 2, hasVector: true}
		candidates[record.ID] = candidate
	}

	text := strings.TrimSpace(options.Text)
	if text != "" {
		request := bleve.NewSearchRequestOptions(buildQuery(options), candidateLimit, 0, false)
		request.SortBy([]string{"-_score", "-created_at", "_id"})
		result, err := s.index.SearchInContext(ctx, request)
		if err != nil {
			return SearchResult{}, fmt.Errorf("sessionstore: search Bleve index for hybrid search: %w", err)
		}
		for rank, indexed := range result.Hits {
			candidate := candidates[indexed.ID]
			if candidate == nil {
				record, err := s.loadRecordLocked(indexed.ID)
				if err != nil {
					return SearchResult{}, fmt.Errorf("sessionstore: load text hit %q: %w", indexed.ID, err)
				}
				candidate = &fusedCandidate{record: record}
				candidates[indexed.ID] = candidate
			}
			candidate.textScore = indexed.Score
			candidate.baseScore += 1 / float64(60+rank+1)
		}
		weight := options.Vector.Weight
		for rank, result := range vectorResults {
			if candidate := candidates[result.RecordID]; candidate != nil && candidate.hasVector {
				candidate.baseScore += weight / float64(60+rank+1)
			}
		}
	} else {
		for _, candidate := range candidates {
			candidate.baseScore = candidate.vectorScore
		}
	}

	hits := make([]Hit, 0, len(candidates))
	for _, candidate := range candidates {
		hits = append(hits, Hit{
			Record: candidate.record, Score: candidate.baseScore, BaseScore: candidate.baseScore,
			TextScore: candidate.textScore, VectorScore: candidate.vectorScore,
		})
	}
	sort.SliceStable(hits, func(left, right int) bool {
		if hits[left].Score != hits[right].Score {
			return hits[left].Score > hits[right].Score
		}
		if !hits[left].Record.CreatedAt.Equal(hits[right].Record.CreatedAt) {
			return hits[left].Record.CreatedAt.After(hits[right].Record.CreatedAt)
		}
		return hits[left].Record.ID < hits[right].Record.ID
	})
	if options.Recency != nil {
		rerankByRecency(hits, *options.Recency)
	}
	total := uint64(len(hits))
	start := min(options.Offset, len(hits))
	end := min(start+options.Limit, len(hits))
	return SearchResult{Total: total, Hits: hits[start:end]}, nil
}

func recordMatchesSearch(record Record, options SearchOptions) bool {
	matches := func(value string, allowed []string) bool {
		if len(allowed) == 0 {
			return true
		}
		for _, candidate := range allowed {
			if value == candidate {
				return true
			}
		}
		return false
	}
	intersects := func(values, allowed []string) bool {
		if len(allowed) == 0 {
			return true
		}
		for _, value := range values {
			if matches(value, allowed) {
				return true
			}
		}
		return false
	}
	filters := options.Filters
	if !matches(record.RunID, filters.RunIDs) || !matches(record.TaskID, filters.TaskIDs) ||
		!matches(record.ParentID, filters.ParentIDs) || !matches(record.Kind, filters.Kinds) ||
		!matches(record.Role, filters.Roles) || !matches(record.Model, filters.Models) ||
		!matches(record.Effort, filters.Efforts) || !matches(record.Status, filters.Statuses) ||
		!intersects(record.Refs, filters.Refs) || !intersects(record.Tags, filters.Tags) {
		return false
	}
	if options.CreatedAfter != nil && record.CreatedAt.Before(options.CreatedAfter.UTC()) {
		return false
	}
	if options.CreatedBefore != nil && record.CreatedAt.After(options.CreatedBefore.UTC()) {
		return false
	}
	return true
}

func buildQuery(options SearchOptions) blevequery.Query {
	conjuncts := make([]blevequery.Query, 0, 12)
	if text := strings.TrimSpace(options.Text); text != "" {
		summary := bleve.NewMatchQuery(text)
		summary.SetField("summary")
		content := bleve.NewMatchQuery(text)
		content.SetField("content")
		conjuncts = append(conjuncts, bleve.NewDisjunctionQuery(summary, content))
	}
	for field, values := range map[string][]string{
		"run_id": options.Filters.RunIDs, "task_id": options.Filters.TaskIDs,
		"parent_id": options.Filters.ParentIDs, "kind": options.Filters.Kinds,
		"role": options.Filters.Roles, "model": options.Filters.Models,
		"effort": options.Filters.Efforts, "status": options.Filters.Statuses,
		"refs": options.Filters.Refs, "tags": options.Filters.Tags,
	} {
		terms := make([]blevequery.Query, 0, len(values))
		for _, value := range values {
			term := bleve.NewTermQuery(value)
			term.SetField(field)
			terms = append(terms, term)
		}
		if len(terms) == 1 {
			conjuncts = append(conjuncts, terms[0])
		} else if len(terms) > 1 {
			conjuncts = append(conjuncts, bleve.NewDisjunctionQuery(terms...))
		}
	}
	if options.CreatedAfter != nil || options.CreatedBefore != nil {
		var start, end time.Time
		if options.CreatedAfter != nil {
			start = options.CreatedAfter.UTC()
		}
		if options.CreatedBefore != nil {
			end = options.CreatedBefore.UTC()
		}
		inclusive := true
		date := bleve.NewDateRangeInclusiveQuery(start, end, &inclusive, &inclusive)
		date.SetField("created_at")
		conjuncts = append(conjuncts, date)
	}
	if len(conjuncts) == 0 {
		return bleve.NewMatchAllQuery()
	}
	if len(conjuncts) == 1 {
		return conjuncts[0]
	}
	return bleve.NewConjunctionQuery(conjuncts...)
}

func rerankByRecency(hits []Hit, options Recency) {
	now := options.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	for index := range hits {
		age := now.Sub(hits[index].Record.CreatedAt)
		if age < 0 {
			age = 0
		}
		decay := math.Exp(-math.Ln2 * float64(age) / float64(options.HalfLife))
		hits[index].Score = hits[index].BaseScore * (1 + options.Weight*decay)
	}
	sort.SliceStable(hits, func(left, right int) bool {
		if hits[left].Score != hits[right].Score {
			return hits[left].Score > hits[right].Score
		}
		if !hits[left].Record.CreatedAt.Equal(hits[right].Record.CreatedAt) {
			return hits[left].Record.CreatedAt.After(hits[right].Record.CreatedAt)
		}
		return hits[left].Record.ID < hits[right].Record.ID
	})
}
