package workitems

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type HumanInboxEntry struct {
	WorkspaceID string        `json:"workspace_id"`
	Project     string        `json:"project"`
	TicketID    string        `json:"ticket_id"`
	Number      int64         `json:"number"`
	Title       string        `json:"title"`
	Status      string        `json:"status"`
	AttemptID   string        `json:"attempt_id,omitempty"`
	Detail      string        `json:"detail,omitempty"`
	UpdatedAt   time.Time     `json:"updated_at"`
	Question    *UserQuestion `json:"question,omitempty"`
	Revision    int64         `json:"revision"`
}

type HumanInbox struct {
	Entries []HumanInboxEntry `json:"entries"`
	More    bool              `json:"more,omitempty"`
}

func (s *Store) HumanInbox(ctx context.Context, workspaceID string) (_ HumanInbox, returnErr error) {
	d := s.dialect()
	rows, err := s.queryContext(ctx, `
SELECT w.name, i.payload, coalesce(a.payload,`+d.emptyJSONLit()+`)
FROM items i JOIN workspaces w ON w.id=i.workspace_id
LEFT JOIN ticket_attempts a ON a.id=`+d.jsonText("i.payload", "active_attempt_id")+`
WHERE i.kind='ticket' AND w.archived_at IS NULL AND (?='' OR w.id=?)
  AND `+d.jsonBoolFalse("i.payload", "deleted")+`
  AND `+d.jsonText("i.payload", "execution_state")+` IN
    ('awaiting_review','blocked','failed','unknown','merge_failed','close_failed','waiting_user','waiting_approval')
  AND (coalesce(`+d.jsonText("i.payload", "parent_ticket_id")+`,'')='' OR `+d.jsonText("i.payload", "execution_state")+`<>'awaiting_review')
ORDER BY `+d.jsonText("i.payload", "updated_at")+` DESC, i.seq DESC LIMIT 101`, workspaceID, workspaceID)
	if err != nil {
		return HumanInbox{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	out := HumanInbox{Entries: []HumanInboxEntry{}}
	for rows.Next() {
		var project string
		var ticketJSON, attemptJSON []byte
		if err := rows.Scan(&project, &ticketJSON, &attemptJSON); err != nil {
			return HumanInbox{}, err
		}
		var item Item
		if err := json.Unmarshal(ticketJSON, &item); err != nil {
			return HumanInbox{}, err
		}
		var attempt Attempt
		if err := json.Unmarshal(attemptJSON, &attempt); err != nil {
			return HumanInbox{}, err
		}
		detail := item.ExecutionReason
		if attempt.OperationError != "" {
			detail = attempt.OperationError
		}
		if len(out.Entries) == 100 {
			out.More = true
			break
		}
		out.Entries = append(out.Entries, HumanInboxEntry{
			WorkspaceID: item.WorkspaceID, Project: project, TicketID: item.ID, Number: item.Number,
			Title: item.Title, Status: item.ExecutionState, AttemptID: item.ActiveAttemptID, Detail: detail, UpdatedAt: item.UpdatedAt,
			Question: attempt.UserQuestion,
			Revision: item.Revision,
		})
	}
	return out, rows.Err()
}
