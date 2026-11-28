package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

type phaseStreamProvider struct{}

func (*phaseStreamProvider) Chat(context.Context, llmprovider.ChatRequest) (*llmprovider.ChatResponse, error) {
	return nil, nil
}

func (*phaseStreamProvider) ChatStream(context.Context, llmprovider.ChatRequest) (llmprovider.Stream, error) {
	return &phaseChunkStream{chunks: []*llmprovider.ChatChunk{
		{Choices: []llmprovider.Choice{{
			Index: 0, Phase: "commentary",
			Delta: &llmprovider.Message{Role: llmprovider.RoleAssistant, Content: "working"},
		}}},
		{Choices: []llmprovider.Choice{{
			Index: 0, Phase: "final_answer",
			Delta: &llmprovider.Message{Role: llmprovider.RoleAssistant, Content: "done"},
		}}},
	}}, nil
}

func (*phaseStreamProvider) Close() error { return nil }

type phaseChunkStream struct {
	chunks []*llmprovider.ChatChunk
	index  int
}

func (s *phaseChunkStream) Recv() (*llmprovider.ChatChunk, error) {
	if s.index >= len(s.chunks) {
		return nil, io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}

func (*phaseChunkStream) Close() error { return nil }

func TestGatewayStreamPreservesCodexChoicePhase(t *testing.T) {
	value := &Gateway{routes: map[string]*route{
		"codex": {id: "codex", prefix: "codex", provider: &phaseStreamProvider{}, providerType: "codex"},
	}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"codex/test","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	)
	response := httptest.NewRecorder()
	value.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"phase":"commentary"`) ||
		!strings.Contains(response.Body.String(), `"phase":"final_answer"`) ||
		!strings.Contains(response.Body.String(), "data: [DONE]") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
