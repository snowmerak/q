package workitems

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const TicketGuidanceMessagePrefix = "q_ticket_guidance_"

type TicketGuidance struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

type TicketGuidanceInput struct {
	Content   string `json:"content"`
	RequestID string `json:"request_id"`
}

func (a Attempt) AcceptsGuidance() bool {
	return a.WorkerKind == "native" && a.GuidanceOpen && a.Status == "running" && !a.StopRequested && time.Now().Before(a.LeaseUntil)
}

func (s *Store) QueueTicketGuidance(ctx context.Context, id string, in TicketGuidanceInput) (TicketGuidance, error) {
	in.Content = strings.TrimSpace(in.Content)
	if in.RequestID == "" || in.Content == "" || len(in.Content) > 8192 {
		return TicketGuidance{}, ErrInvalidInput
	}
	attempt, err := s.GetAttempt(ctx, id)
	if err != nil {
		return TicketGuidance{}, err
	}
	return mutate(ctx, s, attempt.WorkspaceID, "ticket-guidance:"+id, in.RequestID, in, func(tx *storeTx) (TicketGuidance, error) {
		a, err := decodeRow[Attempt](tx.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, id))
		if err != nil {
			return TicketGuidance{}, err
		}
		item, err := ticketTx(ctx, tx, a.TicketID)
		if err != nil {
			return TicketGuidance{}, err
		}
		if item.ActiveAttemptID != a.ID || !a.AcceptsGuidance() {
			return TicketGuidance{}, fmt.Errorf("%w: this worker is no longer accepting guidance", ErrConflict)
		}
		if a.Guidance != nil {
			return TicketGuidance{}, fmt.Errorf("%w: the previous guidance is still pending", ErrConflict)
		}
		body, err := decodeRow[Post](tx.QueryRowContext(ctx, `SELECT payload FROM posts WHERE id=?`, item.BodyPostID))
		if err != nil {
			return TicketGuidance{}, err
		}
		if ticketContract(item, body.Revision) != a.TicketContract {
			return TicketGuidance{}, fmt.Errorf("%w: the ticket scope changed after the worker started", ErrConflict)
		}
		guidance := TicketGuidance{ID: uuid.NewString(), Content: in.Content}
		a.Guidance = &guidance
		return guidance, saveAttempt(ctx, tx, a, false)
	})
}
