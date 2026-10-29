package library

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/snowmerak/q/workitems"
)

type WorkspaceList struct {
	Workspaces []workitems.Workspace `json:"workspaces"`
}

func (c *Client) ListWorkspaces(ctx context.Context, includeArchived bool) (WorkspaceList, error) {
	path := "/workspaces"
	if includeArchived {
		path += "?include_archived=true"
	}
	var result WorkspaceList
	err := c.doJSON(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

func (c *Client) CreateWorkspace(ctx context.Context, input workitems.WorkspaceInput) (workitems.Workspace, error) {
	var result workitems.Workspace
	err := c.doJSON(ctx, http.MethodPost, "/workspaces", input, &result)
	return result, err
}

func (c *Client) UpdateWorkspace(ctx context.Context, id string, input workitems.WorkspaceInput) (workitems.Workspace, error) {
	var result workitems.Workspace
	err := c.doJSON(ctx, http.MethodPut, "/workspaces/"+url.PathEscape(id), input, &result)
	return result, err
}

func (c *Client) ArchiveWorkspace(ctx context.Context, id string) error {
	var result struct {
		Archived bool `json:"archived"`
	}
	return c.doJSON(ctx, http.MethodPost, "/workspaces/"+url.PathEscape(id)+"/archive", struct{}{}, &result)
}

func workspaceError(writer http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, workitems.ErrInvalidInput) {
		status = http.StatusBadRequest
	}
	if errors.Is(err, workitems.ErrNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, workitems.ErrConflict) {
		status = http.StatusConflict
	}
	writeLibraryError(writer, status, err)
}

func registerWorkspaceRoutes(mux *http.ServeMux, catalog *workitems.Store) {
	mux.HandleFunc("GET /v1/workspaces", func(writer http.ResponseWriter, request *http.Request) {
		values, err := catalog.ListWorkspaces(request.Context(), request.URL.Query().Get("include_archived") == "true")
		if err != nil {
			workspaceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, WorkspaceList{Workspaces: values})
	})
	mux.HandleFunc("POST /v1/workspaces", func(writer http.ResponseWriter, request *http.Request) {
		var input workitems.WorkspaceInput
		if err := decodeLibraryJSON(writer, request, &input); err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		value, err := catalog.CreateWorkspace(request.Context(), input)
		if err != nil {
			workspaceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, value)
	})
	mux.HandleFunc("PUT /v1/workspaces/{workspace}", func(writer http.ResponseWriter, request *http.Request) {
		var input workitems.WorkspaceInput
		if err := decodeLibraryJSON(writer, request, &input); err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		value, err := catalog.UpdateWorkspace(request.Context(), request.PathValue("workspace"), input)
		if err != nil {
			workspaceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, value)
	})
	mux.HandleFunc("POST /v1/workspaces/{workspace}/archive", func(writer http.ResponseWriter, request *http.Request) {
		if err := catalog.ArchiveWorkspace(request.Context(), request.PathValue("workspace")); err != nil {
			workspaceError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]bool{"archived": true})
	})
}
