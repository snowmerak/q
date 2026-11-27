package studio

import (
	"context"
	"errors"
	"net/http"

	"github.com/snowmerak/q/internal/workmedia"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
)

func (s *sessionsService) serveWorkAssetUpload(w http.ResponseWriter, r *http.Request) {
	var input workmedia.Image
	if err := decodeRequest(w, r, &input, workmedia.MaxImageJSON, "decode image"); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	workResponse(s, w, r, http.StatusCreated, func(ctx context.Context, c *library.Client) (workitems.Asset, error) {
		return c.PutWorkAsset(ctx, r.PathValue("project"), library.AssetUpload{Data: input.Data, ImagesOnly: true})
	})
}

func (s *sessionsService) serveWorkAsset(w http.ResponseWriter, r *http.Request) {
	asset, err := withProjectCatalog(s.projects, func(_ context.Context, c *library.Client) (workitems.Asset, error) {
		return c.GetWorkAsset(r.Context(), r.PathValue("project"), r.PathValue("asset"))
	})
	if err != nil {
		var response *library.ResponseError
		if errors.As(err, &response) {
			writeAPIError(w, response.StatusCode, err)
		} else {
			writeAPIError(w, http.StatusInternalServerError, err)
		}
		return
	}
	w.Header().Set("Content-Type", asset.MIME)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(asset.Data)
}
