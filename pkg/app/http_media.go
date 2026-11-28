package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
)

const (
	defaultArtifactReadLimit int64 = 64 << 10
	maxArtifactReadLimit     int64 = 1 << 20
)

func (a *App) handleUploadMedia(w http.ResponseWriter, r *http.Request) {
	cfg := a.currentConfig()
	maxBytes := cfg.Storage.Blob.MaxUploadBytes
	if maxBytes <= 0 {
		maxBytes = 20 << 20
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
	if err := r.ParseMultipartForm(min(maxBytes, 8<<20)); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", "invalid multipart upload: "+err.Error(), false, nil)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", "form field \"file\" is required", false, nil)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", "failed to read uploaded file", false, nil)
		return
	}
	if int64(len(data)) > maxBytes {
		writeError(w, http.StatusBadRequest, "invalid_input", "uploaded file exceeds max_upload_bytes", false, nil)
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_input", "uploaded file is empty", false, nil)
		return
	}

	mediaType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if mediaType == "" || mediaType == "application/octet-stream" {
		mediaType = http.DetectContentType(data[:min(512, len(data))])
	}

	id := domain.NewID()
	blobKey := "media/" + id
	size, shaHex, err := a.blob.Put(r.Context(), blobKey, bytes.NewReader(data), mediaType)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	metaJSON, _ := json.Marshal(map[string]any{"filename": header.Filename})
	art := domain.Artifact{
		ID:             id,
		Kind:           domain.ArtifactKindMedia,
		MediaType:      mediaType,
		ByteSize:       size,
		SHA256:         shaHex,
		BlobKey:        blobKey,
		SourceMetaJSON: string(metaJSON),
		CreatedAt:      domain.NowMS(),
		LastAccessedAt: domain.NowMS(),
	}
	if err := a.rdb.Artifacts().Create(r.Context(), art); err != nil {
		_ = a.blob.Delete(r.Context(), blobKey)
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"artifact_id": art.ID,
		"url":         "/api/v1/media/" + art.ID,
	})
}

func (a *App) handleGetMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	art, err := a.rdb.Artifacts().Get(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	rc, err := a.blob.Open(r.Context(), art.BlobKey)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	defer rc.Close()

	_ = a.rdb.Artifacts().Touch(r.Context(), art.ID, domain.NowMS())
	if art.MediaType != "" {
		w.Header().Set("Content-Type", art.MediaType)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func (a *App) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	art, err := a.rdb.Artifacts().Get(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var offset int64
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			writeError(w, http.StatusBadRequest, "invalid_input", "invalid offset", false, nil)
			return
		}
		offset = v
	}
	limit := defaultArtifactReadLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_input", "invalid limit", false, nil)
			return
		}
		if v > maxArtifactReadLimit {
			v = maxArtifactReadLimit
		}
		limit = v
	}

	rc, err := a.blob.Open(r.Context(), art.BlobKey)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	defer rc.Close()

	if offset > 0 {
		if _, err := io.CopyN(io.Discard, rc, offset); err != nil && err != io.EOF {
			writeDomainError(w, err)
			return
		}
	}
	buf, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	truncated := int64(len(buf)) > limit
	if truncated {
		buf = buf[:limit]
	}
	_ = a.rdb.Artifacts().Touch(r.Context(), art.ID, domain.NowMS())

	writeJSON(w, http.StatusOK, map[string]any{
		"artifact":  art,
		"content":   string(buf),
		"truncated": truncated,
	})
}

func (a *App) handleReindex(w http.ResponseWriter, _ *http.Request) {
	if !a.reindexRunning.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "session_busy", "reindex job is already running", true, nil)
		return
	}
	jobID := domain.NewID()
	a.reindexWG.Add(1)
	go func() {
		defer a.reindexWG.Done()
		defer a.reindexRunning.Store(false)

		ctx := context.Background()
		if err := a.runReindex(ctx); err != nil {
			a.logger.Warn("storage reindex failed", "job_id", jobID, "error", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id": jobID,
	})
}

func (a *App) runReindex(ctx context.Context) error {
	docsCh := make(chan storage.SearchDocument, 64)
	errCh := make(chan error, 1)

	go func() {
		errCh <- a.search.ResetAndRebuild(ctx, docsCh)
	}()

	emitErr := func() error {
		defer close(docsCh)

		rooms, err := a.rdb.Rooms().List(ctx)
		if err != nil {
			return err
		}
		for _, rm := range rooms {
			var beforeSeq int64
			for {
				msgs, err := a.rdb.Messages().List(ctx, rm.ID, beforeSeq, 200)
				if err != nil {
					return err
				}
				if len(msgs) == 0 {
					break
				}
				for _, m := range msgs {
					if m.Role == domain.MessageRoleSystem {
						continue
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					case docsCh <- storage.SearchDocument{
						ID:         m.ID,
						EntityType: "message",
						Scope:      "room:" + rm.ID,
						OwnerID:    m.AuthorID,
						ObservedBy: m.ObservedBy,
						Text:       m.Content,
						OccurredAt: m.CreatedAt,
						Revision:   m.Seq,
					}:
					}
				}
				if len(msgs) < 200 {
					break
				}
				beforeSeq = msgs[0].Seq
			}

			mems, err := a.rdb.Memories().ListMemories(ctx, rm.ID, "", "", 10000)
			if err != nil {
				return err
			}
			for _, mem := range mems {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case docsCh <- storage.SearchDocument{
					ID:         mem.ID,
					EntityType: "memory",
					Scope:      "room:" + rm.ID,
					OwnerID:    mem.OwnerID,
					ObservedBy: mem.ObservedBy,
					Text:       mem.Content,
					OccurredAt: mem.OccurredAt,
					Revision:   mem.Revision,
					Vector:     mem.Embedding,
				}:
				}
			}
		}

		docs, err := a.rdb.Knowledge().ListDocs(ctx, "")
		if err != nil {
			return err
		}
		for _, kd := range docs {
			chunks, err := a.rdb.Knowledge().ListChunksByDoc(ctx, kd.ID)
			if err != nil {
				return err
			}
			for _, ch := range chunks {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case docsCh <- storage.SearchDocument{
					ID:         ch.ID,
					EntityType: "knowledge_chunk",
					Scope:      kd.Scope,
					Text:       ch.Text,
					OccurredAt: kd.UpdatedAt,
					Revision:   ch.DocRevision,
					Vector:     ch.Embedding,
				}:
				}
			}
		}
		return nil
	}()

	rebuildErr := <-errCh
	if emitErr != nil {
		return emitErr
	}
	return rebuildErr
}
