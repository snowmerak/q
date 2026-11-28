package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

// Chat executes a synchronous chat completion against the resolved provider route,
// applying Gateway prompt-cache preparation and restoring the prefixed model ID.
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
	if response.ConversationID == "" {
		response.ConversationID = cacheConversationID
	}
	return response, nil
}

// ChatStream starts a streaming chat completion against the resolved provider route,
// applying Gateway prompt-cache preparation and rewriting streamed model/conversation metadata.
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
