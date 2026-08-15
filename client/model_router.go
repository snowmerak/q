package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	defaultCircuitFailureThreshold = 3
	defaultCircuitOpenDuration     = 30 * time.Second
)

// ModelCandidate is one ordered member of a model group. A zero Timeout does
// not add a deadline; the caller's context still applies.
type ModelCandidate struct {
	Model           string
	ReasoningEffort string
	Timeout         time.Duration
}

type ModelChatClient interface {
	Chat(context.Context, ChatRequest) (*ChatResponse, error)
}

type modelCircuit struct {
	failures  int
	openUntil time.Time
	halfOpen  bool
}

// ModelRouter applies ordered fallback and a process-memory circuit breaker.
// A router is safe for concurrent use.
type ModelRouter struct {
	mu               sync.Mutex
	circuits         map[string]*modelCircuit
	failureThreshold int
	openDuration     time.Duration
	now              func() time.Time
}

// NewModelRouter creates a router with three consecutive transient failures
// opening a model circuit for 30 seconds.
func NewModelRouter() *ModelRouter {
	return &ModelRouter{
		circuits: make(map[string]*modelCircuit), failureThreshold: defaultCircuitFailureThreshold,
		openDuration: defaultCircuitOpenDuration, now: time.Now,
	}
}

var processModelRouter = NewModelRouter()

// RouteChat tries ordered candidates beginning at start. It returns the index
// of the model that succeeded, or the last model attempted on error. Timeout
// and HTTP 5xx errors move immediately to the next candidate. Other failures
// stop routing without affecting the circuit.
func (r *ModelRouter) RouteChat(
	ctx context.Context,
	configured ModelChatClient,
	request ChatRequest,
	candidates []ModelCandidate,
	start int,
) (*ChatResponse, int, error) {
	if r == nil {
		r = processModelRouter
	}
	if configured == nil {
		return nil, start, errors.New("client: model router client is required")
	}
	if len(candidates) == 0 {
		return nil, start, errors.New("client: model router requires at least one candidate")
	}
	if start < 0 || start >= len(candidates) {
		return nil, start, fmt.Errorf("client: model router start index %d is out of range", start)
	}

	failures := make([]error, 0, len(candidates)-start)
	last := start
	for index := start; index < len(candidates); index++ {
		candidate := candidates[index]
		last = index
		if !r.allow(candidate.Model) {
			failures = append(failures, fmt.Errorf("model %q: circuit is open", candidate.Model))
			continue
		}

		attemptContext := ctx
		cancel := func() {}
		if candidate.Timeout > 0 {
			attemptContext, cancel = context.WithTimeout(ctx, candidate.Timeout)
		}
		attempt := request
		attempt.Model = candidate.Model
		attempt.ReasoningEffort = candidate.ReasoningEffort
		response, err := configured.Chat(attemptContext, attempt)
		attemptErr := attemptContext.Err()
		cancel()
		if err == nil {
			r.succeed(candidate.Model)
			return response, index, nil
		}
		if ctx.Err() != nil {
			r.neutral(candidate.Model)
			failures = append(failures, fmt.Errorf("model %q: %w", candidate.Model, err))
			return nil, index, errors.Join(failures...)
		}
		if !transientModelError(err, attemptErr) {
			r.neutral(candidate.Model)
			failures = append(failures, fmt.Errorf("model %q: %w", candidate.Model, err))
			return nil, index, errors.Join(failures...)
		}
		r.fail(candidate.Model)
		failures = append(failures, fmt.Errorf("model %q: %w", candidate.Model, err))
	}
	return nil, last, fmt.Errorf("client: model group exhausted: %w", errors.Join(failures...))
}

func transientModelError(err error, attemptErr error) bool {
	if errors.Is(attemptErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var apiError *APIError
	if errors.As(err, &apiError) {
		return apiError.StatusCode >= http.StatusInternalServerError && apiError.StatusCode <= 599
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func (r *ModelRouter) allow(model string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.circuits[model]
	if state == nil || state.openUntil.IsZero() {
		return true
	}
	now := r.now()
	if now.Before(state.openUntil) || state.halfOpen {
		return false
	}
	state.halfOpen = true
	return true
}

func (r *ModelRouter) succeed(model string) {
	r.mu.Lock()
	delete(r.circuits, model)
	r.mu.Unlock()
}

func (r *ModelRouter) neutral(model string) {
	r.mu.Lock()
	state := r.circuits[model]
	if state != nil && state.halfOpen {
		delete(r.circuits, model)
	}
	r.mu.Unlock()
}

func (r *ModelRouter) fail(model string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.circuits[model]
	if state == nil {
		state = &modelCircuit{}
		r.circuits[model] = state
	}
	state.failures++
	if state.halfOpen || state.failures >= r.failureThreshold {
		state.failures = r.failureThreshold
		state.openUntil = r.now().Add(r.openDuration)
		state.halfOpen = false
	}
}
