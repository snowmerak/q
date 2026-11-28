package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

type pendingDelegatedTool struct {
	call      llmprovider.ToolCall
	requestID json.RawMessage
	queued    bool
	delivered bool
}

type turnState struct {
	ctx         context.Context
	provider    *Provider
	threadID    string
	model       string
	toolHandler llmprovider.ToolHandler

	mu            sync.Mutex
	turnID        string
	queue         []*llmprovider.ChatChunk
	done          bool
	err           error
	errDelivered  bool
	wake          chan struct{}
	closed        bool
	usage         llmprovider.Usage
	pendingError  error
	deltaItems    map[string]bool
	itemPhases    map[string]string
	delegated     []pendingDelegatedTool
	awaitingTools bool
	parallelTools bool
	nextToolIndex int
}

// addDelegatedTool parks an App Server callback so an OpenAI-compatible
// caller can execute it without interrupting the Codex turn.
func (s *turnState) addDelegatedTool(call llmprovider.ToolCall, requestID json.RawMessage) bool {
	s.mu.Lock()
	if s.done || s.closed {
		s.mu.Unlock()
		return false
	}
	if s.parallelTools {
		call.Index = s.nextToolIndex
		s.nextToolIndex++
	} else {
		call.Index = 0
	}
	s.delegated = append(s.delegated, pendingDelegatedTool{
		call: call, requestID: append(json.RawMessage(nil), requestID...),
	})
	s.awaitingTools = true
	s.queueDelegatedToolsLocked()
	s.mu.Unlock()
	s.signal()
	return true
}

// queueDelegatedToolsLocked exposes either every pending callback or one at a
// time, depending on the OpenAI-compatible parallel_tool_calls setting.
func (s *turnState) queueDelegatedToolsLocked() {
	if !s.parallelTools {
		for _, pending := range s.delegated {
			if pending.queued || pending.delivered {
				return
			}
		}
	}

	for index := range s.delegated {
		pending := &s.delegated[index]
		if pending.queued || pending.delivered {
			continue
		}
		call := pending.call
		pending.queued = true
		usage := s.usage
		s.queue = append(s.queue, &llmprovider.ChatChunk{
			ID: s.turnID, Object: "chat.completion.chunk", Model: s.model,
			ConversationID: s.threadID, Usage: &usage,
			Choices: []llmprovider.Choice{{
				Index: 0,
				Delta: &llmprovider.Message{
					Role: llmprovider.RoleAssistant, ToolCalls: []llmprovider.ToolCall{call},
				},
				FinishReason: "tool_calls",
			}},
		})
		if !s.parallelTools {
			return
		}
	}
}

func (s *turnState) delegatedTools() []llmprovider.ToolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]llmprovider.ToolCall, 0, len(s.delegated))
	for _, pending := range s.delegated {
		result = append(result, pending.call)
	}
	return result
}

func (s *turnState) isAwaitingTools() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.awaitingTools && !s.done
}

// matchDelegatedToolResults validates results for callbacks that have actually
// been delivered to the caller. It deliberately does not mutate state: each
// callback is acknowledged only after its JSON-RPC response is written.
func (s *turnState) matchDelegatedToolResults(messages []llmprovider.Message) ([]pendingDelegatedTool, []llmprovider.Message, error) {
	results := make(map[string]llmprovider.Message)
	for _, message := range messages {
		if message.Role == llmprovider.RoleTool && message.ToolCallID != "" {
			results[message.ToolCallID] = message
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || !s.awaitingTools || len(s.delegated) == 0 {
		return nil, nil, errors.New("codex: thread is not waiting for delegated tool results")
	}
	pending := make([]pendingDelegatedTool, 0, len(s.delegated))
	orderedResults := make([]llmprovider.Message, 0, len(s.delegated))
	for _, item := range s.delegated {
		if !item.delivered {
			continue
		}
		result, ok := results[item.call.ID]
		if !ok {
			return nil, nil, fmt.Errorf("codex: missing result for delegated tool call %q", item.call.ID)
		}
		pending = append(pending, item)
		orderedResults = append(orderedResults, result)
	}
	return pending, orderedResults, nil
}

func (s *turnState) acknowledgeDelegatedTool(acknowledged pendingDelegatedTool) bool {
	s.mu.Lock()
	for index, pending := range s.delegated {
		if pending.call.ID != acknowledged.call.ID || string(pending.requestID) != string(acknowledged.requestID) {
			continue
		}
		s.delegated = append(s.delegated[:index], s.delegated[index+1:]...)
		s.awaitingTools = len(s.delegated) > 0
		if s.awaitingTools {
			s.queueDelegatedToolsLocked()
		} else {
			s.nextToolIndex = 0
		}
		s.mu.Unlock()
		s.signal()
		return true
	}
	s.mu.Unlock()
	return false
}

func newTurnState(
	ctx context.Context,
	provider *Provider,
	threadID, model string,
	toolHandler llmprovider.ToolHandler,
	parallelTools bool,
) *turnState {
	return &turnState{
		ctx: ctx, provider: provider, threadID: threadID, model: model, toolHandler: toolHandler,
		wake: make(chan struct{}, 1), deltaItems: make(map[string]bool), itemPhases: make(map[string]string),
		parallelTools: parallelTools,
	}
}

func (s *turnState) setTurnID(id string) {
	s.mu.Lock()
	s.turnID = id
	s.mu.Unlock()
}

func (s *turnState) setTurnIDIfEmpty(id string) {
	s.mu.Lock()
	if s.turnID == "" {
		s.turnID = id
	}
	s.mu.Unlock()
}

func (s *turnState) id() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnID
}

func (s *turnState) enqueue(chunk *llmprovider.ChatChunk) {
	s.mu.Lock()
	if !s.done && !s.closed {
		s.queue = append(s.queue, chunk)
	}
	s.mu.Unlock()
	s.signal()
}

func (s *turnState) finish(err error) {
	s.mu.Lock()
	if !s.done {
		s.done = true
		s.err = err
	}
	s.mu.Unlock()
	s.signal()
}

func (s *turnState) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *turnState) recv(ctx context.Context) (*llmprovider.ChatChunk, error) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			chunk := s.queue[0]
			s.queue = s.queue[1:]
			s.markDelegatedToolDeliveredLocked(chunk)
			s.mu.Unlock()
			return chunk, nil
		}
		if s.done || s.closed {
			if s.err != nil && !s.errDelivered {
				s.errDelivered = true
				err := s.err
				s.mu.Unlock()
				return nil, err
			}
			s.mu.Unlock()
			return nil, io.EOF
		}
		if s.awaitingTools {
			s.mu.Unlock()
			return nil, io.EOF
		}
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.wake:
		}
	}
}

func (s *turnState) markDelegatedToolDeliveredLocked(chunk *llmprovider.ChatChunk) {
	if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil {
		return
	}
	for _, call := range chunk.Choices[0].Delta.ToolCalls {
		for index := range s.delegated {
			if s.delegated[index].call.ID == call.ID {
				s.delegated[index].queued = false
				s.delegated[index].delivered = true
				break
			}
		}
	}
}

func (s *turnState) close() error {
	s.mu.Lock()
	if s.awaitingTools && !s.done {
		s.mu.Unlock()
		return nil
	}
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	done := s.done
	threadID, turnID := s.threadID, s.turnID
	s.mu.Unlock()
	s.signal()
	if !done {
		go s.provider.interrupt(threadID, turnID)
		s.provider.removeTurn(s)
	}
	return nil
}

func (s *turnState) markDelta(itemID string) {
	s.mu.Lock()
	s.deltaItems[itemID] = true
	s.mu.Unlock()
}

func (s *turnState) hasDelta(itemID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deltaItems[itemID]
}

func (s *turnState) setItemPhase(itemID, phase string) {
	s.mu.Lock()
	s.itemPhases[itemID] = phase
	s.mu.Unlock()
}

func (s *turnState) itemPhase(itemID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.itemPhases[itemID]
}

func (s *turnState) setUsage(usage llmprovider.Usage) {
	s.mu.Lock()
	s.usage = usage
	s.mu.Unlock()
}

func (s *turnState) usageValue() llmprovider.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage
}

func (s *turnState) setTurnError(err error) {
	s.mu.Lock()
	s.pendingError = err
	s.mu.Unlock()
}

func (s *turnState) turnError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingError
}

type codexStream struct {
	state *turnState
	ctx   context.Context
}

func (s *codexStream) Recv() (*llmprovider.ChatChunk, error) { return s.state.recv(s.ctx) }
func (s *codexStream) Close() error                          { return s.state.close() }

var _ llmprovider.Stream = (*codexStream)(nil)
