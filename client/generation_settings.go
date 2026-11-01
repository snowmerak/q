package client

import "context"

func (c *Client) GenerationSettings(ctx context.Context, model string) (GenerationSettings, error) {
	if c.generationConfig != nil {
		return c.generationConfig(ctx, c.model(model))
	}
	return GenerationSettings{MaxAttempts: 2, SafeReplay: true, RequireFinishReason: true}, nil
}
