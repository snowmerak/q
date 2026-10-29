package library

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/snowmerak/q/workitems"
)

type MemberList struct {
	Members []workitems.Member `json:"members"`
}
type AttemptList struct {
	Attempts []workitems.Attempt `json:"attempts"`
}
type InboxList struct {
	Entries []workitems.InboxEntry `json:"entries"`
}

func (c *Client) ListTicketQueue(ctx context.Context, workspaceID, queue, cursor string, limit int) (workitems.ItemPage, error) {
	var out workitems.ItemPage
	query := url.Values{"queue": {queue}, "cursor": {cursor}, "limit": {strconv.Itoa(limit)}}
	err := c.doJSON(ctx, http.MethodGet, "/workspaces/"+url.PathEscape(workspaceID)+"/ticket-queue?"+query.Encode(), nil, &out)
	return out, err
}
func (c *Client) GetTicketLoad(ctx context.Context, workspaceID string) (workitems.TicketLoad, error) {
	var out workitems.TicketLoad
	err := c.doJSON(ctx, http.MethodGet, "/workspaces/"+url.PathEscape(workspaceID)+"/ticket-load", nil, &out)
	return out, err
}
func (c *Client) NextPendingInbox(ctx context.Context, workspaceID string) (InboxList, error) {
	var out InboxList
	err := c.doJSON(ctx, http.MethodGet, "/workspaces/"+url.PathEscape(workspaceID)+"/inbox?pending=true", nil, &out)
	return out, err
}

func (c *Client) ListMembers(ctx context.Context, id string) (MemberList, error) {
	var out MemberList
	err := c.doJSON(ctx, http.MethodGet, "/workspaces/"+url.PathEscape(id)+"/members", nil, &out)
	return out, err
}
func (c *Client) PutMember(ctx context.Context, id string, in workitems.MemberInput) (workitems.Member, error) {
	var out workitems.Member
	err := c.doJSON(ctx, http.MethodPost, "/workspaces/"+url.PathEscape(id)+"/members", in, &out)
	return out, err
}
func (c *Client) ControlTicket(ctx context.Context, id string, in workitems.TicketAction) (workitems.Item, error) {
	var out workitems.Item
	err := c.doJSON(ctx, http.MethodPost, "/items/"+url.PathEscape(id)+"/control", in, &out)
	return out, err
}
func (c *Client) ClaimTicket(ctx context.Context, id string, in workitems.ClaimInput) (workitems.Attempt, error) {
	var out workitems.Attempt
	err := c.doJSON(ctx, http.MethodPost, "/items/"+url.PathEscape(id)+"/claim", in, &out)
	return out, err
}
func (c *Client) ListAttempts(ctx context.Context, id string) (AttemptList, error) {
	var out AttemptList
	err := c.doJSON(ctx, http.MethodGet, "/items/"+url.PathEscape(id)+"/attempts", nil, &out)
	return out, err
}
func (c *Client) GetAttempt(ctx context.Context, id string) (workitems.Attempt, error) {
	var out workitems.Attempt
	err := c.doJSON(ctx, http.MethodGet, "/attempts/"+url.PathEscape(id), nil, &out)
	return out, err
}
func (c *Client) UpdateAttempt(ctx context.Context, id string, in workitems.AttemptUpdate) (workitems.Attempt, error) {
	var out workitems.Attempt
	err := c.doJSON(ctx, http.MethodPost, "/attempts/"+url.PathEscape(id), in, &out)
	return out, err
}
func (c *Client) ReviewAttempt(ctx context.Context, id string, in workitems.ReviewInput) (workitems.Attempt, error) {
	var out workitems.Attempt
	err := c.doJSON(ctx, http.MethodPost, "/attempts/"+url.PathEscape(id)+"/review", in, &out)
	return out, err
}
func (c *Client) ClaimMerge(ctx context.Context, id, owner string) (workitems.Attempt, error) {
	var out workitems.Attempt
	err := c.doJSON(ctx, http.MethodPost, "/attempts/"+url.PathEscape(id)+"/merge", map[string]string{"owner": owner}, &out)
	return out, err
}
func (c *Client) ListInbox(ctx context.Context, id string) (InboxList, error) {
	var out InboxList
	err := c.doJSON(ctx, http.MethodGet, "/workspaces/"+url.PathEscape(id)+"/inbox", nil, &out)
	return out, err
}
func (c *Client) AcknowledgeInbox(ctx context.Context, id, managerID string) error {
	var out any
	return c.doJSON(ctx, http.MethodPost, "/inbox/"+url.PathEscape(id)+"/ack", map[string]string{"manager_session_id": managerID}, &out)
}

func (c *Client) DeferInbox(ctx context.Context, id, managerID, reason string) error {
	var out any
	return c.doJSON(ctx, http.MethodPost, "/inbox/"+url.PathEscape(id)+"/defer", map[string]string{"manager_session_id": managerID, "reason": reason}, &out)
}
func (c *Client) RecoverAttempts(ctx context.Context) error {
	var out any
	return c.doJSON(ctx, http.MethodPost, "/attempts/recover", struct{}{}, &out)
}

func coordinatorWrite[I, O any](mux *http.ServeMux, route string, action func(context.Context, *http.Request, I) (O, error)) {
	mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
		var in I
		if err := decodeLibraryJSONWithLimit(w, r, &in, 4<<20); err != nil {
			writeLibraryError(w, 400, err)
			return
		}
		out, err := action(r.Context(), r, in)
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
}

func registerCoordinatorRoutes(mux *http.ServeMux, s *workitems.Store) {
	mux.HandleFunc("GET /v1/workspaces/{workspace}/ticket-queue", func(w http.ResponseWriter, r *http.Request) {
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil && r.URL.Query().Get("limit") != "" {
			workspaceError(w, workitems.ErrInvalidInput)
			return
		}
		out, err := s.ListTicketQueue(r.Context(), r.PathValue("workspace"), r.URL.Query().Get("queue"), r.URL.Query().Get("cursor"), limit)
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /v1/workspaces/{workspace}/ticket-load", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.GetTicketLoad(r.Context(), r.PathValue("workspace"))
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, 200, out)
	})

	coordinatorWrite(mux, "POST /v1/inbox/{entry}/defer", func(ctx context.Context, r *http.Request, in struct {
		ManagerSessionID string `json:"manager_session_id"`
		Reason           string `json:"reason"`
	}) (map[string]bool, error) {
		err := s.DeferInbox(ctx, r.PathValue("entry"), in.ManagerSessionID, in.Reason)
		return map[string]bool{"deferred": err == nil}, err
	})
	mux.HandleFunc("GET /v1/workspaces/{workspace}/members", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.ListMembers(r.Context(), r.PathValue("workspace"))
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, 200, MemberList{Members: out})
	})
	coordinatorWrite(mux, "POST /v1/workspaces/{workspace}/members", func(ctx context.Context, r *http.Request, in workitems.MemberInput) (workitems.Member, error) {
		return s.PutMember(ctx, r.PathValue("workspace"), in)
	})
	coordinatorWrite(mux, "POST /v1/items/{item}/control", func(ctx context.Context, r *http.Request, in workitems.TicketAction) (workitems.Item, error) {
		return s.ControlTicket(ctx, r.PathValue("item"), in)
	})
	coordinatorWrite(mux, "POST /v1/items/{item}/claim", func(ctx context.Context, r *http.Request, in workitems.ClaimInput) (workitems.Attempt, error) {
		return s.ClaimTicket(ctx, r.PathValue("item"), in)
	})
	mux.HandleFunc("GET /v1/items/{item}/attempts", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.ListAttempts(r.Context(), r.PathValue("item"))
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, 200, AttemptList{Attempts: out})
	})
	mux.HandleFunc("GET /v1/attempts/{attempt}", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.GetAttempt(r.Context(), r.PathValue("attempt"))
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	coordinatorWrite(mux, "POST /v1/attempts/{attempt}", func(ctx context.Context, r *http.Request, in workitems.AttemptUpdate) (workitems.Attempt, error) {
		return s.UpdateAttempt(ctx, r.PathValue("attempt"), in)
	})
	coordinatorWrite(mux, "POST /v1/attempts/{attempt}/review", func(ctx context.Context, r *http.Request, in workitems.ReviewInput) (workitems.Attempt, error) {
		return s.ReviewAttempt(ctx, r.PathValue("attempt"), in)
	})
	coordinatorWrite(mux, "POST /v1/attempts/{attempt}/merge", func(ctx context.Context, r *http.Request, in struct {
		Owner string `json:"owner"`
	}) (workitems.Attempt, error) {
		return s.ClaimMerge(ctx, r.PathValue("attempt"), in.Owner)
	})
	coordinatorWrite(mux, "POST /v1/attempts/recover", func(ctx context.Context, _ *http.Request, _ struct{}) (map[string]bool, error) {
		err := s.RecoverExpired(ctx)
		return map[string]bool{"reconciled": err == nil}, err
	})
	mux.HandleFunc("GET /v1/workspaces/{workspace}/inbox", func(w http.ResponseWriter, r *http.Request) {
		var out []workitems.InboxEntry
		var err error
		if r.URL.Query().Get("pending") == "true" {
			out, err = s.NextPendingInbox(r.Context(), r.PathValue("workspace"))
		} else {
			out, err = s.ListInbox(r.Context(), r.PathValue("workspace"))
		}
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, 200, InboxList{Entries: out})
	})
	coordinatorWrite(mux, "POST /v1/inbox/{entry}/ack", func(ctx context.Context, r *http.Request, in struct {
		ManagerSessionID string `json:"manager_session_id"`
	}) (map[string]bool, error) {
		err := s.AcknowledgeInbox(ctx, r.PathValue("entry"), in.ManagerSessionID)
		return map[string]bool{"acknowledged": err == nil}, err
	})
}
