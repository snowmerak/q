package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

const conversationCacheWriteTimeout = 2 * time.Second

type conversationCheckpoint struct {
	ThreadID    string `json:"thread_id"`
	TurnID      string `json:"turn_id,omitempty"`
	HistoryHash string `json:"history_hash"`
}

type conversationResolution struct {
	checkpoint conversationCheckpoint
	inferred   bool
}

type conversationHashInput struct {
	Version          int                   `json:"version"`
	Scope            string                `json:"scope,omitempty"`
	Model            string                `json:"model,omitempty"`
	WorkingDirectory string                `json:"working_directory,omitempty"`
	Messages         []llmprovider.Message `json:"messages"`
	Tools            []llmprovider.Tool    `json:"tools,omitempty"`
}

func (p *Provider) inferConversation(ctx context.Context, request llmprovider.ChatRequest) conversationResolution {
	if request.ConversationID != "" || p.config.conversationCache == nil {
		return conversationResolution{}
	}

	parent, hasHistory := conversationParent(request.Messages)
	scope := conversationScope(request)
	var key string
	if hasHistory {
		hash, err := p.conversationHistoryHash(request, parent)
		if err != nil {
			return conversationResolution{}
		}
		key = "lineage:" + hash
	} else if scope != "" {
		key = "session:" + digestString(scope)
	} else {
		return conversationResolution{}
	}

	checkpoint, ok := p.loadConversationCheckpoint(ctx, key)
	if !ok || checkpoint.ThreadID == "" {
		return conversationResolution{}
	}
	return conversationResolution{checkpoint: checkpoint, inferred: true}
}

func (p *Provider) resolveInferredThread(ctx context.Context, resolution conversationResolution, toolContinuation bool) string {
	if !resolution.inferred {
		return ""
	}
	checkpoint := resolution.checkpoint
	if toolContinuation {
		return checkpoint.ThreadID
	}

	shouldFork := false
	p.mu.Lock()
	shouldFork = p.active[checkpoint.ThreadID] != nil
	p.mu.Unlock()
	if !shouldFork {
		if head, ok := p.loadConversationCheckpoint(ctx, conversationHeadKey(checkpoint.ThreadID)); ok {
			shouldFork = head.HistoryHash != "" && head.HistoryHash != checkpoint.HistoryHash
		}
	}
	if !shouldFork {
		return checkpoint.ThreadID
	}
	if checkpoint.TurnID == "" {
		return ""
	}

	params := map[string]any{
		"threadId":   checkpoint.ThreadID,
		"lastTurnId": checkpoint.TurnID,
		"ephemeral":  p.config.ephemeral,
	}
	var response threadResponse
	if err := p.Call(ctx, "thread/fork", params, &response); err != nil || response.Thread.ID == "" {
		return ""
	}
	p.mu.Lock()
	p.loaded[response.Thread.ID] = true
	p.mu.Unlock()
	return response.Thread.ID
}

func (p *Provider) saveConversationCheckpoint(request llmprovider.ChatRequest, assistant llmprovider.Message, threadID, turnID string) {
	if p.config.conversationCache == nil || threadID == "" || (assistant.Content == "" && len(assistant.ToolCalls) == 0) {
		return
	}
	messages := append(append([]llmprovider.Message(nil), request.Messages...), assistant)
	hash, err := p.conversationHistoryHash(request, messages)
	if err != nil {
		return
	}
	checkpoint := conversationCheckpoint{ThreadID: threadID, TurnID: turnID, HistoryHash: hash}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), conversationCacheWriteTimeout)
	defer cancel()
	_ = p.config.conversationCache.Set(ctx, "lineage:"+hash, data, p.config.conversationCacheTTL)
	_ = p.config.conversationCache.Set(ctx, conversationHeadKey(threadID), data, p.config.conversationCacheTTL)
	if scope := conversationScope(request); scope != "" {
		_ = p.config.conversationCache.Set(ctx, "session:"+digestString(scope), data, p.config.conversationCacheTTL)
	}
}

func (p *Provider) loadConversationCheckpoint(ctx context.Context, key string) (conversationCheckpoint, bool) {
	data, ok, err := p.config.conversationCache.Get(ctx, key)
	if err != nil || !ok {
		return conversationCheckpoint{}, false
	}
	var checkpoint conversationCheckpoint
	if json.Unmarshal(data, &checkpoint) != nil || checkpoint.ThreadID == "" {
		return conversationCheckpoint{}, false
	}
	return checkpoint, true
}

func (p *Provider) conversationHistoryHash(request llmprovider.ChatRequest, messages []llmprovider.Message) (string, error) {
	payload := conversationHashInput{
		Version:          1,
		Scope:            conversationScope(request),
		Model:            firstNonEmpty(request.Model, p.config.model),
		WorkingDirectory: firstNonEmpty(request.WorkingDirectory, p.config.cwd),
		Messages:         messages,
		Tools:            request.Tools,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func conversationParent(messages []llmprovider.Message) ([]llmprovider.Message, bool) {
	end := len(messages)
	for end > 0 && messages[end-1].Role == llmprovider.RoleTool {
		end--
	}
	if end == len(messages) {
		if end == 0 || messages[end-1].Role != llmprovider.RoleUser {
			return nil, false
		}
		end--
	}
	parent := messages[:end]
	for _, message := range parent {
		if message.Role == llmprovider.RoleAssistant || message.Role == llmprovider.RoleTool {
			return parent, true
		}
	}
	return parent, false
}

func conversationScope(request llmprovider.ChatRequest) string {
	if request.Extra != nil {
		if value, ok := request.Extra["session_id"].(string); ok && value != "" {
			return value
		}
	}
	if request.Headers != nil {
		if value := request.Headers.Get("X-Session-Id"); value != "" {
			return value
		}
	}
	if request.Extra != nil {
		if value, ok := request.Extra["user"].(string); ok {
			return value
		}
	}
	return ""
}

func conversationHeadKey(threadID string) string {
	return "head:" + digestString(threadID)
}

func digestString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

type conversationStream struct {
	inner        llmprovider.Stream
	provider     *Provider
	request      llmprovider.ChatRequest
	once         sync.Once
	allContent   string
	finalContent string
	toolCalls    []llmprovider.ToolCall
	threadID     string
	turnID       string
	complete     bool
}

func newConversationStream(provider *Provider, request llmprovider.ChatRequest, inner llmprovider.Stream) llmprovider.Stream {
	request.Messages = append([]llmprovider.Message(nil), request.Messages...)
	return &conversationStream{provider: provider, request: request, inner: inner}
}

func (s *conversationStream) Recv() (*llmprovider.ChatChunk, error) {
	chunk, err := s.inner.Recv()
	if chunk != nil {
		if s.turnID == "" {
			s.turnID = chunk.ID
		}
		if chunk.ConversationID != "" {
			s.threadID = chunk.ConversationID
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta != nil {
			delta := chunk.Choices[0].Delta
			s.allContent += delta.Content
			if chunk.Choices[0].Phase == "final_answer" {
				s.finalContent += delta.Content
			}
			s.toolCalls = append(s.toolCalls, delta.ToolCalls...)
			if chunk.Choices[0].FinishReason != "" {
				s.complete = true
			}
		}
	}
	if errors.Is(err, io.EOF) {
		s.save()
	}
	return chunk, err
}

func (s *conversationStream) Close() error {
	if s.complete {
		s.save()
	}
	return s.inner.Close()
}

func (s *conversationStream) save() {
	s.once.Do(func() {
		content := s.finalContent
		if content == "" {
			content = s.allContent
		}
		s.provider.saveConversationCheckpoint(s.request, llmprovider.Message{
			Role: llmprovider.RoleAssistant, Content: content, ToolCalls: s.toolCalls,
		}, s.threadID, s.turnID)
	})
}

func (s *conversationStream) ResponseHeaders() http.Header {
	if headerer, ok := s.inner.(llmprovider.ResponseHeaderer); ok {
		return headerer.ResponseHeaders()
	}
	return nil
}

var _ llmprovider.Stream = (*conversationStream)(nil)
var _ llmprovider.ResponseHeaderer = (*conversationStream)(nil)
