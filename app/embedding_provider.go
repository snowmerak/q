package app

import (
	"context"
	"errors"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/library"
)

type currentEmbeddingProvider struct {
	store      config.Store
	model      string
	dimensions int
	provider   library.Embedder
}

func (provider *currentEmbeddingProvider) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	value, err := provider.store.Load()
	if err != nil {
		return err
	}
	if value.Embedding.Model != provider.model || value.Embedding.Dimensions != provider.dimensions {
		return errors.New("embedding configuration changed during sync; retry with the current settings")
	}
	return nil
}

func (provider *currentEmbeddingProvider) Embed(ctx context.Context, request client.EmbeddingRequest) (*client.EmbeddingResponse, error) {
	if err := provider.check(ctx); err != nil {
		return nil, err
	}
	if request.Model != provider.model {
		return nil, errors.New("embedding request does not match the current sync model")
	}
	response, err := provider.provider.Embed(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := provider.check(ctx); err != nil {
		return nil, err
	}
	return response, nil
}
