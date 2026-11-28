package runtime

import (
	"context"
	"strings"
	"sync"
)

type activeSpeculativeTurn struct {
	turnID     string
	specCancel context.CancelFunc
	stopped    bool
	cutIns     []string
}

// InterceptHub coordinates lock-free real-time cut-in and stop signals against
// a room's currently executing speculative turn context.
type InterceptHub struct {
	mu     sync.Mutex
	active map[string]*activeSpeculativeTurn
}

func NewInterceptHub() *InterceptHub {
	return &InterceptHub{
		active: make(map[string]*activeSpeculativeTurn),
	}
}

// HasActiveTurn reports whether roomID currently has a running turn registered.
func (h *InterceptHub) HasActiveTurn(roomID string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	_, ok := h.active[roomID]
	h.mu.Unlock()
	return ok
}

// IsTurnActive reports whether turnID is currently registered as active for roomID.
func (h *InterceptHub) IsTurnActive(roomID, turnID string) bool {
	if h == nil || turnID == "" {
		return false
	}
	h.mu.Lock()
	cur, ok := h.active[roomID]
	active := ok && cur.turnID == turnID
	h.mu.Unlock()
	return active
}

// StartTurn reserves roomID for turnID and returns its cancellable speculative context.
func (h *InterceptHub) StartTurn(roomID, turnID string) (context.Context, bool) {
	if h == nil {
		return context.Background(), true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.active[roomID]; exists {
		return nil, false
	}
	specCtx, specCancel := context.WithCancel(context.Background())
	h.active[roomID] = &activeSpeculativeTurn{
		turnID:     turnID,
		specCancel: specCancel,
	}
	return specCtx, true
}

// RestartSpeculative issues a fresh cancellable context after handling a cut-in.
func (h *InterceptHub) RestartSpeculative(roomID, turnID string) (context.Context, bool) {
	if h == nil {
		return context.Background(), true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cur, exists := h.active[roomID]
	if !exists || cur.turnID != turnID || cur.stopped {
		return nil, false
	}
	if cur.specCancel != nil {
		cur.specCancel()
	}
	specCtx, specCancel := context.WithCancel(context.Background())
	cur.specCancel = specCancel
	return specCtx, true
}

// FinishTurn releases the room slot if turnID is still the registered turn.
func (h *InterceptHub) FinishTurn(roomID, turnID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	cur, exists := h.active[roomID]
	if exists && cur.turnID == turnID {
		if cur.specCancel != nil {
			cur.specCancel()
		}
		delete(h.active, roomID)
	}
	h.mu.Unlock()
}

// Intercept cancels the running speculative turn immediately. Non-empty text
// queues a cut-in; empty text marks the turn for graceful cancellation.
func (h *InterceptHub) Intercept(roomID, text string) (turnID, kind string, accepted bool) {
	if h == nil {
		return "", "", false
	}
	h.mu.Lock()
	cur, exists := h.active[roomID]
	if !exists {
		h.mu.Unlock()
		return "", "", false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		cur.stopped = true
		kind = "stop"
	} else {
		cur.cutIns = append(cur.cutIns, trimmed)
		kind = "cut_in"
	}
	turnID = cur.turnID
	cancel := cur.specCancel
	h.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return turnID, kind, true
}

// IsStopped reports whether stop was requested for turnID in roomID.
func (h *InterceptHub) IsStopped(roomID, turnID string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	cur, exists := h.active[roomID]
	stopped := exists && cur.turnID == turnID && cur.stopped
	h.mu.Unlock()
	return stopped
}

// TakeCutIn pops the next pending cut-in message if any.
func (h *InterceptHub) TakeCutIn(roomID, turnID string) (string, bool) {
	if h == nil {
		return "", false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cur, exists := h.active[roomID]
	if !exists || cur.turnID != turnID || len(cur.cutIns) == 0 {
		return "", false
	}
	next := cur.cutIns[0]
	cur.cutIns = cur.cutIns[1:]
	return next, true
}

// StopAll cancels all active speculative turns on engine shutdown.
func (h *InterceptHub) StopAll() {
	if h == nil {
		return
	}
	h.mu.Lock()
	for _, cur := range h.active {
		cur.stopped = true
		if cur.specCancel != nil {
			cur.specCancel()
		}
	}
	h.mu.Unlock()
}
