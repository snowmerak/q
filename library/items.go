package library

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/snowmerak/q/workitems"
)

func (c *Client) CreateItem(ctx context.Context, input workitems.CreateInput) (workitems.Item, error) {
	var output workitems.Item
	err := c.doJSON(ctx, http.MethodPost, "/items", input, &output)
	return output, err
}
func (c *Client) UpdateItem(ctx context.Context, id string, input workitems.UpdateInput) (workitems.Item, error) {
	var output workitems.Item
	err := c.doJSON(ctx, http.MethodPut, "/items/"+url.PathEscape(id), input, &output)
	return output, err
}
func (c *Client) GetItem(ctx context.Context, id string) (workitems.Item, error) {
	var output workitems.Item
	err := c.doJSON(ctx, http.MethodGet, "/items/"+url.PathEscape(id), nil, &output)
	return output, err
}
func (c *Client) ListItems(ctx context.Context, workspaceID, kind, cursor string, limit int) (workitems.ItemPage, error) {
	query := url.Values{"workspace_id": {workspaceID}, "kind": {kind}, "cursor": {cursor}, "limit": {strconv.Itoa(limit)}}
	var output workitems.ItemPage
	err := c.doJSON(ctx, http.MethodGet, "/items?"+query.Encode(), nil, &output)
	return output, err
}
func (c *Client) ReadThread(ctx context.Context, id, cursor string, limit int) (workitems.Thread, error) {
	query := url.Values{"cursor": {cursor}, "limit": {strconv.Itoa(limit)}}
	var output workitems.Thread
	err := c.doJSON(ctx, http.MethodGet, "/items/"+url.PathEscape(id)+"/posts?"+query.Encode(), nil, &output)
	return output, err
}
func (c *Client) AddPost(ctx context.Context, id string, input workitems.PostInput) (workitems.Post, error) {
	var output workitems.Post
	err := c.doJSON(ctx, http.MethodPost, "/items/"+url.PathEscape(id)+"/posts", input, &output)
	return output, err
}
func (c *Client) ReadContent(ctx context.Context, id, cursor string, limit int) (workitems.ContentPage, error) {
	query := url.Values{"cursor": {cursor}, "limit": {strconv.Itoa(limit)}}
	var output workitems.ContentPage
	err := c.doJSON(ctx, http.MethodGet, "/posts/"+url.PathEscape(id)+"?"+query.Encode(), nil, &output)
	return output, err
}
func (c *Client) UpdatePost(ctx context.Context, id string, input workitems.PostInput) (workitems.Post, error) {
	var output workitems.Post
	err := c.doJSON(ctx, http.MethodPut, "/posts/"+url.PathEscape(id), input, &output)
	return output, err
}

func queryLimit(r *http.Request) (int, error) {
	value := r.URL.Query().Get("limit")
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 0 {
		return 0, workitems.ErrInvalidInput
	}
	return limit, nil
}

func registerItemRoutes(mux *http.ServeMux, store *workitems.Store) {
	mux.HandleFunc("GET /v1/items", func(w http.ResponseWriter, r *http.Request) {
		limit, err := queryLimit(r)
		if err != nil {
			workspaceError(w, err)
			return
		}
		out, err := store.ListItems(r.Context(), r.URL.Query().Get("workspace_id"), r.URL.Query().Get("kind"), r.URL.Query().Get("cursor"), limit)
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /v1/items", func(w http.ResponseWriter, r *http.Request) {
		var in workitems.CreateInput
		if err := decodeLibraryJSONWithLimit(w, r, &in, 4<<20); err != nil {
			writeLibraryError(w, 400, err)
			return
		}
		out, err := store.CreateItem(r.Context(), in)
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	})
	mux.HandleFunc("GET /v1/items/{item}", func(w http.ResponseWriter, r *http.Request) {
		out, err := store.GetItem(r.Context(), r.PathValue("item"))
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("PUT /v1/items/{item}", func(w http.ResponseWriter, r *http.Request) {
		var in workitems.UpdateInput
		if err := decodeLibraryJSONWithLimit(w, r, &in, 4<<20); err != nil {
			writeLibraryError(w, 400, err)
			return
		}
		out, err := store.UpdateItem(r.Context(), r.PathValue("item"), in)
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /v1/items/{item}/posts", func(w http.ResponseWriter, r *http.Request) {
		limit, err := queryLimit(r)
		if err != nil {
			workspaceError(w, err)
			return
		}
		out, err := store.ReadThread(r.Context(), r.PathValue("item"), r.URL.Query().Get("cursor"), limit)
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /v1/items/{item}/posts", func(w http.ResponseWriter, r *http.Request) {
		var in workitems.PostInput
		if err := decodeLibraryJSONWithLimit(w, r, &in, 4<<20); err != nil {
			writeLibraryError(w, 400, err)
			return
		}
		out, err := store.AddPost(r.Context(), r.PathValue("item"), in)
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	})
	mux.HandleFunc("GET /v1/posts/{post}", func(w http.ResponseWriter, r *http.Request) {
		limit, err := queryLimit(r)
		if err != nil {
			workspaceError(w, err)
			return
		}
		out, err := store.ReadContent(r.Context(), r.PathValue("post"), r.URL.Query().Get("cursor"), limit)
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("PUT /v1/posts/{post}", func(w http.ResponseWriter, r *http.Request) {
		var in workitems.PostInput
		if err := decodeLibraryJSONWithLimit(w, r, &in, 4<<20); err != nil {
			writeLibraryError(w, 400, err)
			return
		}
		out, err := store.UpdatePost(r.Context(), r.PathValue("post"), in)
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
