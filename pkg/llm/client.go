package llmprovider

import "context"

// Client delegates requests to one provider.
type Client struct {
	provider Provider
}

// New creates a client backed by provider.
func New(provider Provider) *Client {
	if provider == nil {
		panic("llmprovider: nil provider")
	}
	return &Client{provider: provider}
}

// NewWithProvider is retained as a descriptive alias for New.
func NewWithProvider(provider Provider) *Client {
	return New(provider)
}

func (c *Client) Chat(ctx context.Context, request ChatRequest) (*ChatResponse, error) {
	return c.provider.Chat(ctx, request)
}

func (c *Client) ChatStream(ctx context.Context, request ChatRequest) (Stream, error) {
	return c.provider.ChatStream(ctx, request)
}

func (c *Client) Close() error {
	return c.provider.Close()
}
