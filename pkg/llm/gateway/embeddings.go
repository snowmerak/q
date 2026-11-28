package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func (g *Gateway) handleEmbeddings(writer http.ResponseWriter, request *http.Request) {
	embeddingRequest, err := decodeEmbeddingRequest(request.Body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	route, backendModel, err := g.resolve(embeddingRequest.Model)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	embedder, ok := route.provider.(llmprovider.Embedder)
	if !ok {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("provider %q does not support embeddings", route.id))
		return
	}
	externalModel := embeddingRequest.Model
	embeddingRequest.Model = backendModel
	embeddingRequest.Headers = selectedHeaders(request.Header, route.forwardHeaders)
	response, err := embedder.Embed(request.Context(), embeddingRequest)
	if err != nil {
		writeProviderError(writer, err)
		return
	}
	response.Model = externalModel
	copySelectedHeaders(writer.Header(), response.Headers, route.forwardResponseHeaders)
	if len(response.Raw) > 0 {
		writeRawJSON(writer, http.StatusOK, rewriteTopLevelModel(response.Raw, externalModel))
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func decodeEmbeddingRequest(reader io.Reader) (llmprovider.EmbeddingRequest, error) {
	data, err := io.ReadAll(io.LimitReader(reader, 16<<20))
	if err != nil {
		return llmprovider.EmbeddingRequest{}, fmt.Errorf("read request: %w", err)
	}
	var wire struct {
		Model          string          `json:"model"`
		Input          json.RawMessage `json:"input"`
		EncodingFormat string          `json:"encoding_format"`
		Dimensions     *int            `json:"dimensions"`
		User           string          `json:"user"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return llmprovider.EmbeddingRequest{}, fmt.Errorf("decode request: %w", err)
	}
	if wire.Model == "" {
		return llmprovider.EmbeddingRequest{}, errors.New("model is required")
	}
	if len(wire.Input) == 0 || string(wire.Input) == "null" {
		return llmprovider.EmbeddingRequest{}, errors.New("input is required")
	}
	var input any
	if err := json.Unmarshal(wire.Input, &input); err != nil {
		return llmprovider.EmbeddingRequest{}, fmt.Errorf("decode input: %w", err)
	}
	var extra map[string]any
	if err := json.Unmarshal(data, &extra); err != nil {
		return llmprovider.EmbeddingRequest{}, fmt.Errorf("decode request extensions: %w", err)
	}
	for _, key := range []string{"model", "input", "encoding_format", "dimensions", "user"} {
		delete(extra, key)
	}
	return llmprovider.EmbeddingRequest{
		Model: wire.Model, Input: input, EncodingFormat: wire.EncodingFormat,
		Dimensions: wire.Dimensions, User: wire.User, Extra: extra,
	}, nil
}
