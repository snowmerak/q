package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", g.handleModels)
	mux.HandleFunc("GET /v1/models/{id...}", g.handleModel)
	mux.HandleFunc("POST /v1/chat/completions", g.handleChatCompletions)
	mux.HandleFunc("POST /v1/responses", g.handleResponses)
	mux.HandleFunc("POST /v1/embeddings", g.handleEmbeddings)
	mux.HandleFunc("GET /v1/providers/{provider}/chatgpt", g.handleChatGPTAuth)
	mux.HandleFunc("POST /v1/providers/{provider}/chatgpt/{action}", g.handleChatGPTAuth)
	return mux
}

func (g *Gateway) handleModels(writer http.ResponseWriter, request *http.Request) {
	models, err := g.Models(request.Context())
	if err != nil {
		writeError(writer, http.StatusBadGateway, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"object": "list", "data": models})
}

func (g *Gateway) handleModel(writer http.ResponseWriter, request *http.Request) {
	model, err := g.Model(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writeJSON(writer, http.StatusOK, model)
}

func (g *Gateway) handleChatCompletions(writer http.ResponseWriter, request *http.Request) {
	chatRequest, stream, err := decodeChatRequest(request.Body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	route, backendModel, err := g.resolve(chatRequest.Model)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	externalModel := chatRequest.Model
	chatRequest.Model = backendModel
	chatRequest.Headers = selectedHeaders(request.Header, route.forwardHeaders)
	chatRequest, cacheConversationID, err := preparePromptCache(route, chatRequest)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errAnthropicCachePolicy) {
			status = http.StatusBadRequest
		}
		writeError(writer, status, err)
		return
	}
	if stream {
		g.streamChat(writer, request.Context(), route, externalModel, cacheConversationID, chatRequest)
		return
	}
	response, err := route.provider.Chat(request.Context(), chatRequest)
	if err != nil {
		writeProviderError(writer, err)
		return
	}
	response.Model = externalModel
	if response.ConversationID == "" {
		response.ConversationID = cacheConversationID
	}
	copySelectedHeaders(writer.Header(), response.Headers, route.forwardResponseHeaders)
	if _, compatible := route.provider.(llmprovider.OpenAIWireProvider); compatible && len(response.Raw) > 0 {
		writeRawJSON(writer, http.StatusOK, rewriteChatResponse(response.Raw, externalModel, response.ConversationID))
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (g *Gateway) streamChat(writer http.ResponseWriter, ctx context.Context, route *route, externalModel, cacheConversationID string, request llmprovider.ChatRequest) {
	stream, err := route.provider.ChatStream(ctx, request)
	if err != nil {
		writeProviderError(writer, err)
		return
	}
	defer stream.Close()
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, errors.New("streaming is unsupported by the HTTP server"))
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	if headerer, ok := stream.(llmprovider.ResponseHeaderer); ok {
		copySelectedHeaders(writer.Header(), headerer.ResponseHeaders(), route.forwardResponseHeaders)
	}
	writer.WriteHeader(http.StatusOK)
	encoder := json.NewEncoder(writer)
	responseConversationID := cacheConversationID
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			_, _ = io.WriteString(writer, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
		if recvErr != nil {
			data, _ := json.Marshal(errorEnvelope(http.StatusBadGateway, recvErr))
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", data)
			flusher.Flush()
			return
		}
		chunk.Model = externalModel
		if chunk.ConversationID != "" {
			responseConversationID = chunk.ConversationID
		}
		chunk.ConversationID = responseConversationID
		_, _ = io.WriteString(writer, "data: ")
		if _, compatible := route.provider.(llmprovider.OpenAIWireProvider); compatible && len(chunk.Raw) > 0 {
			if _, err := writer.Write(rewriteChatResponse(chunk.Raw, externalModel, chunk.ConversationID)); err != nil {
				return
			}
			_, _ = io.WriteString(writer, "\n")
		} else {
			if err := encoder.Encode(chunk); err != nil {
				return
			}
		}
		_, _ = io.WriteString(writer, "\n")
		flusher.Flush()
	}
}

func decodeChatRequest(reader io.Reader) (llmprovider.ChatRequest, bool, error) {
	data, err := io.ReadAll(io.LimitReader(reader, 16<<20))
	if err != nil {
		return llmprovider.ChatRequest{}, false, fmt.Errorf("read request: %w", err)
	}
	var wire struct {
		llmprovider.ChatRequest
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return llmprovider.ChatRequest{}, false, fmt.Errorf("decode request: %w", err)
	}
	if wire.Model == "" {
		return llmprovider.ChatRequest{}, false, errors.New("model is required")
	}
	var extra map[string]any
	if err := json.Unmarshal(data, &extra); err != nil {
		return llmprovider.ChatRequest{}, false, fmt.Errorf("decode request extensions: %w", err)
	}
	for _, key := range []string{
		"model", "messages", "temperature", "top_p", "max_tokens", "max_completion_tokens",
		"stop", "tools", "tool_choice", "parallel_tool_calls", "stream", "conversation_id",
		"reasoning_effort",
	} {
		delete(extra, key)
	}
	wire.Extra = extra
	return wire.ChatRequest, wire.Stream, nil
}

func selectedHeaders(source http.Header, allowed map[string]struct{}) http.Header {
	result := make(http.Header)
	copySelectedHeaders(result, source, allowed)
	return result
}

func copySelectedHeaders(target, source http.Header, allowed map[string]struct{}) {
	for key, values := range source {
		canonical := http.CanonicalHeaderKey(key)
		if _, ok := allowed[canonical]; !ok {
			continue
		}
		target.Del(canonical)
		for _, value := range values {
			target.Add(canonical, value)
		}
	}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeRawJSON(writer http.ResponseWriter, status int, value []byte) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(value)
	_, _ = writer.Write([]byte("\n"))
}

func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, errorEnvelope(status, err))
}

func writeProviderError(writer http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var apiError llmprovider.APIError
	if errors.As(err, &apiError) && apiError.HTTPStatusCode() >= 400 && apiError.HTTPStatusCode() <= 599 {
		status = apiError.HTTPStatusCode()
	}
	writeError(writer, status, err)
}

func errorEnvelope(status int, err error) map[string]any {
	errorType := "api_error"
	message := err.Error()
	var code any = strings.ToLower(strings.ReplaceAll(http.StatusText(status), " ", "_"))
	if status >= 400 && status < 500 {
		errorType = "invalid_request_error"
	}
	var apiError llmprovider.APIError
	if errors.As(err, &apiError) {
		if apiError.APIErrorMessage() != "" {
			message = apiError.APIErrorMessage()
		}
		if apiError.APIErrorType() != "" {
			errorType = apiError.APIErrorType()
		}
		if apiError.APIErrorCode() != nil {
			code = apiError.APIErrorCode()
		}
	}
	return map[string]any{"error": map[string]any{
		"message": message, "type": errorType, "code": code,
	}}
}
