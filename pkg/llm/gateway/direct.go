package gateway

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func applyModelBehavior(behaviors map[string]ModelBehavior, externalModel string, req llmprovider.ChatRequest) (llmprovider.ChatRequest, ModelBehavior) {
	b, ok := behaviors[externalModel]
	if !ok {
		return req, ModelBehavior{}
	}
	if b.OutputTokenField == "max_tokens" {
		if req.MaxTokens == nil && req.MaxCompletionTokens != nil {
			v := *req.MaxCompletionTokens
			req.MaxTokens = &v
			req.MaxCompletionTokens = nil
		} else if req.MaxTokens == nil && b.OutputLimit > 0 {
			limit := b.OutputLimit
			req.MaxTokens = &limit
		}
	} else if req.MaxTokens == nil && req.MaxCompletionTokens == nil && b.OutputLimit > 0 {
		limit := b.OutputLimit
		req.MaxCompletionTokens = &limit
	}

	if b.Sampling != nil {
		s := b.Sampling
		if req.Temperature == nil && s.Temperature != nil {
			v := *s.Temperature
			req.Temperature = &v
		}
		if req.TopP == nil && s.TopP != nil {
			v := *s.TopP
			req.TopP = &v
		}
		setExtra := func(key string, val any) {
			if req.Extra == nil {
				req.Extra = map[string]any{}
			} else if _, exists := req.Extra[key]; exists {
				return
			}
			req.Extra[key] = val
		}
		if s.TopK != nil || s.MinP != nil || s.RepetitionPenalty != nil || s.PresencePenalty != nil || s.FrequencyPenalty != nil {
			if req.Extra != nil {
				req.Extra = maps.Clone(req.Extra)
			}
			if s.TopK != nil {
				setExtra("top_k", *s.TopK)
			}
			if s.MinP != nil {
				setExtra("min_p", *s.MinP)
			}
			if s.RepetitionPenalty != nil {
				setExtra("repetition_penalty", *s.RepetitionPenalty)
			}
			if s.PresencePenalty != nil {
				setExtra("presence_penalty", *s.PresencePenalty)
			}
			if s.FrequencyPenalty != nil {
				setExtra("frequency_penalty", *s.FrequencyPenalty)
			}
		}
	}
	return req, b
}

// Chat executes a synchronous chat completion against the resolved provider route,
// applying Gateway model behaviors, prompt-cache preparation, and restoring the prefixed model ID.
func (g *Gateway) Chat(ctx context.Context, request llmprovider.ChatRequest) (*llmprovider.ChatResponse, error) {
	if g == nil {
		return nil, errors.New("gateway: gateway is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	route, backendModel, err := g.resolve(request.Model)
	if err != nil {
		return nil, err
	}
	externalModel := request.Model
	request, behavior := applyModelBehavior(g.modelBehaviors, externalModel, request)
	request.Model = backendModel
	request, cacheConversationID, err := preparePromptCache(route, request)
	if err != nil {
		return nil, err
	}
	response, err := route.provider.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	response.Model = externalModel
	if behavior.AllowMissingFinishReason && len(response.Choices) > 0 && response.Choices[0].FinishReason == "" {
		response.Choices[0].FinishReason = "stop"
	}
	if response.ConversationID == "" {
		response.ConversationID = cacheConversationID
	}
	return response, nil
}

// ChatStream starts a streaming chat completion against the resolved provider route,
// applying Gateway model behaviors, prompt-cache preparation, and rewriting streamed model/conversation metadata.
func (g *Gateway) ChatStream(ctx context.Context, request llmprovider.ChatRequest) (llmprovider.Stream, error) {
	if g == nil {
		return nil, errors.New("gateway: gateway is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	route, backendModel, err := g.resolve(request.Model)
	if err != nil {
		return nil, err
	}
	externalModel := request.Model
	request, _ = applyModelBehavior(g.modelBehaviors, externalModel, request)
	request.Model = backendModel
	request, cacheConversationID, err := preparePromptCache(route, request)
	if err != nil {
		return nil, err
	}
	stream, err := route.provider.ChatStream(ctx, request)
	if err != nil {
		return nil, err
	}
	return &prefixedStream{
		inner:          stream,
		externalModel:  externalModel,
		conversationID: cacheConversationID,
	}, nil
}

// Embed executes an embeddings request against the resolved provider route.
func (g *Gateway) Embed(ctx context.Context, request llmprovider.EmbeddingRequest) (*llmprovider.EmbeddingResponse, error) {
	if g == nil {
		return nil, errors.New("gateway: gateway is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	route, backendModel, err := g.resolve(request.Model)
	if err != nil {
		return nil, err
	}
	embedder, ok := route.provider.(llmprovider.Embedder)
	if !ok {
		return nil, fmt.Errorf("gateway: provider %q does not support embeddings", route.id)
	}
	externalModel := request.Model
	request.Model = backendModel
	response, err := embedder.Embed(ctx, request)
	if err != nil {
		return nil, err
	}
	response.Model = externalModel
	return response, nil
}

type prefixedStream struct {
	inner          llmprovider.Stream
	externalModel  string
	conversationID string
}

func (s *prefixedStream) Recv() (*llmprovider.ChatChunk, error) {
	chunk, err := s.inner.Recv()
	if err != nil {
		return nil, err
	}
	chunk.Model = s.externalModel
	if chunk.ConversationID != "" {
		s.conversationID = chunk.ConversationID
	}
	chunk.ConversationID = s.conversationID
	return chunk, nil
}

func (s *prefixedStream) Close() error {
	return s.inner.Close()
}

func (s *prefixedStream) ResponseHeaders() http.Header {
	if headerer, ok := s.inner.(llmprovider.ResponseHeaderer); ok {
		return headerer.ResponseHeaders()
	}
	return nil
}
