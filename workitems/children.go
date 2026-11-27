package workitems

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const WaitingChildren = "waiting_children"

// WorkerBinding fences an operation to the currently running worker. Tool hosts
// supply it from the reservation, independently of model-provided arguments.
type WorkerBinding struct {
	AttemptID  string `json:"attempt_id"`
	Owner      string `json:"owner"`
	Token      string `json:"token"`
	Generation int64  `json:"generation"`
}

func (a Attempt) Binding() WorkerBinding {
	return WorkerBinding{AttemptID: a.ID, Owner: a.Owner, Token: a.Token, Generation: a.Generation}
}

func boundWorker(ctx context.Context, tx *storeTx, in WorkerBinding) (Attempt, error) {
	a, err := decodeRow[Attempt](tx.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, in.AttemptID))
	if err != nil {
		return Attempt{}, err
	}
	item, err := ticketTx(ctx, tx, a.TicketID)
	if err != nil {
		return Attempt{}, err
	}
	if in.Token == "" || a.Token != in.Token || a.Owner != in.Owner || a.Generation != in.Generation || item.ActiveAttemptID != a.ID || a.Status != "running" || a.StopRequested || !time.Now().Before(a.LeaseUntil) {
		return Attempt{}, fmt.Errorf("%w: worker reservation is no longer active", ErrConflict)
	}
	member, err := workerMember(ctx, tx, a.WorkspaceID, a.MemberID)
	if err != nil {
		return Attempt{}, err
	}
	if member.Disabled || member.Revision != a.MemberRevision {
		return Attempt{}, ErrConflict
	}
	return a, nil
}

func workerMember(ctx context.Context, tx *storeTx, workspaceID, memberID string) (Member, error) {
	return decodeRow[Member](tx.QueryRowContext(ctx, `SELECT payload FROM workspace_member_bindings WHERE id=? AND workspace_id=?`, memberID, workspaceID))
}

type childQuery interface {
	queryContext(context.Context, string, ...any) (*sql.Rows, error)
}

const childPredicate = `kind='ticket' AND coalesce(json_extract(payload,'$.deleted'),0)=0 AND json_extract(payload,'$.parent_ticket_id')=? AND json_extract(payload,'$.parent_worktree_id')=?`
const childReviewStates = `('awaiting_review','blocked','failed','unknown','merge_failed','close_failed')`

const parentReadySQL = `(json_extract(items.payload,'$.parent_ticket_id') IS NULL OR EXISTS (
 SELECT 1 FROM items AS parent JOIN ticket_attempts AS worker ON worker.id=json_extract(parent.payload,'$.active_attempt_id')
 WHERE parent.id=json_extract(items.payload,'$.parent_ticket_id') AND parent.workspace_id=items.workspace_id
 AND coalesce(json_extract(parent.payload,'$.deleted'),0)=0 AND json_extract(parent.payload,'$.execution_state')='waiting_children'
 AND json_extract(worker.payload,'$.worktree_id')=json_extract(items.payload,'$.parent_worktree_id')))`

const directChildSQL = `child.kind='ticket' AND coalesce(json_extract(child.payload,'$.deleted'),0)=0
 AND json_extract(child.payload,'$.parent_ticket_id')=items.id
 AND json_extract(child.payload,'$.parent_worktree_id')=(SELECT json_extract(worker.payload,'$.worktree_id') FROM ticket_attempts AS worker WHERE worker.id=json_extract(items.payload,'$.active_attempt_id'))`

const resumeReadySQL = `(EXISTS (SELECT 1 FROM items AS child WHERE ` + directChildSQL + ` AND json_extract(child.payload,'$.execution_state') IN ` + childReviewStates + `)
 OR NOT EXISTS (SELECT 1 FROM items AS child WHERE ` + directChildSQL + ` AND json_extract(child.payload,'$.state')<>'done'))
 AND NOT EXISTS (SELECT 1 FROM items AS child WHERE ` + directChildSQL + ` AND json_extract(child.payload,'$.execution_state') IN ('merging','closing'))`

func (s *Store) initializeChildren(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS ticket_parent_worktrees ON items(json_extract(payload,'$.parent_ticket_id'),json_extract(payload,'$.parent_worktree_id'),seq) WHERE kind='ticket' AND coalesce(json_extract(payload,'$.deleted'),0)=0;
CREATE INDEX IF NOT EXISTS ticket_parent_resume ON items(workspace_id,seq) WHERE `+resumeQueue+`; PRAGMA user_version=10;`)
	return err
}

func (s *Store) ListChildren(ctx context.Context, id, worktree string) (ItemPage, error) {
	items, err := children(ctx, s, s.dialect(), id, worktree)
	return ItemPage{Items: items}, err
}

func children(ctx context.Context, db childQuery, d sqlDialect, id, worktree string) ([]Item, error) {
	rows, err := db.queryContext(ctx, `SELECT payload FROM items WHERE `+childPredicateSQL(d)+` ORDER BY seq`, id, worktree)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []Item{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var item Item
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func childContinuation(ctx context.Context, tx *storeTx, previous Attempt) (review string, hasChildren, outstanding bool, err error) {
	items, err := children(ctx, tx, tx.d, previous.TicketID, previous.WorktreeID)
	if err != nil {
		return "", false, false, err
	}
	for _, item := range items {
		if item.ExecutionState == "merging" || item.ExecutionState == "closing" {
			return "", false, false, fmt.Errorf("%w: a child Git operation is running", ErrConflict)
		}
		outstanding = outstanding || item.State != "done"
		if review == "" && reviewableChild(item.ExecutionState) {
			review = item.ActiveAttemptID
		}
	}
	return review, len(items) > 0, outstanding, nil
}

func childParent(ctx context.Context, tx *storeTx, child Item) (Attempt, error) {
	parent, err := ticketTx(ctx, tx, child.ParentTicketID)
	if err != nil {
		return Attempt{}, err
	}
	a, err := decodeRow[Attempt](tx.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, parent.ActiveAttemptID))
	if err != nil {
		return Attempt{}, err
	}
	if a.WorktreeID != child.ParentWorktreeID || a.WorkspaceID != child.WorkspaceID || a.StopRequested || a.ChangeRequest == nil || a.ChangeRequest.Status != "working" {
		return Attempt{}, fmt.Errorf("%w: parent worktree is no longer available", ErrConflict)
	}
	return a, nil
}

func reviewableChild(state string) bool {
	switch state {
	case "awaiting_review", "failed", "blocked", "unknown", "merge_failed", "close_failed":
		return true
	}
	return false
}

func cancelTicketTree(ctx context.Context, tx *storeTx, item Item, reason string) (Item, error) {
	if item.State == "done" {
		return Item{}, fmt.Errorf("%w: completed tickets cannot be cancelled", ErrConflict)
	}
	if item.ExecutionState == "merging" || item.ExecutionState == "closing" {
		return Item{}, fmt.Errorf("%w: Git review operation is already running", ErrConflict)
	}
	item.ExecutionState, item.ExecutionReason, item.HandoffMemberID = "cancelled", reason, ""
	if item.ActiveAttemptID == "" {
		return touchTicket(ctx, tx, item)
	}
	a, err := decodeRow[Attempt](tx.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, item.ActiveAttemptID))
	if err != nil {
		return Item{}, err
	}
	if a.MergeLanded {
		return Item{}, fmt.Errorf("%w: Git has already applied this merge; retry its cleanup", ErrConflict)
	}
	entries, err := children(ctx, tx, tx.d, item.ID, a.WorktreeID)
	if err != nil {
		return Item{}, err
	}
	for _, child := range entries {
		if child.State != "done" && child.ExecutionState != "cancelled" && child.ExecutionState != "rejected" {
			if _, err := cancelTicketTree(ctx, tx, child, "parent ticket cancelled"); err != nil {
				return Item{}, err
			}
		}
	}
	a.StopRequested = true
	a.UpdatedAt = time.Now().UTC()
	if a.Status == "reserved" || a.Status == "running" {
		item.ExecutionState = "stopping"
	} else {
		a.Status = cancelledAttemptStatus(a)
		a.Decision, a.DecisionID = "", ""
		item.ExecutionState = a.Status
	}
	if err := saveAttempt(ctx, tx, a, false); err != nil {
		return Item{}, err
	}
	return touchTicket(ctx, tx, item)
}
