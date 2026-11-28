package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/snowmerak/q/v2/pkg/app/config"
	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/llm/gateway"
	"github.com/snowmerak/q/v2/pkg/memory"
	"github.com/snowmerak/q/v2/pkg/storage/outbox"
)

type appEmbedder interface {
	outbox.Embedder
	memory.QueryEmbedder
}

type gatewayEmbedder struct {
	mg             *managedGateway
	routedModel    string
	dimensions     int
	queryPrefix    string
	documentPrefix string
	timeout        time.Duration
}

func newGatewayEmbedder(mg *managedGateway, provider gateway.ProviderConfig, cfg config.EmbeddingsConfig) (*gatewayEmbedder, error) {
	if mg == nil || mg.gw == nil {
		return nil, errors.New("app: gateway is required for provider_id embeddings")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, errors.New("app: embeddings model is required")
	}
	routedModel := model
	if prefix := strings.TrimSpace(provider.Prefix); prefix != "" && !strings.HasPrefix(model, prefix+"/") {
		routedModel = prefix + "/" + model
	}
	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &gatewayEmbedder{
		mg:             mg,
		routedModel:    routedModel,
		dimensions:     cfg.Dimensions,
		queryPrefix:    cfg.QueryPrefix,
		documentPrefix: cfg.DocumentPrefix,
		timeout:        timeout,
	}, nil
}

func (e *gatewayEmbedder) EmbedDocument(ctx context.Context, text string) (model string, dim int, vector []float32, err error) {
	if e == nil {
		return "", 0, nil, errors.New("app: nil gateway embedder")
	}
	vec, err := e.embed(ctx, e.documentPrefix+text)
	if err != nil {
		return "", 0, nil, err
	}
	return e.routedModel, len(vec), vec, nil
}

func (e *gatewayEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if e == nil {
		return nil, errors.New("app: nil gateway embedder")
	}
	return e.embed(ctx, e.queryPrefix+text)
}

func (e *gatewayEmbedder) embed(ctx context.Context, input string) ([]float32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !e.mg.Acquire() {
		return nil, errors.New("app: gateway closed for embeddings")
	}
	defer e.mg.Release()
	var cancel context.CancelFunc
	if e.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, e.timeout)
		defer cancel()
	}
	var dimPtr *int
	if e.dimensions > 0 {
		d := e.dimensions
		dimPtr = &d
	}
	resp, err := e.mg.gw.Embed(ctx, llmprovider.EmbeddingRequest{
		Model:      e.routedModel,
		Input:      input,
		Dimensions: dimPtr,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || len(resp.Data) == 0 {
		return nil, errors.New("app: gateway embedding response contained no vectors")
	}
	return decodeEmbeddingVector(resp.Data[0].Embedding)
}

func decodeEmbeddingVector(raw any) ([]float32, error) {
	switch v := raw.(type) {
	case []float32:
		if len(v) == 0 {
			return nil, errors.New("app: gateway embedding vector is empty")
		}
		return v, nil
	case []float64:
		if len(v) == 0 {
			return nil, errors.New("app: gateway embedding vector is empty")
		}
		out := make([]float32, len(v))
		for i, f := range v {
			out[i] = float32(f)
		}
		return out, nil
	case []any:
		if len(v) == 0 {
			return nil, errors.New("app: gateway embedding vector is empty")
		}
		out := make([]float32, len(v))
		for i, item := range v {
			switch num := item.(type) {
			case float64:
				out[i] = float32(num)
			case float32:
				out[i] = num
			case json.Number:
				f, err := num.Float64()
				if err != nil {
					return nil, fmt.Errorf("app: decode embedding element %d: %w", i, err)
				}
				out[i] = float32(f)
			default:
				return nil, fmt.Errorf("app: unexpected embedding element type %T at index %d", item, i)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("app: unexpected embedding vector type %T", raw)
	}
}
