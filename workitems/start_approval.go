package workitems

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const WaitingApproval = "waiting_approval"

func ticketContract(item Item, bodyRevision int64) string {
	raw, _ := json.Marshal([]any{item.Title, item.LinkedIssueID, item.ExecutionMode, item.DependsOn, item.Acceptance, item.RequestedRole, item.RequestedMemberID, bodyRevision})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func requiresFreshStartApproval(item Item) bool {
	return item.RequireStartApproval && item.ActiveAttemptID == "" && (item.Schedule == nil || item.Schedule.Every == "") &&
		(item.ExecutionState == WaitingApproval || item.ExecutionState == "queued" || item.ExecutionState == "assignment_waiting")
}

func resetStartApproval(item *Item) {
	item.ApprovedStartContract = ""
	item.ExecutionState = WaitingApproval
	item.ExecutionReason = ""
}

func approveTicketStart(ctx context.Context, tx *sql.Tx, item Item, action TicketAction) (Item, error) {
	if !item.RequireStartApproval || item.ExecutionState != WaitingApproval || item.ActiveAttemptID != "" || action.RequestID == "" {
		return Item{}, ErrConflict
	}
	if err := requireWorkspace(ctx, tx, item.WorkspaceID); err != nil {
		return Item{}, err
	}
	if err := requireTicketIssue(ctx, tx, item); err != nil {
		return Item{}, err
	}
	body, err := decodeRow[Post](tx.QueryRowContext(ctx, `SELECT payload FROM posts WHERE id=?`, item.BodyPostID))
	if err != nil {
		return Item{}, err
	}
	if body.Deleted {
		return Item{}, fmt.Errorf("%w: ticket body is deleted", ErrConflict)
	}
	item.ApprovedStartContract = ticketContract(item, body.Revision)
	item.ExecutionState, item.ExecutionReason = "queued", ""
	return touchTicket(ctx, tx, item)
}
