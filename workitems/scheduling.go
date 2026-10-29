package workitems

import (
	"context"
	"encoding/json"
	"time"
)

// Keep these predicates identical in the indexes and reads. SQLite maintains
// membership with the payload mutation, including retries and legacy tickets.
const (
	assignmentQueue = `kind='ticket' AND coalesce(json_extract(payload,'$.deleted'),0)=0 AND (json_extract(payload,'$.execution_state') IN ('queued','assignment_waiting') OR (coalesce(json_extract(payload,'$.execution_state'),'')='' AND json_extract(payload,'$.state')='todo' AND coalesce(json_extract(payload,'$.active_attempt_id'),'')=''))`
	mergeQueue      = `kind='ticket' AND coalesce(json_extract(payload,'$.deleted'),0)=0 AND json_extract(payload,'$.execution_state') IN ('merge_queued','close_queued')`
	assignmentDue   = `coalesce(julianday(json_extract(payload,'$.next_assignment_at')),0)`
	activeAttempts  = `json_extract(payload,'$.status') IN ('reserved','running')`
	leasedAttempts  = `json_extract(payload,'$.status') IN ('reserved','running','merging','closing')`
	pendingInbox    = `coalesce(json_extract(payload,'$.delivered'),0)=0`
)

func (s *Store) initializeScheduling(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS ticket_assignment_queue ON items(workspace_id,seq,`+assignmentDue+`) WHERE `+assignmentQueue+`;
CREATE INDEX IF NOT EXISTS ticket_merge_queue ON items(workspace_id,seq) WHERE `+mergeQueue+`;
CREATE INDEX IF NOT EXISTS attempts_active ON ticket_attempts(workspace_id,json_extract(payload,'$.member_id'),ticket_id) WHERE `+activeAttempts+`;
CREATE INDEX IF NOT EXISTS attempts_lease_expiry ON ticket_attempts(julianday(json_extract(payload,'$.lease_until'))) WHERE `+leasedAttempts+`;
CREATE INDEX IF NOT EXISTS inbox_pending ON manager_inbox_events(workspace_id,julianday(json_extract(payload,'$.created_at')),id) WHERE `+pendingInbox+`;`)
	return err
}

// ListTicketQueue returns only due assignment candidates or pending merge/close
// work. Completed, cancelled, deleted, and already claimed tickets stay out of
// the partial indexes. The normal UI list contract remains unchanged.
func (s *Store) ListTicketQueue(ctx context.Context, workspaceID, queue, token string, limit int) (ItemPage, error) {
	predicate, index := assignmentQueue, "ticket_assignment_queue"
	if queue == "merge" {
		predicate, index = mergeQueue, "ticket_merge_queue"
	} else if queue != "assignment" {
		return ItemPage{}, ErrInvalidInput
	}
	if workspaceID == "" {
		return ItemPage{}, ErrInvalidInput
	}
	c, err := readCursor(token, "ticket-queue:"+workspaceID+":"+queue)
	if err != nil {
		return ItemPage{}, err
	}
	if token == "" {
		if err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(seq),0) FROM items`).Scan(&c.Upper); err != nil {
			return ItemPage{}, err
		}
	}
	query := `SELECT seq,payload FROM items INDEXED BY ` + index + ` WHERE ` + predicate + ` AND workspace_id=? AND seq>? AND seq<=?`
	args := []any{workspaceID, c.After, c.Upper}
	if queue == "assignment" {
		query += ` AND ` + assignmentDue + `<=julianday(?)`
		args = append(args, stamp(time.Now()))
	}
	limit = pageLimit(limit)
	query += ` ORDER BY seq LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return ItemPage{}, err
	}
	defer func() { _ = rows.Close() }()
	out := ItemPage{Items: []Item{}}
	for rows.Next() {
		var seq int64
		var raw []byte
		if err := rows.Scan(&seq, &raw); err != nil {
			return out, err
		}
		if len(out.Items) == limit {
			out.NextCursor = encodeCursor(c)
			break
		}
		var item Item
		if err := json.Unmarshal(raw, &item); err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
		c.After = seq
	}
	return out, rows.Err()
}

type TicketLoad struct {
	Active  int            `json:"active"`
	Members map[string]int `json:"members"`
}

func (s *Store) GetTicketLoad(ctx context.Context, workspaceID string) (TicketLoad, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT json_extract(payload,'$.member_id'),count(*) FROM ticket_attempts WHERE `+activeAttempts+` AND workspace_id=? GROUP BY json_extract(payload,'$.member_id')`, workspaceID)
	if err != nil {
		return TicketLoad{}, err
	}
	defer func() { _ = rows.Close() }()
	out := TicketLoad{Members: map[string]int{}}
	for rows.Next() {
		var member string
		var n int
		if err := rows.Scan(&member, &n); err != nil {
			return out, err
		}
		out.Members[member] = n
		out.Active += n
	}
	return out, rows.Err()
}

// The coordinator delivers one ready event per project at a time. Historical
// acknowledged events are absent from this index and never deserialized here.
func (s *Store) NextPendingInbox(ctx context.Context, workspaceID string) ([]InboxEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM manager_inbox_events WHERE `+pendingInbox+` AND workspace_id=? AND coalesce(julianday(json_extract(payload,'$.next_delivery_at')),0)<=julianday(?) ORDER BY julianday(json_extract(payload,'$.created_at')),id LIMIT 1`, workspaceID, stamp(time.Now()))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []InboxEntry{}
	for rows.Next() {
		var raw []byte
		var entry InboxEntry
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}
