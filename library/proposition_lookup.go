package library

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	maximumPropositionJudgeSearches = 3
	maximumPropositionJudgeGets     = 5
	propositionJudgeSearchLimit     = 5
	maximumPropositionJudgeBytes    = 128 * 1024
)

// PropositionLookup is the read-only surface available during one judgment.
// The Library bounds reads and tracks returned candidates for final validation.
// Search uses Query and Embedding, fixes the limit at five without recency,
// and ignores other search options. Detail reads require a returned candidate.
// Calls are sequential and must finish within the judgment's context.
type PropositionLookup interface {
	SearchPropositions(context.Context, PropositionSearchRequest) (PropositionSearchResponse, error)
	GetProposition(context.Context, string) (Proposition, error)
}

// PropositionRetrievalJudge optionally extends PropositionJudge with retrieval.
// Existing judges can continue deciding from the initial candidates alone.
type PropositionRetrievalJudge interface {
	JudgePropositionWithRetrieval(context.Context, PropositionRegisterRequest, []PropositionSearchHit, PropositionLookup) (PropositionDecision, error)
}

type propositionLookup struct {
	service    *propositionService
	candidates []PropositionSearchHit
	searches   int
	gets       int
}

func (l *propositionLookup) SearchPropositions(ctx context.Context, request PropositionSearchRequest) (PropositionSearchResponse, error) {
	if err := ctx.Err(); err != nil {
		return PropositionSearchResponse{}, err
	}
	if l.searches >= maximumPropositionJudgeSearches {
		return PropositionSearchResponse{}, errors.New("library: proposition judge search budget exhausted")
	}
	l.searches++
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" || len([]rune(request.Query)) > maximumPropositionQueryRunesPerItem {
		return PropositionSearchResponse{}, fmt.Errorf("library: judge query must contain 1 to %d characters", maximumPropositionQueryRunesPerItem)
	}
	// Judgment searches must also find older facts; callers cannot change the
	// ranking policy or expand the result window through this capability.
	zero := 0.0
	result, err := l.service.search(ctx, PropositionSearchRequest{
		Query: request.Query, Embedding: request.Embedding,
		Limit: propositionJudgeSearchLimit, RecencyWeight: &zero,
	})
	if err != nil {
		return PropositionSearchResponse{}, err
	}
	l.candidates = append(l.candidates, result.Hits...)
	return result, nil
}

func (l *propositionLookup) GetProposition(ctx context.Context, id string) (Proposition, error) {
	if err := ctx.Err(); err != nil {
		return Proposition{}, err
	}
	if l.gets >= maximumPropositionJudgeGets {
		return Proposition{}, errors.New("library: proposition judge detail budget exhausted")
	}
	l.gets++
	id = strings.TrimSpace(id)
	for _, candidate := range l.candidates {
		if candidate.ID == id {
			return l.service.get(id)
		}
	}
	return Proposition{}, fmt.Errorf("library: proposition detail selected unknown candidate %q", id)
}
