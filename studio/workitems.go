package studio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
)

func workResponse[T any](service *sessionsService, w http.ResponseWriter, r *http.Request, status int, action func(context.Context, *library.Client) (T, error)) {
	result, err := withProjectCatalog(service.projects, func(_ context.Context, c *library.Client) (T, error) { return action(r.Context(), c) })
	if err != nil {
		code := http.StatusInternalServerError
		var response *library.ResponseError
		if errors.As(err, &response) {
			code = response.StatusCode
		}
		writeAPIError(w, code, err)
		return
	}
	writeJSON(w, status, result)
}

func (s *sessionsService) serveWorkItems(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (workitems.ItemPage, error) {
			return c.ListItems(ctx, r.URL.Query().Get("workspace_id"), r.URL.Query().Get("kind"), r.URL.Query().Get("cursor"), limit)
		})
		return
	}
	var in workitems.CreateInput
	if err := decodeWorkRequest(w, r, &in); err != nil {
		writeAPIError(w, 400, err)
		return
	}
	in.Author = "user"
	workResponse(s, w, r, 201, func(ctx context.Context, c *library.Client) (workitems.Item, error) { return c.CreateItem(ctx, in) })
}
func (s *sessionsService) serveWorkItem(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (workitems.Item, error) {
			return c.GetItem(ctx, r.PathValue("item"))
		})
		return
	}
	var in workitems.UpdateInput
	if err := decodeWorkRequest(w, r, &in); err != nil {
		writeAPIError(w, 400, err)
		return
	}
	workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (workitems.Item, error) {
		return c.UpdateItem(ctx, r.PathValue("item"), in)
	})
}
func (s *sessionsService) serveWorkPosts(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (workitems.Thread, error) {
			return c.ReadThread(ctx, r.PathValue("item"), r.URL.Query().Get("cursor"), limit)
		})
		return
	}
	var in workitems.PostInput
	if err := decodeWorkRequest(w, r, &in); err != nil {
		writeAPIError(w, 400, err)
		return
	}
	in.Author = "user"
	workResponse(s, w, r, 201, func(ctx context.Context, c *library.Client) (workitems.Post, error) {
		return c.AddPost(ctx, r.PathValue("item"), in)
	})
}
func (s *sessionsService) serveWorkPost(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (workitems.ContentPage, error) {
			return c.ReadContent(ctx, r.PathValue("post"), r.URL.Query().Get("cursor"), limit)
		})
		return
	}
	var in workitems.PostInput
	if err := decodeWorkRequest(w, r, &in); err != nil {
		writeAPIError(w, 400, err)
		return
	}
	in.Author = "user"
	workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (workitems.Post, error) {
		return c.UpdatePost(ctx, r.PathValue("post"), in)
	})
}
func (s *sessionsService) serveWorkSearch(w http.ResponseWriter, r *http.Request) {
	var in library.WorkSearchRequest
	if err := decodeWorkRequest(w, r, &in); err != nil {
		writeAPIError(w, 400, err)
		return
	}
	if runner, ok := s.runner.(interface {
		SearchWork(context.Context, library.WorkSearchRequest) (library.WorkSearchResponse, error)
	}); ok {
		out, err := runner.SearchWork(r.Context(), in)
		if err != nil {
			writeAPIError(w, 503, err)
			return
		}
		writeJSON(w, 200, out)
		return
	}
	workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (library.WorkSearchResponse, error) {
		return c.SearchWork(ctx, in)
	})
}

func decodeWorkRequest(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}
