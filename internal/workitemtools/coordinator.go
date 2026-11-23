package workitemtools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/gitwork"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
)

type ticketStartInput struct {
	ID        string `json:"id"`
	Revision  int64  `json:"revision"`
	RequestID string `json:"request_id"`
}
type attemptInput struct {
	ID string `json:"id" jsonschema:"Exact execution attempt ID from the current review notification or ticket_attempt_list."`
}
type ticketInput struct {
	ID string `json:"id" jsonschema:"Ticket ID."`
}
type reviewInput struct {
	ID        string `json:"id" jsonschema:"Exact active execution attempt ID listed in the current review notification."`
	Decision  string `json:"decision"`
	Feedback  string `json:"feedback,omitempty"`
	RequestID string `json:"request_id" jsonschema:"A stable key you choose for this review decision. Reuse it when retrying the same decision."`
}

type assignedReviewInput struct {
	Decision string `json:"decision" jsonschema:"Choose accept, request_changes, or reject."`
	Feedback string `json:"feedback,omitempty" jsonschema:"Actionable feedback is required when requesting changes."`
}

type workerPageInput struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type attemptListOutput struct {
	Attempts []map[string]any `json:"attempts"`
}

// RawMessage is a JSON object on the wire, while schema reflection sees a byte
// slice. Decode it before MCP output validation and omit reservation credentials.
func attemptOutput(a workitems.Attempt) map[string]any {
	a.Owner, a.Token = "", ""
	raw, _ := json.Marshal(a)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	delete(out, "owner")
	delete(out, "token")
	return out
}

func registerCoordinator(server *mcp.Server, c *library.Client, b Binding) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	requireItem := func(ctx context.Context, id string) (workitems.Item, error) {
		item, err := c.GetItem(ctx, id)
		if err != nil {
			return item, err
		}
		if b.WorkspaceID == "" || item.WorkspaceID != b.WorkspaceID || item.Kind != workitems.Ticket {
			return workitems.Item{}, errors.New("ticket is outside this workspace")
		}
		return item, nil
	}
	requireAttempt := func(ctx context.Context, id string) (workitems.Attempt, error) {
		a, err := c.GetAttempt(ctx, id)
		if err != nil {
			return a, err
		}
		if b.WorkspaceID == "" || a.WorkspaceID != b.WorkspaceID {
			return workitems.Attempt{}, errors.New("attempt is outside this workspace")
		}
		return a, nil
	}
	mcp.AddTool(server, &mcp.Tool{Name: "workspace_members", Description: "List joined subagents as candidates for ticket assignment. Use ticket_create to request work; direct subagent execution is not supported. Profiles remain in their existing global/workspace stores.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, library.MemberList, error) {
		if b.WorkspaceID == "" {
			return nil, library.MemberList{}, errors.New("no current workspace")
		}
		out, err := c.ListMembers(ctx, b.WorkspaceID)
		return nil, out, err
	})
	for _, action := range []struct{ name, action, description string }{{"ticket_start", "queue", "Resume an inactive ticket for Decisions assignment. Newly issued tickets are queued automatically and do not require this call. Returns durable admission, not execution completion."}, {"ticket_retry", "retry", "Explicitly retry a blocked/unknown ticket. Saved uncertain tool or ACP outcomes remain blocked until inspected; they are never blindly replayed."}, {"ticket_cancel", "cancel", "Request cancellation of an unfinished ticket. Active executions stop first; pending Git changes close before cancellation completes. Cancelled results cannot be accepted."}} {
		mcp.AddTool(server, &mcp.Tool{Name: action.name, Description: action.description}, func(ctx context.Context, _ *mcp.CallToolRequest, in ticketStartInput) (*mcp.CallToolResult, workitems.Item, error) {
			item, err := requireItem(ctx, in.ID)
			if err != nil {
				return nil, workitems.Item{}, err
			}
			if err := b.caller(ctx).requireItemOwner(ctx, c, item); err != nil {
				return nil, workitems.Item{}, err
			}
			out, err := c.ControlTicket(ctx, in.ID, workitems.TicketAction{Action: action.action, Revision: in.Revision, RequestID: in.RequestID})
			return nil, out, err
		})
	}
	mcp.AddTool(server, &mcp.Tool{Name: "ticket_attempt_list", Description: "Read execution attempts and worker session references for a ticket.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in ticketInput) (*mcp.CallToolResult, attemptListOutput, error) {
		if _, err := requireItem(ctx, in.ID); err != nil {
			return nil, attemptListOutput{}, err
		}
		out, err := c.ListAttempts(ctx, in.ID)
		view := attemptListOutput{Attempts: []map[string]any{}}
		for _, a := range out.Attempts {
			view.Attempts = append(view.Attempts, attemptOutput(a))
		}
		return nil, view, err
	})
	readAttempt := func(ctx context.Context, id string) (*mcp.CallToolResult, map[string]any, error) {
		out, err := requireAttempt(ctx, id)
		return nil, attemptOutput(out), err
	}
	readChange := func(ctx context.Context, id string) (*mcp.CallToolResult, gitwork.Diff, error) {
		a, err := requireAttempt(ctx, id)
		if err != nil {
			return nil, gitwork.Diff{}, err
		}
		if a.ChangeRequest == nil {
			return nil, gitwork.Diff{}, errors.New("attempt has no Git change request")
		}
		manager := gitwork.Manager{Root: filepath.Dir(filepath.Dir(a.ChangeRequest.WorktreePath))}
		out, err := manager.Read(ctx, *a.ChangeRequest)
		return nil, out, err
	}
	readWorker := func(ctx context.Context, id, cursor string, limit int) (*mcp.CallToolResult, library.WorkerPage, error) {
		if _, err := requireAttempt(ctx, id); err != nil {
			return nil, library.WorkerPage{}, err
		}
		if limit <= 0 {
			limit = 5
		}
		out, err := c.ReadWorker(ctx, id, cursor, min(limit, 10))
		return nil, out, err
	}
	if b.Review.Active() {
		mcp.AddTool(server, &mcp.Tool{Name: "ticket_attempt_get", Description: "Read the result and acceptance criteria of the assigned review.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, map[string]any, error) {
			return readAttempt(ctx, b.Review.AttemptID)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "ticket_change_read", Description: "Read the pinned Git diff of the assigned review.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, gitwork.Diff, error) {
			return readChange(ctx, b.Review.AttemptID)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "ticket_worker_read", Description: "Read a page of the assigned worker result transcript.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in workerPageInput) (*mcp.CallToolResult, library.WorkerPage, error) {
			return readWorker(ctx, b.Review.AttemptID, in.Cursor, in.Limit)
		})
	} else {
		mcp.AddTool(server, &mcp.Tool{Name: "ticket_attempt_get", Description: "Read the persisted worker result and pinned change request for an attempt.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in attemptInput) (*mcp.CallToolResult, map[string]any, error) {
			return readAttempt(ctx, in.ID)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "ticket_change_read", Description: "Inspect the pinned Git change request for this ticket attempt.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in attemptInput) (*mcp.CallToolResult, gitwork.Diff, error) {
			return readChange(ctx, in.ID)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "ticket_worker_read", Description: "Read a page of the ticket worker's persisted transcript.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in itemInput) (*mcp.CallToolResult, library.WorkerPage, error) {
			return readWorker(ctx, in.ID, in.Cursor, in.Limit)
		})
	}
	if b.Manager {
		mcp.AddTool(server, &mcp.Tool{Name: "manager_inbox", Description: "Read this workspace Integrator's persistent ticket result inbox. Clearing a conversation does not remove inbox entries.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, library.InboxList, error) {
			out, err := c.ListInbox(ctx, b.WorkspaceID)
			return nil, out, err
		})
	}
	review := func(ctx context.Context, id, requestID, decision, feedback string) (*mcp.CallToolResult, map[string]any, error) {
		if DelegatedSession(ctx) != "" {
			return nil, nil, errors.New("this review belongs to the parent session")
		}
		if _, err := requireAttempt(ctx, id); err != nil {
			return nil, nil, err
		}
		out, err := c.ReviewAttempt(ctx, id, workitems.ReviewInput{Reviewer: b.Worker, ManagerSessionID: b.SessionID, Decision: decision, Feedback: feedback, RequestID: requestID})
		return nil, attemptOutput(out), err
	}
	if b.Review.Active() && (b.Manager || b.Worker != nil) {
		key := sha256.Sum256([]byte(b.Review.AttemptID + "\x00" + b.Review.OperationID))
		mcp.AddTool(server, &mcp.Tool{Name: "ticket_review", Description: "Record the decision for the assigned review. Accept queues the pinned merge for Git work."}, func(ctx context.Context, _ *mcp.CallToolRequest, in assignedReviewInput) (*mcp.CallToolResult, map[string]any, error) {
			return review(ctx, b.Review.AttemptID, fmt.Sprintf("assigned-review-%x", key), in.Decision, in.Feedback)
		})
		return
	}
	if !b.Manager {
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "ticket_review", Description: "Review a selected attempt: accept, request_changes (requires feedback), or reject. Git accept queues the reviewed merge; done is recorded after Git succeeds."}, func(ctx context.Context, _ *mcp.CallToolRequest, in reviewInput) (*mcp.CallToolResult, map[string]any, error) {
		if strings.TrimSpace(in.RequestID) == "" || len(in.RequestID) > 256 {
			return nil, nil, errors.New("request_id must contain 1 to 256 bytes")
		}
		// Workers can reuse the ticket's creation key in a review. Limit the
		// model-provided key to this review operation and attempt.
		key := sha256.Sum256([]byte(in.ID + "\x00" + in.RequestID))
		return review(ctx, in.ID, fmt.Sprintf("ticket-review-%x", key), in.Decision, in.Feedback)
	})
}
