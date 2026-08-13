package builtin

import (
	"context"
	"fmt"
	"strings"
	"time"

	qlibrary "github.com/snowmerak/q/library"
)

type PropositionLibrary interface {
	SearchPropositions(context.Context, qlibrary.PropositionSearchRequest) (qlibrary.PropositionSearchResponse, error)
	GetProposition(context.Context, string) (qlibrary.Proposition, error)
}

type SearchPropositionsInput struct {
	Query                string   `json:"query" jsonschema:"Keywords or a natural-language question to match against canonical propositions and their generated queries"`
	Tags                 []string `json:"tags,omitempty" jsonschema:"Optional tags that every result must match"`
	CreatedAfter         string   `json:"created_after,omitempty" jsonschema:"Optional inclusive RFC3339 lower bound for proposition creation time"`
	CreatedBefore        string   `json:"created_before,omitempty" jsonschema:"Optional inclusive RFC3339 upper bound for proposition creation time"`
	Limit                int      `json:"limit,omitempty" jsonschema:"Maximum results; defaults to 10 and must be at most 100"`
	Offset               int      `json:"offset,omitempty" jsonschema:"Result offset for pagination"`
	RecencyWeight        *float64 `json:"recency_weight,omitempty" jsonschema:"Optional created_at relevance boost from 0 to 100; defaults to 0.25 and explicit 0 disables it"`
	RecencyHalfLifeHours *float64 `json:"recency_half_life_hours,omitempty" jsonschema:"Optional positive recency half-life in hours; defaults to 720"`
}

type SearchPropositionsOutput = qlibrary.PropositionSearchResponse

type GetPropositionInput struct {
	ID string `json:"id" jsonschema:"Exact proposition ID returned by search_propositions"`
}

type GetPropositionOutput = qlibrary.Proposition

func searchPropositions(ctx context.Context, library PropositionLibrary, input SearchPropositionsInput) (SearchPropositionsOutput, error) {
	if library == nil {
		return SearchPropositionsOutput{}, fmt.Errorf("[E_PROPOSITION] q Library is unavailable")
	}
	createdAfter, err := parseOptionalRFC3339("created_after", input.CreatedAfter)
	if err != nil {
		return SearchPropositionsOutput{}, err
	}
	createdBefore, err := parseOptionalRFC3339("created_before", input.CreatedBefore)
	if err != nil {
		return SearchPropositionsOutput{}, err
	}
	output, err := library.SearchPropositions(ctx, qlibrary.PropositionSearchRequest{
		Query: input.Query, Tags: input.Tags, CreatedAfter: createdAfter, CreatedBefore: createdBefore,
		Limit: input.Limit, Offset: input.Offset, RecencyWeight: input.RecencyWeight,
		RecencyHalfLifeHours: input.RecencyHalfLifeHours,
	})
	if err != nil {
		return SearchPropositionsOutput{}, fmt.Errorf("[E_PROPOSITION] search global propositions: %w", err)
	}
	return output, nil
}

func getProposition(ctx context.Context, library PropositionLibrary, input GetPropositionInput) (GetPropositionOutput, error) {
	if library == nil {
		return GetPropositionOutput{}, fmt.Errorf("[E_PROPOSITION] q Library is unavailable")
	}
	if strings.TrimSpace(input.ID) == "" {
		return GetPropositionOutput{}, fmt.Errorf("[E_PROPOSITION] proposition ID is required")
	}
	output, err := library.GetProposition(ctx, input.ID)
	if err != nil {
		return GetPropositionOutput{}, fmt.Errorf("[E_PROPOSITION] get global proposition: %w", err)
	}
	return output, nil
}

func parseOptionalRFC3339(name, value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("[E_PROPOSITION] %s must be RFC3339: %w", name, err)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}
