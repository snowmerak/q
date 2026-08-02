package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	llmprovider "github.com/snowmerak/llm-provider"
	provideropenai "github.com/snowmerak/llm-provider/providers/openai"
)

// Config controls the OpenAI-compatible transport. Empty BaseURL and APIKey
// values let llm-provider use OPENAI_BASE_URL, OPENAI_API_KEY, and its standard
// OpenAI endpoint defaults.
type Config struct {
	BaseURL      string
	APIKey       string
	DefaultModel string
	HTTPClient   *http.Client
	Headers      http.Header
	BodyFields   map[string]any

	// DisableAPIKey explicitly suppresses OPENAI_API_KEY. This is useful for
	// local compatible servers when the environment contains an unrelated key.
	DisableAPIKey bool
}

// Client is an OpenAI-compatible client backed by llm-provider's OpenAI
// implementation. It is safe for concurrent calls when its HTTPClient is safe.
type Client struct {
	inner        *llmprovider.Client
	provider     *provideropenai.Provider
	defaultModel string
}

// New validates config and constructs an OpenAI-compatible client.
func New(config Config) (*Client, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	options := make([]provideropenai.Option, 0, 4+len(config.Headers)+len(config.BodyFields))
	if config.BaseURL != "" {
		options = append(options, provideropenai.WithBaseURL(strings.TrimRight(config.BaseURL, "/")))
	}
	if config.DisableAPIKey {
		options = append(options, provideropenai.WithAPIKey(""))
	} else if config.APIKey != "" {
		options = append(options, provideropenai.WithAPIKey(config.APIKey))
	}
	if config.HTTPClient != nil {
		options = append(options, provideropenai.WithHTTPClient(config.HTTPClient))
	}

	headerNames := make([]string, 0, len(config.Headers))
	for name := range config.Headers {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	for _, name := range headerNames {
		values := config.Headers.Values(name)
		if len(values) > 0 {
			options = append(options, provideropenai.WithHeader(name, values[len(values)-1]))
		}
	}
	bodyNames := make([]string, 0, len(config.BodyFields))
	for name := range config.BodyFields {
		bodyNames = append(bodyNames, name)
	}
	sort.Strings(bodyNames)
	for _, name := range bodyNames {
		options = append(options, provideropenai.WithBodyField(name, config.BodyFields[name]))
	}

	provider := provideropenai.New(options...)
	return &Client{
		inner:        llmprovider.New(provider),
		provider:     provider,
		defaultModel: config.DefaultModel,
	}, nil
}

// FromEnvironment creates a client using OPENAI_BASE_URL and OPENAI_API_KEY.
func FromEnvironment(defaultModel string) (*Client, error) {
	return New(Config{DefaultModel: defaultModel})
}

func validateConfig(config Config) error {
	if config.DisableAPIKey && config.APIKey != "" {
		return fmt.Errorf("client: APIKey and DisableAPIKey cannot be used together")
	}
	if config.BaseURL == "" {
		return nil
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil {
		return fmt.Errorf("client: invalid BaseURL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("client: BaseURL scheme must be http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("client: BaseURL must include a host")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("client: BaseURL must not include a query or fragment")
	}
	return nil
}

// Chat creates a non-streaming chat completion. DefaultModel is used when the
// request omits Model.
func (c *Client) Chat(ctx context.Context, request ChatRequest) (*ChatResponse, error) {
	request.Model = c.model(request.Model)
	return c.inner.Chat(ctx, request)
}

// ChatStream creates a streaming chat completion. The caller must close the
// returned stream.
func (c *Client) ChatStream(ctx context.Context, request ChatRequest) (Stream, error) {
	request.Model = c.model(request.Model)
	return c.inner.ChatStream(ctx, request)
}

// ListModels returns models exposed by the compatible endpoint.
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	return c.provider.ListModels(ctx)
}

// Embed creates embeddings, using DefaultModel when request.Model is empty.
func (c *Client) Embed(ctx context.Context, request EmbeddingRequest) (*EmbeddingResponse, error) {
	request.Model = c.model(request.Model)
	return c.provider.Embed(ctx, request)
}

// CreateResponse sends a native Responses API JSON object. If DefaultModel is
// configured and body omits model, the default is injected.
func (c *Client) CreateResponse(ctx context.Context, body json.RawMessage, headers http.Header) (*RawResponse, error) {
	prepared, err := c.prepareResponseBody(body)
	if err != nil {
		return nil, err
	}
	return c.provider.CreateResponse(ctx, prepared, headers)
}

// CreateResponseStream is the streaming counterpart to CreateResponse. The
// caller must close the returned stream.
func (c *Client) CreateResponseStream(ctx context.Context, body json.RawMessage, headers http.Header) (ResponseStream, error) {
	prepared, err := c.prepareResponseBody(body)
	if err != nil {
		return nil, err
	}
	return c.provider.CreateResponseStream(ctx, prepared, headers)
}

// Close releases provider resources.
func (c *Client) Close() error {
	return c.inner.Close()
}

func (c *Client) model(requestModel string) string {
	if requestModel != "" {
		return requestModel
	}
	return c.defaultModel
}

func (c *Client) prepareResponseBody(body json.RawMessage) (json.RawMessage, error) {
	if c.defaultModel == "" {
		return body, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("client: decode Responses API request: %w", err)
	}
	if model, _ := payload["model"].(string); model == "" {
		payload["model"] = c.defaultModel
	}
	prepared, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("client: encode Responses API request: %w", err)
	}
	return prepared, nil
}
