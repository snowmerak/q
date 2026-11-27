package workitems

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const WaitingUser = "waiting_user"

type UserQuestion struct {
	ID       string   `json:"id,omitempty"`
	Question string   `json:"question"`
	Context  string   `json:"context,omitempty"`
	Choices  []string `json:"choices,omitempty"`
	Late     bool     `json:"late,omitempty"`
	Answer   string   `json:"answer,omitempty"`
}

func (q UserQuestion) Validate() error {
	if strings.TrimSpace(q.Question) == "" || len(q.Question) > 1024 || len(q.Context) > 8192 || len(q.Choices) > 5 {
		return ErrInvalidInput
	}
	for _, choice := range q.Choices {
		if strings.TrimSpace(choice) == "" || len(choice) > 320 {
			return ErrInvalidInput
		}
	}
	return nil
}

type AnswerInput struct {
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer"`
	RequestID  string `json:"request_id"`
}

func awaitingTicketDecision(ctx context.Context, tx *storeTx, item Item) (bool, error) {
	if item.ExecutionState == WaitingUser {
		return true, nil
	}
	if item.ActiveAttemptID == "" {
		return false, nil
	}
	switch item.ExecutionState {
	case "queued", "assignment_waiting", "blocked", "unknown", "reserved", "running":
	default:
		return false, nil
	}
	attempt, err := decodeRow[Attempt](tx.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, item.ActiveAttemptID))
	return attempt.Status == WaitingUser || attempt.CanRetryCompaction() || attempt.CompactionAttemptID != "" && (attempt.Status == "running" || attempt.Status == "reserved"), err
}

func (s *Store) AnswerQuestion(ctx context.Context, id string, in AnswerInput) (Item, error) {
	attempt, err := s.GetAttempt(ctx, id)
	if err != nil {
		return Item{}, err
	}
	in.Answer = strings.TrimSpace(in.Answer)
	if in.QuestionID == "" || in.RequestID == "" || in.Answer == "" || len(in.Answer) > 8192 {
		return Item{}, ErrInvalidInput
	}
	return mutate(ctx, s, attempt.WorkspaceID, "ticket-answer:"+id, in.RequestID, in, func(tx *storeTx) (Item, error) {
		item, err := ticketTx(ctx, tx, attempt.TicketID)
		if err != nil {
			return Item{}, err
		}
		if err := requireWorkspace(ctx, tx, item.WorkspaceID); err != nil {
			return Item{}, err
		}
		a, err := decodeRow[Attempt](tx.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, id))
		if err != nil {
			return Item{}, err
		}
		if item.ActiveAttemptID != id || item.ExecutionState != WaitingUser || a.Status != WaitingUser || a.StopRequested || a.UserQuestion == nil || a.UserQuestion.ID != in.QuestionID || a.UserQuestion.Answer != "" {
			return Item{}, ErrConflict
		}
		body, err := decodeRow[Post](tx.QueryRowContext(ctx, `SELECT payload FROM posts WHERE id=?`, item.BodyPostID))
		if err != nil {
			return Item{}, err
		}
		if ticketContract(item, body.Revision) != a.TicketContract {
			return Item{}, fmt.Errorf("%w: the ticket scope changed after the worker started", ErrConflict)
		}
		a.UserQuestion.Answer = in.Answer
		a.UpdatedAt = time.Now().UTC()
		if err := saveAttempt(ctx, tx, a, false); err != nil {
			return Item{}, err
		}
		item.ExecutionState, item.ExecutionReason = "queued", ""
		item.NextAssignmentAt = time.Time{}
		return touchTicket(ctx, tx, item)
	})
}
