// Package memory implements observer-isolated episodic recall, post-turn
// experience extraction, character state appraisal, and background value consolidation.
package memory

import (
	"context"
	"errors"

	sdk "github.com/snowmerak/decisions"
	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

var (
	// ErrValuesTooLarge indicates consolidated or initial character values exceed their context budget.
	ErrValuesTooLarge = errors.New("memory: character values exceed budget")
)

// ChatCompleter executes synchronous LLM chat completions.
type ChatCompleter interface {
	Chat(ctx context.Context, req llmprovider.ChatRequest) (*llmprovider.ChatResponse, error)
}

// ModelLookup inspects discovered model metadata and capabilities.
type ModelLookup interface {
	Model(ctx context.Context, id string) (llmprovider.Model, error)
}

// DecisionEvaluator evaluates discrete predicate/score questions via Decisions.
type DecisionEvaluator interface {
	Evaluate(ctx context.Context, modelID string, input sdk.Input, questions sdk.Questions) (*sdk.Response, error)
}

// QueryEmbedder produces dense query vectors for hybrid search.
type QueryEmbedder interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// Deps bundles optional/required collaborators for memory operations.
type Deps struct {
	Chat      ChatCompleter
	Models    ModelLookup
	Decisions DecisionEvaluator
	Embedder  QueryEmbedder
}
