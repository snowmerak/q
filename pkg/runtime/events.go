// Package runtime orchestrates character turns, streaming bubbles, real-time
// intercepts, context compaction, and SSE event delivery.
package runtime

import (
	"sync"
)

const eventRingCapacity = 256

// Event represents one typed SSE notification emitted by a room.
type Event struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
	Data any    `json:"data"`
}

type roomBus struct {
	mu      sync.Mutex
	nextID  int64
	ring    []Event
	nextSub uint64
	subs    map[uint64]chan Event
}

// EventBus distributes room-scoped events to SSE subscribers and keeps a short
// replay ring buffer to satisfy Last-Event-ID reconnects.
type EventBus struct {
	mu    sync.Mutex
	rooms map[string]*roomBus
}

func NewEventBus() *EventBus {
	return &EventBus{
		rooms: make(map[string]*roomBus),
	}
}

func (b *EventBus) getOrCreate(roomID string) *roomBus {
	b.mu.Lock()
	defer b.mu.Unlock()
	rb, ok := b.rooms[roomID]
	if !ok {
		rb = &roomBus{
			nextID: 1,
			subs:   make(map[uint64]chan Event),
		}
		b.rooms[roomID] = rb
	}
	return rb
}

// Publish records an event in the room's ring buffer and pushes it to active subscribers.
func (b *EventBus) Publish(roomID, eventType string, data any) Event {
	if b == nil {
		return Event{}
	}
	rb := b.getOrCreate(roomID)
	rb.mu.Lock()
	ev := Event{
		ID:   rb.nextID,
		Type: eventType,
		Data: data,
	}
	rb.nextID++
	rb.ring = append(rb.ring, ev)
	if len(rb.ring) > eventRingCapacity {
		copy(rb.ring, rb.ring[len(rb.ring)-eventRingCapacity:])
		rb.ring = rb.ring[:eventRingCapacity]
	}
	for _, ch := range rb.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	rb.mu.Unlock()
	return ev
}

// Subscribe attaches a listener for roomID, replaying buffered events newer than lastEventID.
func (b *EventBus) Subscribe(roomID string, lastEventID int64) (<-chan Event, func()) {
	ch := make(chan Event, eventRingCapacity)
	if b == nil {
		close(ch)
		return ch, func() {}
	}
	rb := b.getOrCreate(roomID)
	rb.mu.Lock()
	if lastEventID > 0 {
		for _, ev := range rb.ring {
			if ev.ID > lastEventID {
				select {
				case ch <- ev:
				default:
				}
			}
		}
	}
	subID := rb.nextSub
	rb.nextSub++
	rb.subs[subID] = ch
	rb.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			rb.mu.Lock()
			if existing, ok := rb.subs[subID]; ok {
				delete(rb.subs, subID)
				close(existing)
			}
			rb.mu.Unlock()
		})
	}
	return ch, cancel
}
