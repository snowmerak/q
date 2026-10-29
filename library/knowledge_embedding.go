package library

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/snowmerak/q/sessionstore"
)

type WorkEmbeddingSource struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Digest      string `json:"digest"`
	Text        string `json:"text"`
}
type WorkEmbeddingBatch struct {
	Model      string                `json:"model"`
	Dimensions int                   `json:"dimensions"`
	Sources    []WorkEmbeddingSource `json:"sources"`
}
type WorkEmbeddingApply struct {
	Model      string                `json:"model"`
	Dimensions int                   `json:"dimensions"`
	Sources    []WorkEmbeddingSource `json:"sources"`
	Vectors    [][]float32           `json:"vectors"`
}

func (k *knowledgeIndex) sources(ctx context.Context) (WorkEmbeddingBatch, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	p, err := k.projection(ctx, "")
	if err != nil {
		return WorkEmbeddingBatch{}, err
	}
	if err := k.sync(ctx, p); err != nil {
		return WorkEmbeddingBatch{}, err
	}
	out := WorkEmbeddingBatch{Model: k.vector.Model, Dimensions: k.vector.Dimensions, Sources: []WorkEmbeddingSource{}}
	if out.Model == "" {
		return out, nil
	}
	for offset := 0; ; offset += 100 {
		result, err := p.store.Search(ctx, sessionstore.SearchOptions{Limit: 100, Offset: offset, Sort: sessionstore.SortOldest})
		if err != nil {
			return out, err
		}
		for _, hit := range result.Hits {
			record := hit.Record
			workspace, err := k.projection(ctx, record.WorkspaceID)
			if err != nil {
				return out, err
			}
			if err := k.sync(ctx, workspace); err != nil {
				return out, err
			}
			local, localErr := workspace.store.Get(record.ID)
			matched := func(r sessionstore.Record) bool {
				return r.Embedding != nil && r.Embedding.Model == out.Model && r.Embedding.Dimensions == out.Dimensions
			}
			if matched(record) && localErr == nil && matched(local) {
				continue
			}
			digest, err := sessionstore.RecordSourceDigest(record)
			if err != nil {
				return out, err
			}
			out.Sources = append(out.Sources, WorkEmbeddingSource{ID: record.ID, WorkspaceID: record.WorkspaceID, Digest: digest, Text: record.Summary + "\n" + record.SearchText + "\n" + record.Content})
			if len(out.Sources) == 16 {
				return out, nil
			}
		}
		if len(result.Hits) < 100 {
			return out, nil
		}
	}
}

func (k *knowledgeIndex) apply(ctx context.Context, in WorkEmbeddingApply) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	p, err := k.projection(ctx, "")
	if err != nil {
		return 0, err
	}
	if in.Model != k.vector.Model || in.Dimensions != k.vector.Dimensions || len(in.Sources) != len(in.Vectors) || len(in.Sources) > 16 {
		return 0, errors.New("library: stale or invalid knowledge embedding batch")
	}
	updated := 0
	for n, source := range in.Sources {
		record, err := p.store.Get(source.ID)
		if errors.Is(err, sessionstore.ErrNotFound) {
			continue
		}
		if err != nil {
			return updated, err
		}
		post, err := k.source.GetPost(ctx, record.PostID, 0)
		if err != nil {
			return updated, err
		}
		item, err := k.source.GetItem(ctx, record.EntityID)
		if err != nil {
			return updated, err
		}
		metadata := chunkMetadata{}
		if err := json.Unmarshal(record.Payload, &metadata); err != nil {
			return updated, err
		}
		if post.Deleted || item.Deleted || post.Revision != record.SourceRevision || item.Revision != metadata.ItemRevision {
			continue
		}
		digest, err := sessionstore.RecordSourceDigest(record)
		if err != nil {
			return updated, err
		}
		if digest != source.Digest || record.WorkspaceID != source.WorkspaceID {
			continue
		}
		workspace, err := k.projection(ctx, source.WorkspaceID)
		if err != nil {
			return updated, err
		}
		if err := k.sync(ctx, workspace); err != nil {
			return updated, err
		}
		update := sessionstore.EmbeddingUpdate{ID: record.ID, SourceDigest: digest, Embedding: &sessionstore.Embedding{Model: in.Model, Dimensions: in.Dimensions, CreatedAt: time.Now().UTC(), Vector: in.Vectors[n]}}
		for _, projection := range []*knowledgeProjection{p, workspace} {
			if _, err := projection.store.ApplyEmbeddings([]sessionstore.EmbeddingUpdate{update}); err != nil {
				return updated, err
			}
		}
		updated++
	}
	return updated, nil
}

func (c *Client) SyncWorkEmbeddings(ctx context.Context) (int, error) {
	configured := c.embeddingConfig()
	if configured.model == "" || configured.provider == nil {
		return 0, nil
	}
	var batch WorkEmbeddingBatch
	if err := c.doJSONWithClient(ctx, c.index, http.MethodGet, "/work/embeddings", nil, &batch, nil); err != nil {
		return 0, err
	}
	if len(batch.Sources) == 0 {
		return 0, nil
	}
	if batch.Model != configured.model || batch.Dimensions != configured.dimensions {
		return 0, errors.New("library: knowledge embedding configuration changed")
	}
	texts := make([]string, len(batch.Sources))
	for i, s := range batch.Sources {
		texts[i] = s.Text
	}
	vectors, err := embedTexts(ctx, configured, texts)
	if err != nil {
		return 0, err
	}
	var output struct {
		Updated int `json:"updated"`
	}
	err = c.doJSONWithClient(ctx, c.index, http.MethodPost, "/work/embeddings", WorkEmbeddingApply{Model: batch.Model, Dimensions: batch.Dimensions, Sources: batch.Sources, Vectors: vectors}, &output, nil)
	return output.Updated, err
}

func (c *Client) PendingWorkEmbeddings(ctx context.Context) (int, error) {
	var batch WorkEmbeddingBatch
	err := c.doJSONWithClient(ctx, c.index, http.MethodGet, "/work/embeddings", nil, &batch, nil)
	return len(batch.Sources), err
}

func registerWorkEmbeddingRoutes(mux *http.ServeMux, index *knowledgeIndex) {
	mux.HandleFunc("GET /v1/work/embeddings", func(w http.ResponseWriter, r *http.Request) {
		out, err := index.sources(r.Context())
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /v1/work/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var in WorkEmbeddingApply
		if err := decodeLibraryJSONWithLimit(w, r, &in, 4<<20); err != nil {
			writeLibraryError(w, 400, err)
			return
		}
		updated, err := index.apply(r.Context(), in)
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]int{"updated": updated})
	})
	mux.HandleFunc("POST /v1/work/reindex", func(w http.ResponseWriter, r *http.Request) {
		index.mu.Lock()
		defer index.mu.Unlock()
		p, err := index.projection(r.Context(), "")
		if err == nil {
			err = index.source.ResetReceipt(r.Context(), p.key)
		}
		for _, p := range index.projections {
			if err == nil {
				err = index.source.ResetReceipt(r.Context(), p.key)
			}
		}
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, 202, map[string]bool{"queued": true})
	})
}
