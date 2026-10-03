package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/snowmerak/q/client"
)

func propositionJudgeTools(retrieval bool) []client.Tool {
	strict := true
	tools := []client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name: "resolve_proposition", Description: "Choose the final disposition for the proposal, after any needed retrieval.", Strict: &strict,
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"action":    map[string]any{"type": "string", "enum": []string{PropositionActionCreate, PropositionActionMerge, PropositionActionDiscard}},
				"target_id": map[string]any{"type": "string"},
				"reason":    map[string]any{"type": "string", "maxLength": 1024},
			},
			"required": []string{"action", "target_id", "reason"},
		},
	}}}
	if !retrieval {
		return tools
	}
	return append(tools,
		client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
			Name: "search_propositions", Description: "Find existing memories using a revised query. Returns up to 5 candidates without a recency boost. At most 3 searches per judgment.", Strict: &strict,
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"query": map[string]any{"type": "string", "minLength": 1, "maxLength": maximumPropositionQueryRunesPerItem}},
				"required":   []string{"query"},
			},
		}},
		client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
			Name: "get_proposition", Description: "Read a candidate already returned in this judgment, including provenance and extraction metadata. At most 5 detail reads per judgment.", Strict: &strict,
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"id": map[string]any{"type": "string", "minLength": 1}},
				"required":   []string{"id"},
			},
		}},
	)
}

func decodePropositionJudgeArguments(input string, value any) error {
	if len(input) > 16*1024 {
		return errors.New("library: proposition judge tool arguments exceed 16 KiB")
	}
	if !strings.HasPrefix(strings.TrimSpace(input), "{") {
		return errors.New("library: proposition judge tool arguments must be an object")
	}
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("library: decode proposition judge tool arguments: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("library: proposition judge tool arguments contain trailing data")
	}
	return nil
}

func (j *modelPropositionJudge) searchPropositions(ctx context.Context, lookup PropositionLookup, arguments string) (PropositionSearchResponse, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := decodePropositionJudgeArguments(arguments, &args); err != nil {
		return PropositionSearchResponse{}, err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" || len([]rune(query)) > maximumPropositionQueryRunesPerItem {
		return PropositionSearchResponse{}, fmt.Errorf("library: judge query must contain 1 to %d characters", maximumPropositionQueryRunesPerItem)
	}
	zero := 0.0
	request := PropositionSearchRequest{Query: query, Limit: propositionJudgeSearchLimit, RecencyWeight: &zero}
	if j.embedding.provider != nil {
		vectors, err := embedTexts(ctx, j.embedding, []string{query})
		if err != nil {
			return PropositionSearchResponse{}, fmt.Errorf("library: embed judge query: %w", err)
		}
		request.Embedding = vectors[0]
	}
	return lookup.SearchPropositions(ctx, request)
}
