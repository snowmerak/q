package workitemtools

import "context"

const AssignedReviewInstructions = "Q has already selected the attempt for this review. Call ticket_attempt_get with empty arguments. For a Git result, call ticket_change_read with empty arguments. Call ticket_worker_read with optional cursor and limit. Call ticket_review with decision and, when requesting changes, feedback. Q supplies the attempt ID and retry key."

// ReviewAssignment binds one scheduled review turn to the active attempt and
// the host's stable operation key. It is never supplied by the model.
type ReviewAssignment struct {
	AttemptID   string
	OperationID string
}

func (review ReviewAssignment) Active() bool {
	return review.AttemptID != "" && review.OperationID != ""
}

type reviewAssignmentKey struct{}

func WithReviewAssignment(ctx context.Context, review ReviewAssignment) context.Context {
	return context.WithValue(ctx, reviewAssignmentKey{}, review)
}

func AssignedReview(ctx context.Context) ReviewAssignment {
	if ctx == nil {
		return ReviewAssignment{}
	}
	review, _ := ctx.Value(reviewAssignmentKey{}).(ReviewAssignment)
	return review
}
