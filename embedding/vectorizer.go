// Package embedding provides validated, batched access to an OpenAI-compatible
// embedding provider.
package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/snowmerak/q/client"
)

const maximumDimensions = 4096

// Provider is the subset of an LLM client needed to generate embeddings.
type Provider interface {
	Embed(context.Context, client.EmbeddingRequest) (*client.EmbeddingResponse, error)
}

// Vectorizer binds one provider to a model and vector dimension.
type Vectorizer struct {
	provider   Provider
	model      string
	dimensions int
}

func New(provider Provider, model string, dimensions int) (*Vectorizer, error) {
	model = strings.TrimSpace(model)
	if provider == nil || model == "" || dimensions < 1 || dimensions > maximumDimensions {
		return nil, fmt.Errorf("embedding: provider, model, and dimensions between 1 and %d are required", maximumDimensions)
	}
	return &Vectorizer{provider: provider, model: model, dimensions: dimensions}, nil
}

func (v *Vectorizer) Model() string {
	if v == nil {
		return ""
	}
	return v.model
}

func (v *Vectorizer) Dimensions() int {
	if v == nil {
		return 0
	}
	return v.dimensions
}

// Embed returns vectors in the same order as texts and validates the provider
// response before exposing it to an index.
func (v *Vectorizer) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if v == nil || v.provider == nil {
		return nil, errors.New("embedding: vectorizer is not configured")
	}
	if ctx == nil {
		return nil, errors.New("embedding: context is nil")
	}
	if len(texts) == 0 {
		return nil, errors.New("embedding: input is empty")
	}
	dimensions := v.dimensions
	response, err := v.provider.Embed(ctx, client.EmbeddingRequest{
		Model: v.model, Input: append([]string(nil), texts...), Dimensions: &dimensions,
	})
	if err != nil {
		return nil, err
	}
	if response == nil || len(response.Data) != len(texts) {
		return nil, fmt.Errorf("embedding: response contains %d vectors; want %d", responseLength(response), len(texts))
	}
	data := append([]client.Embedding(nil), response.Data...)
	sort.Slice(data, func(left, right int) bool { return data[left].Index < data[right].Index })
	vectors := make([][]float32, len(texts))
	for position, item := range data {
		if item.Index != position {
			return nil, fmt.Errorf("embedding: response index %d is invalid; want %d", item.Index, position)
		}
		vector, err := decodeVector(item.Embedding)
		if err != nil {
			return nil, fmt.Errorf("embedding: response index %d: %w", item.Index, err)
		}
		if len(vector) != v.dimensions {
			return nil, fmt.Errorf("embedding: response index %d has %d dimensions; want %d", item.Index, len(vector), v.dimensions)
		}
		vectors[position] = vector
	}
	return vectors, nil
}

func responseLength(response *client.EmbeddingResponse) int {
	if response == nil {
		return 0
	}
	return len(response.Data)
}

func decodeVector(value any) ([]float32, error) {
	switch vector := value.(type) {
	case []float32:
		return append([]float32(nil), vector...), nil
	case []float64:
		result := make([]float32, len(vector))
		for index, component := range vector {
			result[index] = float32(component)
		}
		return result, nil
	case []any:
		result := make([]float32, len(vector))
		for index, component := range vector {
			number, ok := component.(float64)
			if !ok {
				return nil, fmt.Errorf("component %d is %T, not a number", index, component)
			}
			result[index] = float32(number)
		}
		return result, nil
	case json.RawMessage:
		var decoded []float32
		if err := json.Unmarshal(vector, &decoded); err != nil {
			return nil, fmt.Errorf("decode vector: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("unsupported vector encoding %T", value)
	}
}
