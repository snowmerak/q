// Package knowledge manages scoped reference documents, overlapping chunk
// splitting, revision updates, outbox indexing, and hybrid search.
package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/memory"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/outbox"
)

const (
	defaultChunkMaxRunes     = 1000
	defaultChunkOverlapRunes = 120
)

var ErrInvalidInput = errors.New("knowledge: invalid input")

type CreateDocInput struct {
	Scope     string `json:"scope"`
	Title     string `json:"title"`
	SourceURI string `json:"source_uri,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Content   string `json:"content"`
}

type UpdateDocInput struct {
	ExpectedRevision int64   `json:"expected_revision"`
	Scope            *string `json:"scope,omitempty"`
	Title            *string `json:"title,omitempty"`
	SourceURI        *string `json:"source_uri,omitempty"`
	MediaType        *string `json:"media_type,omitempty"`
	Content          *string `json:"content,omitempty"`
}

type SearchInput struct {
	Query  string   `json:"query"`
	Mode   string   `json:"mode,omitempty"` // "hybrid" | "lexical" | "semantic"
	Scopes []string `json:"scopes,omitempty"`
	Limit  int      `json:"limit,omitempty"`
}

type KnowledgeHit struct {
	ChunkID      string  `json:"chunk_id"`
	DocID        string  `json:"doc_id"`
	DocTitle     string  `json:"doc_title"`
	Scope        string  `json:"scope"`
	DocRevision  int64   `json:"doc_revision"`
	ChunkOrdinal int     `json:"chunk_ordinal"`
	CharOffset   int     `json:"char_offset"`
	Text         string  `json:"text"`
	Score        float64 `json:"score"`
}

type Service struct {
	rdb        storage.RDBStore
	search     storage.SearchIndex
	embedderFn func() memory.QueryEmbedder
}

func NewService(rdb storage.RDBStore, search storage.SearchIndex, embedderFn func() memory.QueryEmbedder) *Service {
	return &Service{
		rdb:        rdb,
		search:     search,
		embedderFn: embedderFn,
	}
}

type TextChunk struct {
	Ordinal    int
	CharOffset int
	Text       string
}

// ChunkText splits content into bounded rune windows with overlap, preferring
// paragraph and line boundaries when available.
func ChunkText(content string, maxRunes, overlapRunes int) []TextChunk {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if maxRunes <= 0 {
		maxRunes = defaultChunkMaxRunes
	}
	if overlapRunes < 0 {
		overlapRunes = 0
	}
	if overlapRunes >= maxRunes {
		overlapRunes = maxRunes / 4
	}

	runes := []rune(content)
	total := len(runes)
	if total <= maxRunes {
		return []TextChunk{{Ordinal: 0, CharOffset: 0, Text: content}}
	}

	var chunks []TextChunk
	start := 0
	ordinal := 0
	for start < total {
		end := min(start+maxRunes, total)
		if end < total {
			// Prefer splitting at newline near the upper third of the window.
			minBreak := start + (maxRunes*2)/3
			for i := end; i >= minBreak; i-- {
				if runes[i-1] == '\n' {
					end = i
					break
				}
			}
		}
		seg := strings.TrimSpace(string(runes[start:end]))
		if seg != "" {
			chunks = append(chunks, TextChunk{
				Ordinal:    ordinal,
				CharOffset: start,
				Text:       seg,
			})
			ordinal++
		}
		if end >= total {
			break
		}
		next := end - overlapRunes
		if next <= start {
			next = end
		}
		start = next
	}
	return chunks
}

func (s *Service) CreateDoc(ctx context.Context, in CreateDocInput) (domain.KnowledgeDoc, error) {
	in.Scope = strings.TrimSpace(in.Scope)
	if in.Scope == "" {
		in.Scope = "global"
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return domain.KnowledgeDoc{}, fmt.Errorf("%w: title is required", ErrInvalidInput)
	}
	if strings.TrimSpace(in.Content) == "" {
		return domain.KnowledgeDoc{}, fmt.Errorf("%w: content is required", ErrInvalidInput)
	}
	if strings.TrimSpace(in.MediaType) == "" {
		in.MediaType = "text/markdown"
	}

	var created domain.KnowledgeDoc
	err := s.rdb.Tx(ctx, func(tx storage.RDBTx) error {
		doc, err := tx.Knowledge().CreateDoc(ctx, domain.KnowledgeDoc{
			Scope:     in.Scope,
			Title:     in.Title,
			SourceURI: strings.TrimSpace(in.SourceURI),
			MediaType: strings.TrimSpace(in.MediaType),
			Content:   in.Content,
			Revision:  1,
			Status:    domain.KnowledgeStatusActive,
		})
		if err != nil {
			return err
		}
		if err := s.writeChunksAndOutboxTx(ctx, tx, doc, nil); err != nil {
			return err
		}
		created = doc
		return nil
	})
	if err != nil {
		return domain.KnowledgeDoc{}, err
	}
	return created, nil
}

func (s *Service) GetDoc(ctx context.Context, id string) (domain.KnowledgeDoc, error) {
	return s.rdb.Knowledge().GetDoc(ctx, id)
}

func (s *Service) ListDocs(ctx context.Context, scope string) ([]domain.KnowledgeDoc, error) {
	docs, err := s.rdb.Knowledge().ListDocs(ctx, strings.TrimSpace(scope))
	if err != nil {
		return nil, err
	}
	if docs == nil {
		docs = []domain.KnowledgeDoc{}
	}
	return docs, nil
}

func (s *Service) UpdateDoc(ctx context.Context, id string, in UpdateDocInput) (domain.KnowledgeDoc, error) {
	if in.ExpectedRevision < 1 {
		return domain.KnowledgeDoc{}, fmt.Errorf("%w: expected_revision >= 1 is required", ErrInvalidInput)
	}

	var updated domain.KnowledgeDoc
	err := s.rdb.Tx(ctx, func(tx storage.RDBTx) error {
		cur, err := tx.Knowledge().GetDoc(ctx, id)
		if err != nil {
			return err
		}
		if cur.Revision != in.ExpectedRevision {
			return storage.ErrConflict
		}
		if in.Scope != nil {
			sc := strings.TrimSpace(*in.Scope)
			if sc == "" {
				sc = "global"
			}
			cur.Scope = sc
		}
		if in.Title != nil {
			t := strings.TrimSpace(*in.Title)
			if t == "" {
				return fmt.Errorf("%w: title cannot be empty", ErrInvalidInput)
			}
			cur.Title = t
		}
		if in.SourceURI != nil {
			cur.SourceURI = strings.TrimSpace(*in.SourceURI)
		}
		if in.MediaType != nil {
			cur.MediaType = strings.TrimSpace(*in.MediaType)
		}
		if in.Content != nil {
			if strings.TrimSpace(*in.Content) == "" {
				return fmt.Errorf("%w: content cannot be empty", ErrInvalidInput)
			}
			cur.Content = *in.Content
		}

		oldChunks, err := tx.Knowledge().ListChunksByDoc(ctx, id)
		if err != nil {
			return err
		}
		nextDoc, err := tx.Knowledge().UpdateDoc(ctx, cur)
		if err != nil {
			return err
		}
		if err := s.writeChunksAndOutboxTx(ctx, tx, nextDoc, oldChunks); err != nil {
			return err
		}
		updated = nextDoc
		return nil
	})
	if err != nil {
		return domain.KnowledgeDoc{}, err
	}
	return updated, nil
}

func (s *Service) DeleteDoc(ctx context.Context, id string) error {
	return s.rdb.Tx(ctx, func(tx storage.RDBTx) error {
		doc, err := tx.Knowledge().GetDoc(ctx, id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil
			}
			return err
		}
		oldChunks, err := tx.Knowledge().ListChunksByDoc(ctx, id)
		if err != nil {
			return err
		}
		for _, ch := range oldChunks {
			if _, err := tx.Outbox().Enqueue(ctx, domain.OutboxEntry{
				EntityType: "knowledge_chunk",
				EntityID:   ch.ID,
				Op:         domain.OutboxOpDelete,
				Revision:   doc.Revision + 1,
				Status:     domain.OutboxStatusPending,
			}); err != nil {
				return err
			}
		}
		if err := tx.Knowledge().ReplaceChunks(ctx, id, nil); err != nil {
			return err
		}
		return tx.Knowledge().DeleteDoc(ctx, id, domain.NowMS())
	})
}

func (s *Service) writeChunksAndOutboxTx(ctx context.Context, tx storage.RDBTx, doc domain.KnowledgeDoc, oldChunks []domain.KnowledgeChunk) error {
	for _, old := range oldChunks {
		if _, err := tx.Outbox().Enqueue(ctx, domain.OutboxEntry{
			EntityType: "knowledge_chunk",
			EntityID:   old.ID,
			Op:         domain.OutboxOpDelete,
			Revision:   doc.Revision,
			Status:     domain.OutboxStatusPending,
		}); err != nil {
			return err
		}
	}

	rawChunks := ChunkText(doc.Content, defaultChunkMaxRunes, defaultChunkOverlapRunes)
	newChunks := make([]domain.KnowledgeChunk, len(rawChunks))
	for i, c := range rawChunks {
		newChunks[i] = domain.KnowledgeChunk{
			ID:           domain.NewID(),
			DocID:        doc.ID,
			DocRevision:  doc.Revision,
			ChunkOrdinal: c.Ordinal,
			CharOffset:   c.CharOffset,
			Text:         c.Text,
		}
	}
	if err := tx.Knowledge().ReplaceChunks(ctx, doc.ID, newChunks); err != nil {
		return err
	}
	for _, ch := range newChunks {
		payloadBytes, err := json.Marshal(outbox.Payload{
			Scope:      doc.Scope,
			OwnerID:    doc.ID,
			Text:       doc.Title + "\n\n" + ch.Text,
			OccurredAt: doc.UpdatedAt,
		})
		if err != nil {
			return err
		}
		if _, err := tx.Outbox().Enqueue(ctx, domain.OutboxEntry{
			EntityType:  "knowledge_chunk",
			EntityID:    ch.ID,
			Op:          domain.OutboxOpUpsert,
			Revision:    doc.Revision,
			PayloadJSON: string(payloadBytes),
			Status:      domain.OutboxStatusPending,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Search(ctx context.Context, in SearchInput) ([]KnowledgeHit, error) {
	if s.search == nil || s.rdb == nil {
		return []KnowledgeHit{}, nil
	}
	queryText := strings.TrimSpace(in.Query)
	if queryText == "" || utf8.RuneCountInString(queryText) == 0 {
		return []KnowledgeHit{}, nil
	}
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	switch mode {
	case "", "hybrid", "lexical", "semantic":
	default:
		return nil, fmt.Errorf("%w: unsupported search mode %q", ErrInvalidInput, in.Mode)
	}
	if mode == "" {
		mode = "hybrid"
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	var vec []float32
	if mode != "lexical" && s.embedderFn != nil {
		if emb := s.embedderFn(); emb != nil {
			if v, err := emb.EmbedQuery(ctx, queryText); err == nil && len(v) > 0 {
				vec = v
			}
		}
	}
	if mode == "hybrid" && len(vec) == 0 {
		mode = "lexical"
	}

	hits, err := s.search.Search(ctx, storage.SearchQuery{
		Text:        queryText,
		Vector:      vec,
		Mode:        mode,
		EntityTypes: []string{"knowledge_chunk"},
		Scopes:      in.Scopes,
		Limit:       limit,
	})
	if err != nil {
		return nil, err
	}

	docCache := make(map[string]domain.KnowledgeDoc)
	out := make([]KnowledgeHit, 0, len(hits))
	for _, hit := range hits {
		ch, err := s.rdb.Knowledge().GetChunk(ctx, hit.ID)
		if err != nil {
			continue
		}
		doc, ok := docCache[ch.DocID]
		if !ok {
			loaded, err := s.rdb.Knowledge().GetDoc(ctx, ch.DocID)
			if err != nil {
				continue
			}
			doc = loaded
			docCache[ch.DocID] = doc
		}
		if doc.Status != domain.KnowledgeStatusActive || ch.DocRevision != doc.Revision {
			continue
		}
		out = append(out, KnowledgeHit{
			ChunkID:      ch.ID,
			DocID:        doc.ID,
			DocTitle:     doc.Title,
			Scope:        doc.Scope,
			DocRevision:  doc.Revision,
			ChunkOrdinal: ch.ChunkOrdinal,
			CharOffset:   ch.CharOffset,
			Text:         ch.Text,
			Score:        hit.Score,
		})
	}
	return out, nil
}
