package workitems

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/snowmerak/q/change"
)

// Member references an existing profile. Definitions remain in their original
// global/workspace profile store and are resolved again before every assignment.
type Member struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	AgentID        string `json:"agent_id"`
	DefinitionRoot string `json:"definition_root"`
	ExecutionRoot  string `json:"execution_root"`
	Disabled       bool   `json:"disabled"`
	Revision       int64  `json:"revision"`
}

type MemberInput struct {
	ID             string `json:"id,omitempty"`
	AgentID        string `json:"agent_id"`
	DefinitionRoot string `json:"definition_root"`
	ExecutionRoot  string `json:"execution_root"`
	Disabled       bool   `json:"disabled"`
	Revision       int64  `json:"revision,omitempty"`
	RequestID      string `json:"request_id,omitempty"`
}

// Attempt is a fenced execution reservation. Lease loss never implies that
// external effects did not happen: recovery requires an explicit retry.
type Attempt struct {
	ID                string          `json:"id"`
	WorkspaceID       string          `json:"workspace_id"`
	TicketID          string          `json:"ticket_id"`
	MemberID          string          `json:"member_id"`
	MemberRevision    int64           `json:"member_revision"`
	DefinitionDigest  string          `json:"definition_digest"`
	TicketRevision    int64           `json:"ticket_revision"`
	BodyRevision      int64           `json:"body_revision"`
	Acceptance        []string        `json:"acceptance"`
	WorkerSessionID   string          `json:"worker_session_id"`
	WorktreeID        string          `json:"worktree_id"`
	PreviousAttemptID string          `json:"previous_attempt_id,omitempty"`
	PreviousStatus    string          `json:"previous_status,omitempty"`
	Generation        int64           `json:"generation"`
	Owner             string          `json:"owner"`
	Token             string          `json:"token"`
	LeaseUntil        time.Time       `json:"lease_until"`
	Status            string          `json:"status"`
	StopRequested     bool            `json:"stop_requested,omitempty"`
	Result            json.RawMessage `json:"result,omitempty"`
	ChangeRequest     *change.Request `json:"change_request,omitempty"`
	Feedback          string          `json:"feedback,omitempty"`
	Decision          string          `json:"decision,omitempty"`
	DecisionID        string          `json:"decision_id,omitempty"`
	OperationError    string          `json:"operation_error,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type InboxEntry struct {
	ID               string    `json:"id"`
	WorkspaceID      string    `json:"workspace_id"`
	TicketID         string    `json:"ticket_id"`
	AttemptID        string    `json:"attempt_id"`
	AttemptStatus    string    `json:"attempt_status"`
	ManagerSessionID string    `json:"manager_session_id"`
	OperationID      string    `json:"operation_id"`
	Delivered        bool      `json:"delivered"`
	DeliveryError    string    `json:"delivery_error,omitempty"`
	DeliveryAttempts int       `json:"delivery_attempts,omitempty"`
	NextDeliveryAt   time.Time `json:"next_delivery_at,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type TicketAction struct {
	Action    string `json:"action"`
	Revision  int64  `json:"revision"`
	Reason    string `json:"reason,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

type ClaimInput struct {
	ParallelLimit    int    `json:"parallel_limit,omitempty"`
	Revision         int64  `json:"revision"`
	MemberID         string `json:"member_id"`
	MemberRevision   int64  `json:"member_revision"`
	DefinitionDigest string `json:"definition_digest"`
	Owner            string `json:"owner"`
	RequestID        string `json:"request_id"`
}

type AttemptUpdate struct {
	Owner          string          `json:"owner"`
	Token          string          `json:"token"`
	Generation     int64           `json:"generation"`
	Status         string          `json:"status"`
	Result         json.RawMessage `json:"result,omitempty"`
	ChangeRequest  *change.Request `json:"change_request,omitempty"`
	OperationError string          `json:"operation_error,omitempty"`
}

type ReviewInput struct {
	ManagerSessionID string `json:"manager_session_id"`
	Decision         string `json:"decision"`
	Feedback         string `json:"feedback,omitempty"`
	RequestID        string `json:"request_id"`
}

func (s *Store) initializeCoordinator(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS workspace_members (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), agent_id TEXT NOT NULL, payload BLOB NOT NULL, UNIQUE(workspace_id,agent_id));
CREATE TABLE IF NOT EXISTS workspace_member_bindings (id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id),agent_id TEXT NOT NULL,definition_root TEXT NOT NULL,execution_root TEXT NOT NULL,payload BLOB NOT NULL,UNIQUE(workspace_id,agent_id,definition_root,execution_root));
INSERT OR IGNORE INTO workspace_member_bindings SELECT id,workspace_id,agent_id,json_extract(payload,'$.definition_root'),json_extract(payload,'$.execution_root'),payload FROM workspace_members;
CREATE TABLE IF NOT EXISTS ticket_attempts (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, workspace_id TEXT NOT NULL REFERENCES workspaces(id), ticket_id TEXT NOT NULL REFERENCES items(id), payload BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS attempts_ticket ON ticket_attempts(ticket_id,seq);
CREATE TABLE IF NOT EXISTS manager_inbox (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), attempt_id TEXT NOT NULL UNIQUE REFERENCES ticket_attempts(id), payload BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS manager_inbox_events (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), attempt_id TEXT NOT NULL REFERENCES ticket_attempts(id), payload BLOB NOT NULL);
INSERT OR IGNORE INTO manager_inbox_events SELECT id,workspace_id,attempt_id,payload FROM manager_inbox;
`)
	return err
}

func (s *Store) ListMembers(ctx context.Context, workspaceID string) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM workspace_member_bindings WHERE workspace_id=? ORDER BY agent_id,definition_root,execution_root`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := []Member{}
	for rows.Next() {
		var raw []byte
		var value Member
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) PutMember(ctx context.Context, workspaceID string, in MemberInput) (Member, error) {
	if strings.TrimSpace(in.AgentID) == "" || len(in.AgentID) > 256 || !filepath.IsAbs(in.DefinitionRoot) || !filepath.IsAbs(in.ExecutionRoot) {
		return Member{}, ErrInvalidInput
	}
	return mutate(ctx, s, workspaceID, "member", in.RequestID, in, func(tx *sql.Tx) (Member, error) {
		return putMemberTx(ctx, tx, workspaceID, in)
	})
}

func putMemberTx(ctx context.Context, tx *sql.Tx, workspaceID string, in MemberInput) (Member, error) {
	if strings.TrimSpace(in.AgentID) == "" || len(in.AgentID) > 256 || !filepath.IsAbs(in.DefinitionRoot) || !filepath.IsAbs(in.ExecutionRoot) {
		return Member{}, ErrInvalidInput
	}

	var belongs int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workspace_roots WHERE workspace_id=? AND path=? AND active=1`, workspaceID, in.ExecutionRoot).Scan(&belongs); err != nil {
		return Member{}, fmt.Errorf("%w: execution root must belong to the project", ErrInvalidInput)
	}
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workspace_roots WHERE workspace_id=? AND path=? AND active=1`, workspaceID, in.DefinitionRoot).Scan(&belongs); err != nil {
		return Member{}, fmt.Errorf("%w: definition root must belong to the project", ErrInvalidInput)
	}
	value := Member{ID: in.ID, WorkspaceID: workspaceID, AgentID: in.AgentID, DefinitionRoot: filepath.Clean(in.DefinitionRoot), ExecutionRoot: filepath.Clean(in.ExecutionRoot), Disabled: in.Disabled, Revision: 1}
	if in.ID != "" {
		old, err := decodeRow[Member](tx.QueryRowContext(ctx, `SELECT payload FROM workspace_member_bindings WHERE id=? AND workspace_id=?`, in.ID, workspaceID))
		if err != nil {
			return Member{}, err
		}
		if old.Revision != in.Revision {
			return Member{}, ErrConflict
		}
		value.Revision = old.Revision + 1
	} else {
		value.ID = uuid.NewString()
		if in.Revision != 0 {
			return Member{}, ErrConflict
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return Member{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workspace_member_bindings(id,workspace_id,agent_id,definition_root,execution_root,payload) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET agent_id=excluded.agent_id,definition_root=excluded.definition_root,execution_root=excluded.execution_root,payload=excluded.payload`, value.ID, workspaceID, value.AgentID, value.DefinitionRoot, value.ExecutionRoot, raw)
	if err != nil {
		return Member{}, mutationError(err)
	}
	return value, nil
}

func ticketTx(ctx context.Context, tx *sql.Tx, id string) (Item, error) {
	item, err := decodeRow[Item](tx.QueryRowContext(ctx, `SELECT payload FROM items WHERE id=? AND kind='ticket'`, id))
	if err != nil {
		return item, err
	}
	if item.Deleted {
		return Item{}, ErrNotFound
	}
	return item, nil
}

func touchTicket(ctx context.Context, tx *sql.Tx, item Item) (Item, error) {
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	return item, saveItem(ctx, tx, item)
}

func (s *Store) ControlTicket(ctx context.Context, id string, in TicketAction) (Item, error) {
	current, err := s.GetItem(ctx, id)
	if err != nil {
		return Item{}, err
	}
	return mutate(ctx, s, current.WorkspaceID, "ticket-control:"+id, in.RequestID, in, func(tx *sql.Tx) (Item, error) {
		item, err := ticketTx(ctx, tx, id)
		if err != nil {
			return item, err
		}
		if item.Revision != in.Revision {
			return Item{}, ErrConflict
		}
		switch in.Action {
		case "queue", "retry":
			if in.Action == "queue" && item.ExecutionState == "queued" {
				return item, nil
			}
			if item.State == "done" || item.ExecutionState == "running" || item.ExecutionState == "reserved" || item.ExecutionState == "awaiting_review" || item.ExecutionState == "merging" || item.ExecutionState == "merge_queued" || item.ExecutionState == "closing" || item.ExecutionState == "close_queued" || item.ExecutionState == "stopping" {
				return Item{}, ErrConflict
			}
			item.ExecutionState = "queued"
			item.ExecutionReason = ""
			item.NextAssignmentAt = time.Time{}
		case "assignment_waiting":
			if item.ExecutionState != "queued" && item.ExecutionState != "assignment_waiting" {
				return Item{}, ErrConflict
			}
			item.ExecutionState = "assignment_waiting"
			item.ExecutionReason = in.Reason
			item.NextAssignmentAt = time.Now().UTC().Add(time.Minute)
		case "cancel":
			if item.ExecutionState == "merging" || item.ExecutionState == "closing" {
				return Item{}, fmt.Errorf("%w: Git review operation is already running", ErrConflict)
			}
			if item.ExecutionState == "running" || item.ExecutionState == "reserved" || item.ExecutionState == "stopping" {
				a, err := decodeRow[Attempt](tx.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, item.ActiveAttemptID))
				if err != nil {
					return Item{}, err
				}
				a.StopRequested = true
				if err := saveAttempt(ctx, tx, a, false); err != nil {
					return Item{}, err
				}
				item.ExecutionState = "stopping"
				return touchTicket(ctx, tx, item)
			}
			item.ExecutionState = "cancelled"
			item.ExecutionReason = in.Reason
		default:
			return Item{}, ErrInvalidInput
		}
		return touchTicket(ctx, tx, item)
	})
}

func (s *Store) ClaimTicket(ctx context.Context, id string, in ClaimInput) (Attempt, error) {
	current, err := s.GetItem(ctx, id)
	if err != nil {
		return Attempt{}, err
	}
	if in.Owner == "" || len(in.Owner) > 256 || in.RequestID == "" || in.DefinitionDigest == "" {
		return Attempt{}, ErrInvalidInput
	}
	return mutate(ctx, s, current.WorkspaceID, "claim:"+id, in.RequestID, in, func(tx *sql.Tx) (Attempt, error) {
		item, err := ticketTx(ctx, tx, id)
		if err != nil {
			return Attempt{}, err
		}
		if item.Revision != in.Revision || (item.ExecutionState != "queued" && item.ExecutionState != "assignment_waiting") {
			return Attempt{}, ErrConflict
		}
		limit := in.ParallelLimit
		if limit <= 0 {
			limit = 3
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ticket_attempts WHERE `+activeAttempts).Scan(&active); err != nil {
			return Attempt{}, err
		}
		if active >= limit {
			return Attempt{}, fmt.Errorf("%w: ticket execution capacity is busy", ErrConflict)
		}
		var projectLimit, projectActive int
		if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT max_parallel_tickets FROM workspace_settings WHERE workspace_id=?),0)`, item.WorkspaceID).Scan(&projectLimit); err != nil {
			return Attempt{}, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ticket_attempts WHERE workspace_id=? AND `+activeAttempts, item.WorkspaceID).Scan(&projectActive); err != nil {
			return Attempt{}, err
		}
		if projectActive >= EffectiveParallel(projectLimit, limit) {
			return Attempt{}, fmt.Errorf("%w: project ticket capacity is busy", ErrConflict)
		}
		member, err := decodeRow[Member](tx.QueryRowContext(ctx, `SELECT payload FROM workspace_member_bindings WHERE id=? AND workspace_id=?`, in.MemberID, item.WorkspaceID))
		if err != nil {
			return Attempt{}, err
		}
		if member.Disabled || member.Revision != in.MemberRevision || (item.RequestedMemberID != "" && item.RequestedMemberID != member.ID) {
			return Attempt{}, ErrConflict
		}
		now := time.Now().UTC()
		attempt := Attempt{ID: uuid.NewString(), WorkspaceID: item.WorkspaceID, TicketID: id, MemberID: member.ID, MemberRevision: member.Revision, DefinitionDigest: in.DefinitionDigest, WorkerSessionID: uuid.NewString(), Generation: 1, Owner: in.Owner, Token: uuid.NewString(), LeaseUntil: now.Add(45 * time.Second), Status: "reserved", CreatedAt: now, UpdatedAt: now}
		attempt.WorktreeID = attempt.ID
		attempt.TicketRevision = item.Revision
		attempt.Acceptance = append([]string(nil), item.Acceptance...)
		body, err := decodeRow[Post](tx.QueryRowContext(ctx, `SELECT payload FROM posts WHERE id=?`, item.BodyPostID))
		if err != nil {
			return Attempt{}, err
		}
		attempt.BodyRevision = body.Revision
		if item.ActiveAttemptID != "" {
			previous, err := decodeRow[Attempt](tx.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, item.ActiveAttemptID))
			if err != nil {
				return Attempt{}, err
			}
			if previous.Status == "running" || previous.Status == "reserved" || previous.Status == "awaiting_review" || previous.Status == "merging" || previous.Status == "closing" || previous.Status == "merge_queued" || previous.Status == "close_queued" {
				return Attempt{}, ErrConflict
			}
			attempt.Generation = previous.Generation + 1
			attempt.PreviousAttemptID = previous.ID
			attempt.PreviousStatus = previous.Status
			attempt.Feedback = previous.Feedback
			if previous.ChangeRequest != nil && !previous.ChangeRequest.Final() && (previous.MemberID != member.ID || previous.DefinitionDigest != in.DefinitionDigest) {
				return Attempt{}, fmt.Errorf("%w: active worktree requires the original member and profile", ErrConflict)
			}
			// Continuing a checkpoint/worktree is allowed only for the same member
			// and unchanged definition. Otherwise a fresh worker must be chosen.
			if previous.MemberID == member.ID && previous.DefinitionDigest == in.DefinitionDigest && (previous.ChangeRequest == nil || !previous.ChangeRequest.Final()) {
				attempt.WorkerSessionID = previous.WorkerSessionID
				attempt.WorktreeID = previous.WorktreeID
				attempt.ChangeRequest = previous.ChangeRequest
			}
		}
		if err := saveAttempt(ctx, tx, attempt, true); err != nil {
			return Attempt{}, err
		}
		item.ActiveAttemptID = attempt.ID
		item.AssignedMemberID = member.ID
		item.State = "in_progress"
		item.ExecutionState = "reserved"
		item.ExecutionReason = ""
		_, err = touchTicket(ctx, tx, item)
		return attempt, err
	})
}

func saveAttempt(ctx context.Context, tx *sql.Tx, a Attempt, create bool) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if create {
		_, err = tx.ExecContext(ctx, `INSERT INTO ticket_attempts(id,workspace_id,ticket_id,payload) VALUES(?,?,?,?)`, a.ID, a.WorkspaceID, a.TicketID, raw)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE ticket_attempts SET payload=? WHERE id=?`, raw, a.ID)
	}
	return err
}

func (s *Store) GetAttempt(ctx context.Context, id string) (Attempt, error) {
	return decodeRow[Attempt](s.db.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, id))
}
func (s *Store) ListAttempts(ctx context.Context, ticketID string) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM ticket_attempts WHERE ticket_id=? ORDER BY seq`, ticketID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Attempt{}
	for rows.Next() {
		var raw []byte
		var a Attempt
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAttempt validates the current reservation on every heartbeat and
// checkpoint publication. A stale worker cannot complete a newer attempt.
func (s *Store) UpdateAttempt(ctx context.Context, id string, in AttemptUpdate) (Attempt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attempt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := decodeRow[Attempt](tx.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, id))
	if err != nil {
		return a, err
	}
	item, err := ticketTx(ctx, tx, a.TicketID)
	if err != nil {
		return Attempt{}, err
	}
	if err := requireWorkspace(ctx, tx, a.WorkspaceID); err != nil {
		return Attempt{}, err
	}
	if a.Token != in.Token || a.Owner != in.Owner || a.Generation != in.Generation || item.ActiveAttemptID != a.ID || time.Now().After(a.LeaseUntil) || (a.Status != "reserved" && a.Status != "running" && a.Status != "merging" && a.Status != "closing") {
		return Attempt{}, ErrConflict
	}
	if len(in.Result) > 2<<20 || (len(in.Result) > 0 && !json.Valid(in.Result)) {
		return Attempt{}, ErrInvalidInput
	}
	status := in.Status
	if status == "" {
		status = a.Status
	}
	if (a.Status == "reserved" || a.Status == "running") && status == "awaiting_review" {
		member, err := decodeRow[Member](tx.QueryRowContext(ctx, `SELECT payload FROM workspace_member_bindings WHERE id=?`, a.MemberID))
		if err != nil {
			return Attempt{}, err
		}
		if a.StopRequested {
			status = "cancelled"
		} else if member.Disabled || member.Revision != a.MemberRevision {
			status = "blocked"
			a.OperationError = "workspace membership changed during execution; review the saved worker result before retrying"
		}
	}
	if status == "running" {
		member, err := decodeRow[Member](tx.QueryRowContext(ctx, `SELECT payload FROM workspace_member_bindings WHERE id=?`, a.MemberID))
		if err != nil {
			return Attempt{}, err
		}
		if a.StopRequested || member.Disabled || member.Revision != a.MemberRevision {
			return Attempt{}, ErrConflict
		}
	}
	allowed := status == a.Status || a.Status == "reserved" && status == "running" || (a.Status == "reserved" || a.Status == "running") && (status == "awaiting_review" || status == "failed" || status == "blocked" || status == "unknown" || status == "cancelled") || a.Status == "merging" && (status == "accepted" || status == "merge_failed") || a.Status == "closing" && (status == "rejected" || status == "close_failed")
	if !allowed {
		return Attempt{}, ErrConflict
	}
	previousStatus := a.Status
	if status == "accepted" && (in.ChangeRequest == nil || in.ChangeRequest.Status != "merged" || in.ChangeRequest.MergedCommit == "") {
		return Attempt{}, ErrInvalidInput
	}
	a.Status = status
	a.UpdatedAt = time.Now().UTC()
	a.LeaseUntil = a.UpdatedAt.Add(45 * time.Second)
	if len(in.Result) > 0 {
		a.Result = in.Result
	}
	if in.ChangeRequest != nil {
		a.ChangeRequest = in.ChangeRequest
	}
	if status == "merge_failed" || status == "close_failed" || status == "accepted" || status == "rejected" {
		a.OperationError = in.OperationError
	}
	if err := saveAttempt(ctx, tx, a, false); err != nil {
		return Attempt{}, err
	}
	if (previousStatus == "running" || previousStatus == "reserved") && status != previousStatus && status != "running" && len(a.Result) > 0 {
		if err := appendCoordinatorPost(ctx, tx, item, "agent/"+a.WorkerSessionID, string(a.Result), []string{"attempt:" + a.ID, "session:" + a.WorkerSessionID}); err != nil {
			return Attempt{}, err
		}
		item.SourceRevision++
	}
	if item.ExecutionState != status {
		item.ExecutionState = status
		if status == "accepted" {
			item.State = "done"
		}
		if _, err := touchTicket(ctx, tx, item); err != nil {
			return Attempt{}, err
		}
	}
	if status == "awaiting_review" || status == "failed" || status == "blocked" || status == "unknown" || status == "merge_failed" || status == "close_failed" {
		if err := putInbox(ctx, tx, a); err != nil {
			return Attempt{}, err
		}
	}
	return a, tx.Commit()
}

func putInbox(ctx context.Context, tx *sql.Tx, a Attempt) error {
	var managerID string
	if err := tx.QueryRowContext(ctx, `SELECT manager_session_id FROM workspaces WHERE id=?`, a.WorkspaceID).Scan(&managerID); err != nil {
		return err
	}
	identity := a.ID + ":" + a.Status + ":" + a.DecisionID
	entry := InboxEntry{ID: identity, WorkspaceID: a.WorkspaceID, TicketID: a.TicketID, AttemptID: a.ID, AttemptStatus: a.Status, ManagerSessionID: managerID, OperationID: "ticket-review-" + identity, CreatedAt: time.Now().UTC()}
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO manager_inbox_events(id,workspace_id,attempt_id,payload) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`, entry.ID, entry.WorkspaceID, a.ID, raw)
	return err
}

func (s *Store) ListInbox(ctx context.Context, workspaceID string) ([]InboxEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM manager_inbox_events WHERE workspace_id=? ORDER BY id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []InboxEntry{}
	for rows.Next() {
		var raw []byte
		var value InboxEntry
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, rows.Err()
}

func (s *Store) AcknowledgeInbox(ctx context.Context, id, managerID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	entry, err := decodeRow[InboxEntry](tx.QueryRowContext(ctx, `SELECT payload FROM manager_inbox_events WHERE id=?`, id))
	if err != nil {
		return err
	}
	if entry.ManagerSessionID != managerID {
		return ErrConflict
	}
	entry.Delivered = true
	entry.DeliveryError = ""
	entry.NextDeliveryAt = time.Time{}
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE manager_inbox_events SET payload=? WHERE id=?`, raw, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeferInbox(ctx context.Context, id, managerID, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	entry, err := decodeRow[InboxEntry](tx.QueryRowContext(ctx, `SELECT payload FROM manager_inbox_events WHERE id=?`, id))
	if err != nil {
		return err
	}
	if entry.ManagerSessionID != managerID || entry.Delivered {
		return ErrConflict
	}
	if len(reason) > 4096 {
		reason = reason[:4096]
	}
	entry.DeliveryError = reason
	entry.DeliveryAttempts++
	entry.NextDeliveryAt = time.Now().UTC().Add(time.Duration(min(entry.DeliveryAttempts, 5)) * time.Minute)
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE manager_inbox_events SET payload=? WHERE id=?`, raw, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReviewAttempt(ctx context.Context, id string, in ReviewInput) (Attempt, error) {
	current, err := s.GetAttempt(ctx, id)
	if err != nil {
		return Attempt{}, err
	}
	if in.RequestID == "" || len(in.Feedback) > 65536 {
		return Attempt{}, ErrInvalidInput
	}
	return mutate(ctx, s, current.WorkspaceID, "review:"+id, in.RequestID, in, func(tx *sql.Tx) (Attempt, error) {
		var managerID string
		if err := tx.QueryRowContext(ctx, `SELECT manager_session_id FROM workspaces WHERE id=?`, current.WorkspaceID).Scan(&managerID); err != nil {
			return Attempt{}, err
		}
		if managerID != in.ManagerSessionID {
			return Attempt{}, fmt.Errorf("%w: only the workspace Integrator may review attempts", ErrConflict)
		}
		a, err := decodeRow[Attempt](tx.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, id))
		if err != nil {
			return a, err
		}
		item, err := ticketTx(ctx, tx, a.TicketID)
		if err != nil {
			return Attempt{}, err
		}
		if item.ActiveAttemptID != id || (a.Status != "awaiting_review" && a.Status != "blocked" && a.Status != "failed" && a.Status != "unknown" && a.Status != "merge_failed" && a.Status != "close_failed") {
			return Attempt{}, ErrConflict
		}
		a.Decision = in.Decision
		a.DecisionID = in.RequestID
		a.Feedback = in.Feedback
		a.UpdatedAt = time.Now().UTC()
		switch in.Decision {
		case "accept":
			if a.Status != "awaiting_review" && a.Status != "merge_failed" {
				return Attempt{}, ErrConflict
			}
			if item.ExecutionMode == "git" {
				if a.ChangeRequest == nil || a.ChangeRequest.HeadCommit == "" {
					return Attempt{}, ErrConflict
				}
				a.Status = "merge_queued"
				item.ExecutionState = "merge_queued"
			} else {
				a.Status = "accepted"
				item.ExecutionState = "accepted"
				item.State = "done"
			}
		case "request_changes":
			if strings.TrimSpace(in.Feedback) == "" {
				return Attempt{}, ErrInvalidInput
			}
			a.Status = "changes_requested"
			item.ExecutionState = "queued"
			item.RequestedMemberID = a.MemberID
			item.NextAssignmentAt = time.Time{}
		case "reject":
			a.Status = "rejected"
			item.ExecutionState = "rejected"
			if a.ChangeRequest != nil && !a.ChangeRequest.Final() {
				a.Status = "close_queued"
				item.ExecutionState = "close_queued"
			}
		default:
			return Attempt{}, ErrInvalidInput
		}
		if err := saveAttempt(ctx, tx, a, false); err != nil {
			return Attempt{}, err
		}
		note := "Review " + in.Decision + " for attempt " + a.ID
		if in.Feedback != "" {
			note += "\n\n" + in.Feedback
		}
		if err := appendCoordinatorPost(ctx, tx, item, "agent/"+managerID, note, []string{"attempt:" + a.ID}); err != nil {
			return Attempt{}, err
		}
		item.SourceRevision++
		_, err = touchTicket(ctx, tx, item)
		return a, err
	})
}

func appendCoordinatorPost(ctx context.Context, tx *sql.Tx, item Item, author, content string, refs []string) error {
	now := time.Now().UTC()
	post := Post{ID: uuid.NewString(), WorkspaceID: item.WorkspaceID, ItemID: item.ID, Kind: "reply", Content: content, Author: author, Refs: refs, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq)+1 FROM posts WHERE item_id=?`, item.ID).Scan(&post.Seq); err != nil {
		return err
	}
	return savePost(ctx, tx, post, true)
}

// RecoverExpired records uncertain executions without replaying them. The
// per-ticket filesystem lock remains the side-effect exclusion boundary.
func (s *Store) RecoverExpired(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM ticket_attempts WHERE `+leasedAttempts+` AND julianday(json_extract(payload,'$.lease_until'))<julianday(?)`, stamp(time.Now()))
	if err != nil {
		return err
	}
	var expired []Attempt
	for rows.Next() {
		var raw []byte
		var a Attempt
		if err = rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &a); err != nil {
			_ = rows.Close()
			return err
		}
		if (a.Status == "reserved" || a.Status == "running" || a.Status == "merging" || a.Status == "closing") && time.Now().After(a.LeaseUntil) {
			expired = append(expired, a)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, a := range expired {
		item, err := ticketTx(ctx, tx, a.TicketID)
		if err != nil {
			return err
		}
		if item.ActiveAttemptID != a.ID {
			continue
		}
		previousStatus := a.Status
		a.Status = "unknown"
		if previousStatus == "merging" {
			a.Status = "merge_queued"
		}
		if previousStatus == "closing" {
			a.Status = "close_queued"
		}
		a.UpdatedAt = time.Now().UTC()
		if err := saveAttempt(ctx, tx, a, false); err != nil {
			return err
		}
		item.ExecutionState = a.Status
		item.ExecutionReason = "execution lease expired; inspect the persisted worker before retrying"
		if _, err := touchTicket(ctx, tx, item); err != nil {
			return err
		}
		if a.Status == "unknown" {
			if err := putInbox(ctx, tx, a); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) ClaimMerge(ctx context.Context, id, owner string) (Attempt, error) {
	if owner == "" {
		return Attempt{}, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attempt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := decodeRow[Attempt](tx.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, id))
	if err != nil {
		return a, err
	}
	item, err := ticketTx(ctx, tx, a.TicketID)
	if err != nil {
		return Attempt{}, err
	}
	if err := requireWorkspace(ctx, tx, a.WorkspaceID); err != nil {
		return Attempt{}, err
	}
	if (a.Status != "merge_queued" && a.Status != "close_queued") || item.ActiveAttemptID != a.ID {
		return Attempt{}, ErrConflict
	}
	a.Owner = owner
	a.Token = uuid.NewString()
	a.Generation++
	if a.Status == "close_queued" {
		a.Status = "closing"
	} else {
		a.Status = "merging"
	}
	a.UpdatedAt = time.Now().UTC()
	a.LeaseUntil = a.UpdatedAt.Add(45 * time.Second)
	if err := saveAttempt(ctx, tx, a, false); err != nil {
		return Attempt{}, err
	}
	item.ExecutionState = a.Status
	if _, err := touchTicket(ctx, tx, item); err != nil {
		return Attempt{}, err
	}
	return a, tx.Commit()
}
