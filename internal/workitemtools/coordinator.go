package workitemtools

import (
	"context"
	"encoding/json"
	"errors"
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
	ID string `json:"id"`
}
type reviewInput struct {
	ID        string `json:"id"`
	Decision  string `json:"decision"`
	Feedback  string `json:"feedback,omitempty"`
	RequestID string `json:"request_id"`
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
	mcp.AddTool(server, &mcp.Tool{Name: "workspace_members", Description: "List the subagents joined to this workspace. Profiles remain in their existing global/workspace stores.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, library.MemberList, error) {
		if b.WorkspaceID == "" {
			return nil, library.MemberList{}, errors.New("no current workspace")
		}
		out, err := c.ListMembers(ctx, b.WorkspaceID)
		return nil, out, err
	})
	for _, action := range []struct{ name, action, description string }{{"ticket_start", "queue", "Resume an inactive ticket for System One assignment. Newly issued tickets are queued automatically and do not require this call. Returns durable admission, not execution completion."}, {"ticket_retry", "retry", "Explicitly retry a blocked/unknown ticket. Saved uncertain tool or ACP outcomes remain blocked until inspected; they are never blindly replayed."}, {"ticket_cancel", "cancel", "Cancel a queued or inactive ticket. Active executions must stop before cancellation."}} {
		mcp.AddTool(server, &mcp.Tool{Name: action.name, Description: action.description}, func(ctx context.Context, _ *mcp.CallToolRequest, in ticketStartInput) (*mcp.CallToolResult, workitems.Item, error) {
			if _, err := requireItem(ctx, in.ID); err != nil {
				return nil, workitems.Item{}, err
			}
			out, err := c.ControlTicket(ctx, in.ID, workitems.TicketAction{Action: action.action, Revision: in.Revision, RequestID: in.RequestID})
			return nil, out, err
		})
	}
	mcp.AddTool(server, &mcp.Tool{Name: "ticket_attempt_list", Description: "Read execution attempts and worker session references for a ticket.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in attemptInput) (*mcp.CallToolResult, attemptListOutput, error) {
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
	mcp.AddTool(server, &mcp.Tool{Name: "ticket_attempt_get", Description: "Read the persisted worker result and pinned change request for an attempt.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in attemptInput) (*mcp.CallToolResult, map[string]any, error) {
		out, err := requireAttempt(ctx, in.ID)
		return nil, attemptOutput(out), err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "ticket_change_read", Description: "Inspect the actual pinned Git change request belonging to this ticket attempt. Normal delegated-child MR ownership remains separate.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in attemptInput) (*mcp.CallToolResult, gitwork.Diff, error) {
		a, err := requireAttempt(ctx, in.ID)
		if err != nil {
			return nil, gitwork.Diff{}, err
		}
		if a.ChangeRequest == nil {
			return nil, gitwork.Diff{}, errors.New("attempt has no Git change request")
		}
		manager := gitwork.Manager{Root: filepath.Dir(filepath.Dir(a.ChangeRequest.WorktreePath))}
		out, err := manager.Read(ctx, *a.ChangeRequest)
		return nil, out, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "ticket_worker_read", Description: "Read a snapshot page of the ticket worker's persisted transcript and unknown-tool markers. Content and arguments are bounded previews; inspect referenced artifacts/files for full evidence.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in itemInput) (*mcp.CallToolResult, library.WorkerPage, error) {
		if _, err := requireAttempt(ctx, in.ID); err != nil {
			return nil, library.WorkerPage{}, err
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 5
		}
		out, err := c.ReadWorker(ctx, in.ID, in.Cursor, min(limit, 10))
		return nil, out, err
	})
	if !b.Manager {
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "manager_inbox", Description: "Read this workspace Integrator's persistent ticket result inbox. Clearing a conversation does not remove inbox entries.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, library.InboxList, error) {
		out, err := c.ListInbox(ctx, b.WorkspaceID)
		return nil, out, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "ticket_review", Description: "Review an attempt as the workspace's reserved Integrator: accept, request_changes (requires feedback), or reject. Git accept durably queues the pinned merge; done is recorded only after Git succeeds."}, func(ctx context.Context, _ *mcp.CallToolRequest, in reviewInput) (*mcp.CallToolResult, map[string]any, error) {
		if _, err := requireAttempt(ctx, in.ID); err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(in.RequestID) == "" {
			return nil, nil, errors.New("request_id is required")
		}
		out, err := c.ReviewAttempt(ctx, in.ID, workitems.ReviewInput{ManagerSessionID: b.SessionID, Decision: in.Decision, Feedback: in.Feedback, RequestID: in.RequestID})
		return nil, attemptOutput(out), err
	})
}
