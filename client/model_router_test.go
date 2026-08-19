package client

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type routingChatClient struct {
	mu    sync.Mutex
	calls []ChatRequest
	chat  func(context.Context, ChatRequest) (*ChatResponse, error)
}

func (c *routingChatClient) Chat(ctx context.Context, request ChatRequest) (*ChatResponse, error) {
	c.mu.Lock()
	c.calls = append(c.calls, request)
	c.mu.Unlock()
	return c.chat(ctx, request)
}

func (c *routingChatClient) count(model string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, request := range c.calls {
		if request.Model == model {
			count++
		}
	}
	return count
}

func TestModelRouterFallsBackImmediatelyOnHTTP5xx(t *testing.T) {
	configured := &routingChatClient{chat: func(_ context.Context, request ChatRequest) (*ChatResponse, error) {
		if request.Model == "primary" {
			return nil, &APIError{StatusCode: http.StatusServiceUnavailable, Message: "temporary"}
		}
		return &ChatResponse{}, nil
	}}
	response, selected, err := NewModelRouter().RouteChat(t.Context(), configured, ChatRequest{ConversationID: "primary-thread"}, []ModelCandidate{
		{Model: "primary", ReasoningEffort: "high"},
		{Model: "secondary", ReasoningEffort: "medium"},
	}, 0)
	if err != nil || response == nil || selected != 1 {
		t.Fatalf("response = %#v, selected = %d, err = %v", response, selected, err)
	}
	if configured.count("primary") != 1 || configured.count("secondary") != 1 ||
		configured.calls[0].ConversationID != "primary-thread" || configured.calls[1].ConversationID != "" ||
		configured.calls[1].ReasoningEffort != "medium" {
		t.Fatalf("calls = %#v", configured.calls)
	}
}

func TestModelRouterCandidateTimeoutFallsBack(t *testing.T) {
	configured := &routingChatClient{chat: func(ctx context.Context, request ChatRequest) (*ChatResponse, error) {
		if request.Model == "slow" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return &ChatResponse{}, nil
	}}
	_, selected, err := NewModelRouter().RouteChat(t.Context(), configured, ChatRequest{}, []ModelCandidate{
		{Model: "slow", Timeout: 5 * time.Millisecond},
		{Model: "secondary"},
	}, 0)
	if err != nil || selected != 1 {
		t.Fatalf("selected = %d, err = %v", selected, err)
	}
}

func TestModelRouterParentCancellationDoesNotFallback(t *testing.T) {
	configured := &routingChatClient{chat: func(ctx context.Context, _ ChatRequest) (*ChatResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, _, err := NewModelRouter().RouteChat(ctx, configured, ChatRequest{}, []ModelCandidate{
		{Model: "primary"}, {Model: "secondary"},
	}, 0)
	if !errors.Is(err, context.DeadlineExceeded) || configured.count("secondary") != 0 {
		t.Fatalf("calls = %#v, err = %v", configured.calls, err)
	}
}

func TestModelRouterDoesNotFallbackOnHTTP4xx(t *testing.T) {
	configured := &routingChatClient{chat: func(_ context.Context, _ ChatRequest) (*ChatResponse, error) {
		return nil, &APIError{StatusCode: http.StatusBadRequest, Message: "invalid"}
	}}
	_, _, err := NewModelRouter().RouteChat(t.Context(), configured, ChatRequest{}, []ModelCandidate{
		{Model: "primary"}, {Model: "secondary"},
	}, 0)
	var apiError *APIError
	if !errors.As(err, &apiError) || configured.count("secondary") != 0 {
		t.Fatalf("calls = %#v, err = %v", configured.calls, err)
	}
}

func TestModelRouterCircuitOpensAndHalfOpenProbeRecovers(t *testing.T) {
	now := time.Unix(1_000, 0)
	router := NewModelRouter()
	router.now = func() time.Time { return now }
	primaryHealthy := false
	configured := &routingChatClient{chat: func(_ context.Context, request ChatRequest) (*ChatResponse, error) {
		if request.Model == "primary" && !primaryHealthy {
			return nil, &APIError{StatusCode: http.StatusBadGateway, Message: "down"}
		}
		return &ChatResponse{}, nil
	}}
	candidates := []ModelCandidate{{Model: "primary"}, {Model: "secondary"}}
	for range defaultCircuitFailureThreshold {
		if _, selected, err := router.RouteChat(t.Context(), configured, ChatRequest{}, candidates, 0); err != nil || selected != 1 {
			t.Fatalf("selected = %d, err = %v", selected, err)
		}
	}
	if _, selected, err := router.RouteChat(t.Context(), configured, ChatRequest{}, candidates, 0); err != nil || selected != 1 {
		t.Fatalf("open circuit selected = %d, err = %v", selected, err)
	}
	if configured.count("primary") != defaultCircuitFailureThreshold {
		t.Fatalf("primary calls while open = %d", configured.count("primary"))
	}

	primaryHealthy = true
	now = now.Add(defaultCircuitOpenDuration)
	if _, selected, err := router.RouteChat(t.Context(), configured, ChatRequest{}, candidates, 0); err != nil || selected != 0 {
		t.Fatalf("half-open selected = %d, err = %v", selected, err)
	}
	if configured.count("primary") != defaultCircuitFailureThreshold+1 {
		t.Fatalf("primary calls after recovery = %d", configured.count("primary"))
	}
}
