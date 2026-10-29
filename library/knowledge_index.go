package library

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/worklock"
)

var ErrSemanticUnavailable = errors.New("library: semantic search requires a configured embedding model and indexed vectors")

type WorkSearchRequest struct {
	WorkspaceID    string    `json:"workspace_id,omitempty"`
	All            bool      `json:"all,omitempty"`
	Query          string    `json:"query"`
	Mode           string    `json:"mode,omitempty"`
	Kind           string    `json:"kind,omitempty"`
	States         []string  `json:"states,omitempty"`
	Labels         []string  `json:"labels,omitempty"`
	Limit          int       `json:"limit,omitempty"`
	Cursor         string    `json:"cursor,omitempty"`
	Embedding      []float32 `json:"embedding,omitempty"`
	EmbeddingModel string    `json:"embedding_model,omitempty"`
}
type WorkSearchHit struct {
	WorkspaceID  string  `json:"workspace_id"`
	ItemID       string  `json:"item_id"`
	PostID       string  `json:"post_id"`
	Kind         string  `json:"kind"`
	Title        string  `json:"title"`
	Snippet      string  `json:"snippet"`
	Revision     int64   `json:"revision"`
	ItemRevision int64   `json:"item_revision"`
	Offset       int     `json:"offset"`
	Score        float64 `json:"score"`
}
type WorkSearchResponse struct {
	Hits             []WorkSearchHit `json:"hits"`
	Total            uint64          `json:"total"`
	TotalRelation    string          `json:"total_relation"`
	Mode             string          `json:"mode"`
	Fallback         string          `json:"fallback,omitempty"`
	IndexPending     bool            `json:"index_pending"`
	NextCursor       string          `json:"next_cursor,omitempty"`
	Generation       string          `json:"generation"`
	EmbeddingPending bool            `json:"embedding_pending"`
}
type chunkMetadata struct {
	ItemRevision int64 `json:"item_revision"`
	Offset       int   `json:"offset"`
}
type knowledgeProjection struct {
	key         string
	workspaceID string
	store       *sessionstore.Store
}
type knowledgeIndex struct {
	mu          sync.Mutex
	dir         string
	source      *workitems.Store
	lock        *worklock.Lock
	vector      sessionstore.VectorConfig
	generation  string
	projections map[string]*knowledgeProjection
	cancel      context.CancelFunc
	done        chan struct{}
}

func newKnowledgeIndex(parent context.Context, dir string, source *workitems.Store, lock *worklock.Lock, vector sessionstore.VectorConfig) *knowledgeIndex {
	ctx, cancel := context.WithCancel(parent)
	index := &knowledgeIndex{dir: dir, source: source, lock: lock, vector: vector, projections: map[string]*knowledgeProjection{}, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(index.done)
		timer := time.NewTicker(500 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				index.mu.Lock()
				_, _ = index.projection(ctx, "")
				for _, projection := range index.projections {
					_ = index.sync(ctx, projection)
				}
				index.mu.Unlock()
			}
		}
	}()
	return index
}
func (k *knowledgeIndex) Close() error {
	k.cancel()
	<-k.done
	k.mu.Lock()
	defer k.mu.Unlock()
	var result error
	for _, p := range k.projections {
		result = errors.Join(result, p.store.Close())
	}
	return result
}

func (k *knowledgeIndex) projection(ctx context.Context, workspaceID string) (*knowledgeProjection, error) {
	vector := k.vector
	if saved, err := (config.Store{Dir: k.dir}).Load(); err == nil {
		vector = sessionstore.VectorConfig{Model: saved.Embedding.Model, Dimensions: saved.Embedding.Dimensions}
	} else if !errors.Is(err, config.ErrNotFound) {
		return nil, err
	}
	body, _ := json.Marshal([]any{"knowledge-v1", sessionstore.MappingVersion, vector.Model, vector.Dimensions})
	digest := sha256.Sum256(body)
	generation := hex.EncodeToString(digest[:8])
	if k.generation != "" && k.generation != generation {
		for _, p := range k.projections {
			if err := p.store.Close(); err != nil {
				return nil, err
			}
		}
		k.projections = map[string]*knowledgeProjection{}
	}
	k.generation, k.vector = generation, vector
	name := "all"
	if workspaceID != "" {
		name = "workspace-" + workspaceID
		projects, err := k.source.ListWorkspaces(ctx, true)
		if err != nil {
			return nil, err
		}
		found := false
		for _, p := range projects {
			found = found || p.ID == workspaceID
		}
		if !found {
			return nil, workitems.ErrNotFound
		}
	}
	if existing := k.projections[name]; existing != nil {
		return existing, nil
	}
	directory := filepath.Join("library", "knowledge-index", generation, name)
	_, statErr := os.Stat(filepath.Join(k.dir, directory, "data", "records"))
	store, err := sessionstore.OpenWithOptions(k.dir, sessionstore.OpenOptions{Directory: directory, WorkspaceLock: k.lock, Vector: vector})
	if err != nil {
		return nil, err
	}
	projection := &knowledgeProjection{key: generation + "/" + name, workspaceID: workspaceID, store: store}
	if errors.Is(statErr, os.ErrNotExist) {
		if err := k.source.ResetReceipt(ctx, projection.key); err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	k.projections[name] = projection
	return projection, nil
}

func (k *knowledgeIndex) sync(ctx context.Context, p *knowledgeProjection) error {
	after, err := k.source.Applied(ctx, p.key)
	if err != nil {
		return err
	}
	changes, err := k.source.Changes(ctx, after, 100)
	if err != nil {
		return err
	}
	for _, change := range changes {
		if p.workspaceID == "" || p.workspaceID == change.WorkspaceID {
			item, err := k.source.GetItem(ctx, change.ItemID)
			if err != nil {
				return err
			}
			cursor := ""
			for {
				thread, err := k.source.ReadThread(ctx, item.ID, cursor, 100)
				if err != nil {
					return err
				}
				for _, post := range thread.Posts {
					if change.PostID != "" && post.ID != change.PostID {
						continue
					}
					post, err = k.source.GetPost(ctx, post.ID, 0)
					if err != nil {
						return err
					}
					if err := k.indexPost(ctx, p, item, post); err != nil {
						return err
					}
				}
				cursor = thread.NextCursor
				if cursor == "" {
					break
				}
			}
		}
		if err := k.source.ApplyReceipt(ctx, p.key, change.Seq); err != nil {
			return err
		}
	}
	return nil
}

func (k *knowledgeIndex) indexPost(ctx context.Context, p *knowledgeProjection, item workitems.Item, post workitems.Post) error {
	old := map[string]sessionstore.Record{}
	for offset := 0; ; offset += 100 {
		result, err := p.store.Search(ctx, sessionstore.SearchOptions{Filters: sessionstore.Filters{PostIDs: []string{post.ID}}, Limit: 100, Offset: offset})
		if err != nil {
			return err
		}
		for _, hit := range result.Hits {
			old[hit.Record.ID] = hit.Record
		}
		if len(result.Hits) < 100 {
			break
		}
	}
	if !item.Deleted && !post.Deleted {
		content := []rune(post.Content)
		records := make([]sessionstore.Record, 0, (len(content)+2047)/2048)
		for start := 0; start < len(content); start += 2048 {
			id := fmt.Sprintf("%s-%d", post.ID, start/2048)
			text := string(content[start:min(start+2048, len(content))])
			metadata, _ := json.Marshal(chunkMetadata{ItemRevision: item.Revision, Offset: start})
			record := sessionstore.Record{ID: id, Kind: item.Kind, WorkspaceID: item.WorkspaceID, EntityID: item.ID, PostID: post.ID, SourceRevision: post.Revision, Status: item.State, Summary: item.Title, Content: text, SearchText: strings.Join(append(append([]string(nil), item.Labels...), item.Acceptance...), " "), Tags: item.Labels, Refs: post.Refs, CreatedAt: post.CreatedAt, UpdatedAt: post.UpdatedAt, Payload: metadata}
			if previous, found := old[id]; found && previous.Content == record.Content && previous.Summary == record.Summary && previous.SearchText == record.SearchText {
				record.Embedding = previous.Embedding
				record.VectorProjections = previous.VectorProjections
			}
			records = append(records, record)
			delete(old, id)
		}
		if len(records) > 0 {
			if _, err := p.store.SaveBatch(records); err != nil {
				return err
			}
		}
	}
	for id := range old {
		if err := p.store.Delete(id); err != nil {
			return err
		}
	}
	return nil
}

func (k *knowledgeIndex) search(ctx context.Context, in WorkSearchRequest) (WorkSearchResponse, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !in.All && in.WorkspaceID == "" || len(in.Query) > 8192 {
		return WorkSearchResponse{}, workitems.ErrInvalidInput
	}
	workspaceID := in.WorkspaceID
	if in.All {
		workspaceID = ""
	}
	p, err := k.projection(ctx, workspaceID)
	if err != nil {
		return WorkSearchResponse{}, err
	}
	if err := k.sync(ctx, p); err != nil {
		return WorkSearchResponse{}, err
	}
	seq, err := k.source.Applied(ctx, p.key)
	if err != nil {
		return WorkSearchResponse{}, err
	}
	latest, err := k.source.LatestChange(ctx)
	if err != nil {
		return WorkSearchResponse{}, err
	}
	if in.Mode == "" {
		in.Mode = "hybrid"
	}
	if in.Mode != "hybrid" && in.Mode != "lexical" && in.Mode != "semantic" {
		return WorkSearchResponse{}, workitems.ErrInvalidInput
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 100)
	options := sessionstore.SearchOptions{Text: in.Query, Limit: limit, Filters: sessionstore.Filters{Statuses: in.States, Tags: in.Labels}}
	if in.Kind != "" {
		if in.Kind != workitems.Issue && in.Kind != workitems.Ticket {
			return WorkSearchResponse{}, workitems.ErrInvalidInput
		}
		options.Filters.Kinds = []string{in.Kind}
	}
	out := WorkSearchResponse{Hits: []WorkSearchHit{}, Mode: in.Mode, Generation: k.generation, IndexPending: seq < latest, TotalRelation: "exact_chunks"}
	documents, vectorCount, err := p.store.IndexCounts()
	if err != nil {
		return out, err
	}
	out.EmbeddingPending = k.vector.Enabled() && uint64(vectorCount) < documents
	if in.Mode != "lexical" {
		if len(in.Embedding) == k.vector.Dimensions && len(in.Embedding) > 0 && in.EmbeddingModel == k.vector.Model && vectorCount > 0 {
			options.Vector = &sessionstore.VectorQuery{Embedding: in.Embedding}
			out.TotalRelation = "candidate_lower_bound"
			if in.Mode == "semantic" {
				options.Text = ""
			}
		} else if in.Mode == "semantic" {
			return out, ErrSemanticUnavailable
		} else {
			out.Mode = "lexical"
			out.Fallback = "embedding_unavailable"
		}
	}
	fingerprintInput := in
	fingerprintInput.Cursor = ""
	fingerprintInput.Embedding = nil
	body, _ := json.Marshal(fingerprintInput)
	hash := sha256.Sum256(body)
	type cursor struct {
		Fingerprint string `json:"q"`
		Generation  string `json:"g"`
		Seq         int64  `json:"s"`
		Offset      int    `json:"o"`
	}
	position := cursor{Fingerprint: hex.EncodeToString(hash[:]), Generation: k.generation, Seq: seq}
	if in.Cursor != "" {
		if len(in.Cursor) > 2048 {
			return out, workitems.ErrInvalidInput
		}
		body, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		if err != nil || json.Unmarshal(body, &position) != nil || position.Fingerprint != hex.EncodeToString(hash[:]) || position.Offset < 0 {
			return out, workitems.ErrInvalidInput
		}
		if position.Generation != k.generation || position.Seq != seq {
			return out, workitems.ErrConflict
		}
	}
	options.Offset = position.Offset
	result, err := p.store.Search(ctx, options)
	if err != nil {
		return out, err
	}
	out.Total = result.Total
	for _, hit := range result.Hits {
		post, err := k.source.GetPost(ctx, hit.Record.PostID, 0)
		if err != nil {
			return out, err
		}
		item, err := k.source.GetItem(ctx, hit.Record.EntityID)
		if err != nil {
			return out, err
		}
		var metadata chunkMetadata
		if err := json.Unmarshal(hit.Record.Payload, &metadata); err != nil {
			return out, err
		}
		if post.Deleted || item.Deleted || post.Revision != hit.Record.SourceRevision || item.Revision != metadata.ItemRevision {
			out.IndexPending = true
			continue
		}
		out.Hits = append(out.Hits, WorkSearchHit{WorkspaceID: post.WorkspaceID, ItemID: item.ID, PostID: post.ID, Kind: item.Kind, Title: item.Title, Snippet: string([]rune(hit.Record.Content)[:min(512, len([]rune(hit.Record.Content)))]), Revision: post.Revision, ItemRevision: item.Revision, Offset: metadata.Offset, Score: hit.Score})
	}
	if position.Offset+len(result.Hits) < int(result.Total) && len(result.Hits) > 0 {
		position.Offset += len(result.Hits)
		body, _ := json.Marshal(position)
		out.NextCursor = base64.RawURLEncoding.EncodeToString(body)
	}
	return out, nil
}

func (c *Client) SearchWork(ctx context.Context, input WorkSearchRequest) (WorkSearchResponse, error) {
	configured := c.embeddingConfig()
	if input.Mode != "lexical" {
		current, err := c.workEmbeddingConfig(ctx)
		if err == nil {
			configured = current
		} else if input.Mode == "semantic" {
			return WorkSearchResponse{}, errors.Join(ErrSemanticUnavailable, err)
		} else {
			configured = embeddingClientConfig{}
		}
	}
	if input.Mode != "lexical" && len(input.Embedding) == 0 && configured.provider != nil && configured.model != "" && input.Query != "" {
		vectors, err := embedTexts(ctx, configured, []string{input.Query})
		if err == nil && len(vectors) == 1 {
			input.Embedding = vectors[0]
			input.EmbeddingModel = configured.model
		} else if input.Mode == "semantic" {
			return WorkSearchResponse{}, errors.Join(ErrSemanticUnavailable, err)
		}
	}
	var out WorkSearchResponse
	err := c.doJSONWithClient(ctx, c.index, http.MethodPost, "/work/search", input, &out, nil)
	return out, err
}

func registerKnowledgeRoutes(mux *http.ServeMux, index *knowledgeIndex) {
	registerWorkEmbeddingRoutes(mux, index)
	mux.HandleFunc("POST /v1/work/search", func(w http.ResponseWriter, r *http.Request) {
		var in WorkSearchRequest
		if err := decodeLibraryJSON(w, r, &in); err != nil {
			writeLibraryError(w, 400, err)
			return
		}
		out, err := index.search(r.Context(), in)
		if errors.Is(err, ErrSemanticUnavailable) {
			writeLibraryError(w, 503, err)
			return
		}
		if err != nil {
			workspaceError(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
}
