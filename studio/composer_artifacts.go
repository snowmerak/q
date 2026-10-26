package studio

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/snowmerak/q/composer"
)

func (s *composerService) artifactStore(id string) (*composer.Artifacts, error) {
	c, err := s.store.Load(id)
	if err != nil {
		return nil, err
	}
	return &composer.Artifacts{Root: s.store.SessionStore(c).SessionDir()}, nil
}

func (s *composerService) serveArtifacts(w http.ResponseWriter, r *http.Request) {
	files, err := s.artifactStore(r.PathValue("composer"))
	if err != nil {
		writeComposerError(w, err)
		return
	}
	values, err := files.List()
	if err != nil {
		writeComposerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": values})
}

func (s *composerService) serveArtifactSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeAPIError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if r.Method != http.MethodGet {
		lock, err := s.store.Lock(r.PathValue("composer"))
		if err != nil {
			writeComposerError(w, err)
			return
		}
		defer func() { _ = lock.Close() }()
	}
	files, err := s.artifactStore(r.PathValue("composer"))
	if err != nil {
		writeComposerError(w, err)
		return
	}
	name := r.URL.Query().Get("path")
	if r.Method == http.MethodGet {
		value, err := files.Read(name)
		if err != nil {
			writeComposerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
		return
	}
	var input struct {
		Content      string  `json:"content"`
		ExpectedHash *string `json:"expected_hash"`
	}
	// Artifact sources have a larger body limit than ordinary settings requests.
	if err := decodeRequest(w, r, &input, composer.MaximumArtifactSize*6+1024, "decode artifact"); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if input.ExpectedHash == nil {
		writeAPIError(w, http.StatusUnprocessableEntity, errors.New("expected_hash is required; use an empty hash to create a file"))
		return
	}
	if r.Method == http.MethodDelete {
		if err := files.Delete(name, input.ExpectedHash); err != nil {
			writeComposerError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	value, err := files.Write(name, input.Content, input.ExpectedHash)
	if err != nil {
		writeComposerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

// Render at most two diagrams concurrently, bounding expensive compiler work.
var composerRenderSlots = make(chan struct{}, 2)

//go:embed preview/d2.html
var composerD2Preview string

func (s *composerService) serveArtifactPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; form-action 'none'; frame-ancestors 'self'; sandbox allow-scripts")
	fail := func(err error) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = fmt.Fprintf(w, "<!doctype html><html><body><h2>Preview unavailable</h2><pre role=\"alert\" style=\"white-space:pre-wrap\">%s</pre></body></html>", html.EscapeString(err.Error()))
	}
	files, err := s.artifactStore(r.PathValue("composer"))
	if err != nil {
		fail(err)
		return
	}
	value, err := files.Read(r.URL.Query().Get("path"))
	if err != nil {
		fail(err)
		return
	}
	if value.Kind == "html" {
		_, _ = w.Write([]byte(value.Content))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	select {
	case composerRenderSlots <- struct{}{}:
		defer func() { <-composerRenderSlots }()
	case <-ctx.Done():
		fail(ctx.Err())
		return
	}
	svg, err := files.RenderD2(ctx, value)
	if err != nil {
		fail(err)
		return
	}
	// SVG stays in an opaque-origin document, never in Studio's application DOM.
	_, _ = w.Write([]byte(strings.Replace(composerD2Preview, "<!-- DIAGRAM -->", strings.TrimSpace(string(svg)), 1)))
}
