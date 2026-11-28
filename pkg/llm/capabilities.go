package llmprovider

import (
	"context"
	"encoding/json"
	"net/http"
)

// EmbeddingRequest follows the OpenAI-compatible embeddings request shape.
// Input intentionally remains an open JSON value so callers can use text,
// batches of text, token arrays, or batches of token arrays.
type EmbeddingRequest struct {
	Model          string         `json:"model"`
	Input          any            `json:"input"`
	EncodingFormat string         `json:"encoding_format,omitempty"`
	Dimensions     *int           `json:"dimensions,omitempty"`
	User           string         `json:"user,omitempty"`
	Extra          map[string]any `json:"-"`
	Headers        http.Header    `json:"-"`
}

type Embedding struct {
	Object    string `json:"object"`
	Embedding any    `json:"embedding"`
	Index     int    `json:"index"`
}

type EmbeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type EmbeddingResponse struct {
	Object  string          `json:"object"`
	Data    []Embedding     `json:"data"`
	Model   string          `json:"model"`
	Usage   EmbeddingUsage  `json:"usage"`
	Headers http.Header     `json:"-"`
	Raw     json.RawMessage `json:"-"`
}

// Embedder is an optional provider capability. Providers that do not expose
// an embeddings endpoint need not implement it.
type Embedder interface {
	Embed(context.Context, EmbeddingRequest) (*EmbeddingResponse, error)
}

// RawResponse is an unmodified OpenAI Responses API JSON document.
type RawResponse struct {
	Body    json.RawMessage
	Headers http.Header
}

type ResponseEvent struct {
	Event string
	Data  json.RawMessage
}

type ResponseStream interface {
	Recv() (*ResponseEvent, error)
	Close() error
}

// ResponsesProvider is an optional native Responses API capability. The
// gateway uses it when available and adapts the common subset to Chat for
// other providers.
type ResponsesProvider interface {
	CreateResponse(context.Context, json.RawMessage, http.Header) (*RawResponse, error)
	CreateResponseStream(context.Context, json.RawMessage, http.Header) (ResponseStream, error)
}

// OpenAIWireProvider marks a provider whose Chat Raw fields contain an
// OpenAI-compatible wire response and can therefore be forwarded losslessly.
type OpenAIWireProvider interface {
	OpenAICompatibleWire()
}

// APIError exposes a provider HTTP error without coupling the Gateway to a
// concrete provider package.
type APIError interface {
	error
	HTTPStatusCode() int
	APIErrorMessage() string
	APIErrorType() string
	APIErrorCode() any
}
