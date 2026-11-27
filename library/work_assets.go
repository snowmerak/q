package library

import (
	"context"
	"net/http"
	"net/url"

	"github.com/snowmerak/q/internal/workmedia"
	"github.com/snowmerak/q/workitems"
)

type AssetUpload struct {
	Data       []byte `json:"data"`
	ImagesOnly bool   `json:"images_only,omitempty"`
}

func (c *Client) PutWorkAsset(ctx context.Context, workspace string, input AssetUpload) (workitems.Asset, error) {
	var asset workitems.Asset
	err := c.doJSON(ctx, http.MethodPost, "/workspaces/"+url.PathEscape(workspace)+"/assets", input, &asset)
	return asset, err
}

func (c *Client) GetWorkAsset(ctx context.Context, workspace, id string) (workitems.Asset, error) {
	var asset workitems.Asset
	err := c.doJSON(ctx, http.MethodGet, "/workspaces/"+url.PathEscape(workspace)+"/assets/"+url.PathEscape(id), nil, &asset)
	return asset, err
}

func registerWorkAssetRoutes(mux *http.ServeMux, store workitems.AssetRepository) {
	mux.HandleFunc("POST /v1/workspaces/{workspace}/assets", func(w http.ResponseWriter, r *http.Request) {
		var input AssetUpload
		if err := decodeLibraryJSONWithLimit(w, r, &input, workmedia.MaxImageJSON); err != nil {
			writeLibraryError(w, http.StatusBadRequest, err)
			return
		}
		asset, err := store.PutAsset(r.Context(), r.PathValue("workspace"), input.Data, input.ImagesOnly)
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, asset)
	})
	mux.HandleFunc("GET /v1/workspaces/{workspace}/assets/{asset}", func(w http.ResponseWriter, r *http.Request) {
		asset, err := store.GetAsset(r.Context(), r.PathValue("workspace"), r.PathValue("asset"))
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, asset)
	})
}
