package subagent

import (
	"context"
	"errors"
	"slices"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workitems"
)

var errTicketGuidancePending = errors.New("new user guidance is pending; reconsider this call after reading it")

type TicketGuidanceControl struct {
	// Pending optionally closes admission in the same transaction if the queue is empty.
	Pending     func(ctx context.Context, seal bool) (*workitems.TicketGuidance, error)
	Acknowledge func(ctx context.Context, id string) error
}

func (r *GeneralRunner) sealTicketGuidance(ctx context.Context) error {
	if r.TicketGuidance == nil {
		return nil
	}
	pending, err := r.TicketGuidance.Pending(ctx, true)
	if err == nil && pending != nil {
		return errTicketGuidancePending
	}
	return err
}

func (r *GeneralRunner) applyTicketGuidance(ctx context.Context, state *GeneralRunState, history *ContextCompactor, lifecycle *Lifecycle, checkpoint func() error) error {
	if r.TicketGuidance == nil {
		return nil
	}
	guidance, err := r.TicketGuidance.Pending(ctx, false)
	if err != nil || guidance == nil {
		return err
	}
	name := workitems.TicketGuidanceMessagePrefix + guidance.ID
	if !slices.ContainsFunc(state.Transcript, func(message client.Message) bool {
		return message.Role == client.RoleUser && message.Name == name
	}) {
		message := client.Message{Role: client.RoleUser, Name: name, Content: guidance.Content}
		history.Append(message)
		state.Transcript = append(state.Transcript, message)
		state.TurnStartRound, state.Reminders = state.Round, 0
		if err := checkpoint(); err != nil {
			return err
		}
		if lifecycle != nil {
			if err := lifecycle.Message(message); err != nil {
				return err
			}
		}
	}
	return r.TicketGuidance.Acknowledge(ctx, guidance.ID)
}
