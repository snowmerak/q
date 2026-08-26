package subagent

import (
	"context"
	"errors"
	"strings"

	"github.com/snowmerak/q/client"
)

const ExternalSearchToolName = "external_search"

type ExternalSearchInput struct {
	Query              string   `json:"query"`
	Context            []string `json:"context,omitempty"`
	CompletionCriteria []string `json:"completion_criteria,omitempty"`
}

type ExternalSearchResult struct {
	Agent       string   `json:"agent"`
	Summary     string   `json:"summary"`
	Sources     []string `json:"sources,omitempty"`
	Uncertainty []string `json:"uncertainty,omitempty"`
}

type ExternalSearchFunc func(context.Context, ExternalSearchInput) (ExternalSearchResult, error)

func externalSearchTool() client.Tool {
	strict := true
	stringsSchema := stringArraySchemaValue()
	return client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name:        ExternalSearchToolName,
		Description: "Ask the Search role's configured ACP agent to gather current external information. The ACP session is isolated and deleted after this call.",
		Strict:      &strict,
		Parameters: map[string]any{
			"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string"}, "context": stringsSchema,
				"completion_criteria": stringsSchema,
			},
			"required": []string{"query"}, "additionalProperties": false,
		},
	}}
}

func parseExternalSearchInput(arguments string) (ExternalSearchInput, error) {
	var input ExternalSearchInput
	if err := decodeStrict(arguments, &input); err != nil {
		return ExternalSearchInput{}, err
	}
	input.Query = strings.TrimSpace(input.Query)
	input.Context = cleanStrings(input.Context)
	input.CompletionCriteria = cleanStrings(input.CompletionCriteria)
	if input.Query == "" {
		return ExternalSearchInput{}, errors.New("external_search query is required")
	}
	return input, nil
}
