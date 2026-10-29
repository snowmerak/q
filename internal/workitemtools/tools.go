// Package workitemtools registers the same Library-backed commands for native
// Q and ACP's stdio bridge. Workspace and actor binding come from the host.
package workitemtools

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
)

type Binding struct {
	WorkspaceID         string
	SessionID           string
	Manager             bool
	EmbeddingConnection string
}
type createInput struct {
	Title             string   `json:"title"`
	Content           string   `json:"content"`
	Labels            []string `json:"labels,omitempty"`
	Acceptance        []string `json:"acceptance,omitempty"`
	ExecutionMode     string   `json:"execution_mode,omitempty" jsonschema:"Always choose explicitly for tickets: report for read-only investigation; git for creating or editing code or project files, including project initialization. Git requires an existing clean repository with an initial commit. Omission defaults to report for compatibility. Ignored for issues."`
	RequestedMemberID string   `json:"requested_member_id,omitempty" jsonschema:"Optional joined workspace member ID. System One still checks suitability; omit to let it choose."`
	LinkedIssueID     string   `json:"linked_issue_id,omitempty"`
	Refs              []string `json:"refs,omitempty"`
	RequestID         string   `json:"request_id,omitempty"`
}
type itemInput struct {
	ID     string `json:"id"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	All    bool   `json:"all,omitempty"`
}

type listInput struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	All    bool   `json:"all,omitempty"`
}
type replyInput struct {
	ID            string   `json:"id"`
	Content       string   `json:"content"`
	Refs          []string `json:"refs,omitempty"`
	ReplyToPostID string   `json:"reply_to_post_id,omitempty"`
	RequestID     string   `json:"request_id,omitempty"`
}
type updateInput struct {
	ID                string    `json:"id"`
	Revision          int64     `json:"revision"`
	Title             string    `json:"title"`
	State             string    `json:"state"`
	Labels            []string  `json:"labels"`
	Deleted           bool      `json:"deleted,omitempty"`
	RequestID         string    `json:"request_id,omitempty"`
	Acceptance        *[]string `json:"acceptance,omitempty"`
	RequestedMemberID *string   `json:"requested_member_id,omitempty"`
	LinkedIssueID     *string   `json:"linked_issue_id,omitempty"`
	ExecutionMode     string    `json:"execution_mode,omitempty"`
}
type postUpdateInput struct {
	ID        string   `json:"id"`
	Revision  int64    `json:"revision"`
	Content   string   `json:"content"`
	Refs      []string `json:"refs,omitempty"`
	Deleted   bool     `json:"deleted,omitempty"`
	RequestID string   `json:"request_id,omitempty"`
}
type searchInput struct {
	Query  string   `json:"query"`
	Mode   string   `json:"mode,omitempty"`
	Kind   string   `json:"kind,omitempty"`
	All    bool     `json:"all,omitempty"`
	Labels []string `json:"labels,omitempty"`
	States []string `json:"states,omitempty"`
	Cursor string   `json:"cursor,omitempty"`
	Limit  int      `json:"limit,omitempty"`
}

func Register(server *mcp.Server, client *library.Client, binding Binding) {
	registerCoordinator(server, client, binding)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	actor := "agent"
	if binding.SessionID != "" {
		actor += "/" + binding.SessionID
	}
	scope := func(workspaceID string, all bool) error {
		if all {
			return nil
		}
		if binding.WorkspaceID == "" || binding.WorkspaceID != workspaceID {
			return errors.New("resource is outside the current workspace; explicit all:true is required for a global read")
		}
		return nil
	}
	requireItem := func(ctx context.Context, id, kind string, all bool) (workitems.Item, error) {
		item, err := client.GetItem(ctx, id)
		if err != nil {
			return item, err
		}
		if kind != "" && item.Kind != kind {
			return item, errors.New("resource kind does not match this tool")
		}
		return item, scope(item.WorkspaceID, all)
	}
	for _, kind := range []string{workitems.Issue, workitems.Ticket} {
		description := "Create a workspace " + kind + " and its independently addressable body. Returns after durable source commit. Use issues/comments to record lessons and decisions, with evidence refs."
		if kind == workitems.Ticket {
			description += " Issuing a ticket automatically queues it for System One assignment to a joined subagent; no separate start call is required. Execution and manager review complete asynchronously."
			description += " Always set execution_mode explicitly: report for read-only research; git for creating or editing project files, including project initialization. Git mode requires a clean existing repository with an initial commit; a missing repository does not make code work a report task."
		}
		mcp.AddTool(server, &mcp.Tool{Name: kind + "_create", Description: description}, func(ctx context.Context, _ *mcp.CallToolRequest, in createInput) (*mcp.CallToolResult, workitems.Item, error) {
			if binding.WorkspaceID == "" {
				return nil, workitems.Item{}, errors.New("select a Studio project before creating work items")
			}
			out, err := client.CreateItem(ctx, workitems.CreateInput{WorkspaceID: binding.WorkspaceID, Kind: kind, Title: in.Title, Content: in.Content, Labels: in.Labels, Acceptance: in.Acceptance, ExecutionMode: in.ExecutionMode, RequestedMemberID: in.RequestedMemberID, LinkedIssueID: in.LinkedIssueID, Author: actor, Refs: in.Refs, RequestID: in.RequestID})
			return nil, out, err
		})
		mcp.AddTool(server, &mcp.Tool{Name: kind + "_list", Description: "List " + kind + " threads using a source snapshot cursor. all:true explicitly searches all workspaces.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, workitems.ItemPage, error) {
			workspaceID := binding.WorkspaceID
			if in.All {
				workspaceID = ""
			} else if workspaceID == "" {
				return nil, workitems.ItemPage{}, errors.New("no current workspace; request all:true explicitly")
			}
			out, err := client.ListItems(ctx, workspaceID, kind, in.Cursor, in.Limit)
			return nil, out, err
		})
		mcp.AddTool(server, &mcp.Tool{Name: kind + "_get", Description: "Read thread metadata and a snapshot page of body/reply previews. Use post_get for complete content by post ID.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in itemInput) (*mcp.CallToolResult, workitems.Thread, error) {
			if _, err := requireItem(ctx, in.ID, kind, in.All); err != nil {
				return nil, workitems.Thread{}, err
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 10
			}
			out, err := client.ReadThread(ctx, in.ID, in.Cursor, min(limit, 10))
			return nil, out, err
		})
		mcp.AddTool(server, &mcp.Tool{Name: kind + "_reply", Description: "Append a durable comment with its own post ID. Include lessons, findings and evidence here instead of requesting automatic learning."}, func(ctx context.Context, _ *mcp.CallToolRequest, in replyInput) (*mcp.CallToolResult, workitems.Post, error) {
			if _, err := requireItem(ctx, in.ID, kind, false); err != nil {
				return nil, workitems.Post{}, err
			}
			out, err := client.AddPost(ctx, in.ID, workitems.PostInput{Content: in.Content, Author: actor, Refs: in.Refs, ReplyToPostID: in.ReplyToPostID, RequestID: in.RequestID})
			return nil, out, err
		})
		mcp.AddTool(server, &mcp.Tool{Name: kind + "_update", Description: "Edit thread metadata with the expected revision. Ticket column changes use execution/review tools."}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateInput) (*mcp.CallToolResult, workitems.Item, error) {
			item, err := requireItem(ctx, in.ID, kind, false)
			if err != nil {
				return nil, item, err
			}
			if in.Acceptance != nil {
				item.Acceptance = *in.Acceptance
			}
			if in.RequestedMemberID != nil {
				item.RequestedMemberID = *in.RequestedMemberID
			}
			if in.LinkedIssueID != nil {
				item.LinkedIssueID = *in.LinkedIssueID
			}
			out, err := client.UpdateItem(ctx, in.ID, workitems.UpdateInput{Revision: in.Revision, Title: in.Title, State: in.State, Labels: in.Labels, Deleted: in.Deleted, Acceptance: item.Acceptance, RequestedMemberID: item.RequestedMemberID, LinkedIssueID: item.LinkedIssueID, ExecutionMode: in.ExecutionMode, RequestID: in.RequestID})
			return nil, out, err
		})
	}
	mcp.AddTool(server, &mcp.Tool{Name: "post_get", Description: "Read a body or reply by post ID. Content pages pin the revision, so concurrent edits do not splice versions.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in itemInput) (*mcp.CallToolResult, workitems.ContentPage, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 4096
		}
		out, err := client.ReadContent(ctx, in.ID, in.Cursor, min(limit, 4096))
		if err != nil {
			return nil, out, err
		}
		if err := scope(out.Post.WorkspaceID, in.All); err != nil {
			return nil, workitems.ContentPage{}, err
		}
		return nil, out, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "post_update", Description: "Edit or tombstone a body/reply using its expected revision. Historical content remains revision-addressable."}, func(ctx context.Context, _ *mcp.CallToolRequest, in postUpdateInput) (*mcp.CallToolResult, workitems.Post, error) {
		current, err := client.ReadContent(ctx, in.ID, "", 1)
		if err != nil {
			return nil, workitems.Post{}, err
		}
		if err := scope(current.Post.WorkspaceID, false); err != nil {
			return nil, workitems.Post{}, err
		}
		out, err := client.UpdatePost(ctx, in.ID, workitems.PostInput{Content: in.Content, Author: actor, Refs: in.Refs, Deleted: in.Deleted, Revision: in.Revision, RequestID: in.RequestID})
		return nil, out, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "search_work", Description: "Search issue and ticket bodies/replies using lexical, semantic or hybrid retrieval. Defaults to this workspace; all:true explicitly searches across projects. Results contain source IDs/revisions and content offsets.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, library.WorkSearchResponse, error) {
		out, err := client.SearchWork(ctx, library.WorkSearchRequest{WorkspaceID: binding.WorkspaceID, All: in.All, Query: in.Query, Mode: in.Mode, Kind: in.Kind, States: in.States, Labels: in.Labels, Cursor: in.Cursor, Limit: in.Limit})
		return nil, out, err
	})
}
