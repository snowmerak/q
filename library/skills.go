package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/sessionstore"
)

const (
	defaultSkillSearchLimit = 20
	maximumSkillSearchLimit = 100
	maximumSkillRequestBody = 64 << 10
	defaultSkillEmbedBatch  = 32
	maximumSkillEmbedBatch  = 32
	maximumSkillEmbedBody   = 8 << 20
)

type SkillSearchRequest struct {
	Query          string    `json:"query"`
	Tags           []string  `json:"tags,omitempty"`
	Limit          int       `json:"limit,omitempty"`
	Embedding      []float32 `json:"embedding,omitempty"`
	EmbeddingModel string    `json:"embedding_model,omitempty"`
}

type SkillSearchHit struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Scope       string   `json:"scope"`
	Location    string   `json:"location"`
	Score       float64  `json:"score,omitempty"`
}

type SkillSearchResponse struct {
	Total uint64           `json:"total"`
	Hits  []SkillSearchHit `json:"hits"`
}

type SkillResource struct {
	Skill   agentskills.Skill `json:"skill"`
	Path    string            `json:"path"`
	Content []byte            `json:"content"`
}

type SkillReloadResponse struct {
	Active int                 `json:"active"`
	Issues []agentskills.Issue `json:"issues,omitempty"`
}

type SkillVectorConfigRequest struct {
	Model      string `json:"model,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
}

type SkillEmbeddingSourceRequest struct {
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	Limit      int    `json:"limit,omitempty"`
}

type SkillEmbeddingSource struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
	Text   string `json:"text"`
}

type SkillEmbeddingSourceResponse struct {
	Remaining uint64                 `json:"remaining"`
	Sources   []SkillEmbeddingSource `json:"sources"`
}

type SkillEmbeddingItem struct {
	ID        string    `json:"id"`
	Digest    string    `json:"digest"`
	Embedding []float32 `json:"embedding"`
}

type SkillEmbeddingApplyRequest struct {
	Model      string               `json:"model"`
	Dimensions int                  `json:"dimensions"`
	Items      []SkillEmbeddingItem `json:"items"`
}

type SkillEmbeddingApplyResponse struct {
	Updated int `json:"updated"`
}

type SkillEmbeddingSyncStats struct {
	Embedded int `json:"embedded"`
}

type skillService struct {
	registry *agentskills.Registry
	archive  *sessionstore.Store
	mu       sync.Mutex
}

func newSkillService(ctx context.Context, dir string, archive *sessionstore.Store) (*skillService, error) {
	home := dir
	if strings.EqualFold(filepath.Base(filepath.Clean(dir)), ".q") {
		home = filepath.Dir(filepath.Clean(dir))
	}
	registry, err := agentskills.DiscoverGlobal(home, dir)
	if err != nil {
		return nil, err
	}
	service := &skillService{registry: registry, archive: archive}
	if err := registry.SyncRecords(ctx, archive); err != nil {
		return nil, fmt.Errorf("library: reconcile global Agent Skills: %w", err)
	}
	return service, nil
}

func (s *skillService) search(ctx context.Context, request SkillSearchRequest) (SkillSearchResponse, error) {
	if s == nil || s.archive == nil {
		return SkillSearchResponse{}, errors.New("library: global Agent Skills are unavailable")
	}
	if request.Limit == 0 {
		request.Limit = defaultSkillSearchLimit
	}
	if request.Limit < 1 || request.Limit > maximumSkillSearchLimit {
		return SkillSearchResponse{}, fmt.Errorf("library: skill search limit must be between 1 and %d", maximumSkillSearchLimit)
	}
	options := sessionstore.SearchOptions{
		Text: request.Query, Sort: sessionstore.SortRelevance, Limit: request.Limit,
		TextBoosts: &sessionstore.TextFieldBoosts{Summary: 4, Content: 2, SearchText: 3},
		Filters: sessionstore.Filters{
			Kinds: []string{sessionstore.KindSkill}, Scopes: []string{"global"}, Tags: request.Tags,
		},
	}
	if len(request.Embedding) > 0 && skillVectorMatches(
		s.archive.VectorConfig(), strings.TrimSpace(request.EmbeddingModel), len(request.Embedding),
	) {
		options.Vector = &sessionstore.VectorQuery{Embedding: append([]float32(nil), request.Embedding...)}
	}
	result, err := s.archive.Search(ctx, options)
	if err != nil {
		return SkillSearchResponse{}, err
	}
	response := SkillSearchResponse{Total: result.Total, Hits: make([]SkillSearchHit, 0, len(result.Hits))}
	for _, hit := range result.Hits {
		response.Hits = append(response.Hits, SkillSearchHit{
			ID: hit.Record.ID, Title: hit.Record.Summary, Description: hit.Record.Content,
			Tags: hit.Record.Tags, Scope: hit.Record.Scope, Location: hit.Record.Location, Score: hit.Score,
		})
	}
	return response, nil
}

func (s *skillService) configureVector(request SkillVectorConfigRequest) error {
	if s == nil || s.archive == nil {
		return errors.New("library: global Agent Skills are unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.archive.ConfigureVector(sessionstore.VectorConfig{
		Model: strings.TrimSpace(request.Model), Dimensions: request.Dimensions,
	})
}

func (s *skillService) embeddingSources(request SkillEmbeddingSourceRequest) (SkillEmbeddingSourceResponse, error) {
	if s == nil || s.registry == nil || s.archive == nil {
		return SkillEmbeddingSourceResponse{}, errors.New("library: global Agent Skills are unavailable")
	}
	request.Model = strings.TrimSpace(request.Model)
	if !skillVectorMatches(s.archive.VectorConfig(), request.Model, request.Dimensions) {
		return SkillEmbeddingSourceResponse{}, errors.New("library: skill embedding source model does not match the active vector index")
	}
	if request.Limit == 0 {
		request.Limit = defaultSkillEmbedBatch
	}
	if request.Limit < 1 || request.Limit > maximumSkillEmbedBatch {
		return SkillEmbeddingSourceResponse{}, fmt.Errorf("library: skill embedding source limit must be between 1 and %d", maximumSkillEmbedBatch)
	}
	response := SkillEmbeddingSourceResponse{Sources: make([]SkillEmbeddingSource, 0, request.Limit)}
	for _, skill := range s.registry.Skills() {
		record, err := s.archive.Get(skill.ID)
		if err != nil {
			return SkillEmbeddingSourceResponse{}, err
		}
		if record.Embedding != nil && record.Embedding.Model == request.Model &&
			record.Embedding.Dimensions == request.Dimensions && len(record.Embedding.Vector) == request.Dimensions {
			continue
		}
		response.Remaining++
		if len(response.Sources) < request.Limit {
			response.Sources = append(response.Sources, SkillEmbeddingSource{
				ID: skill.ID, Digest: skill.Digest, Text: skillEmbeddingText(skill),
			})
		}
	}
	return response, nil
}

func (s *skillService) applyEmbeddings(request SkillEmbeddingApplyRequest) (SkillEmbeddingApplyResponse, error) {
	if s == nil || s.registry == nil || s.archive == nil {
		return SkillEmbeddingApplyResponse{}, errors.New("library: global Agent Skills are unavailable")
	}
	request.Model = strings.TrimSpace(request.Model)
	if !skillVectorMatches(s.archive.VectorConfig(), request.Model, request.Dimensions) {
		return SkillEmbeddingApplyResponse{}, errors.New("library: skill embedding model does not match the active vector index")
	}
	if len(request.Items) < 1 || len(request.Items) > maximumSkillEmbedBatch {
		return SkillEmbeddingApplyResponse{}, fmt.Errorf("library: skill embedding batch must contain between 1 and %d items", maximumSkillEmbedBatch)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !skillVectorMatches(s.archive.VectorConfig(), request.Model, request.Dimensions) {
		return SkillEmbeddingApplyResponse{}, errors.New("library: skill embedding model changed before the batch was applied")
	}
	active := make(map[string]agentskills.Skill)
	for _, skill := range s.registry.Skills() {
		active[skill.ID] = skill
	}
	now := time.Now().UTC()
	records := make([]sessionstore.Record, 0, len(request.Items))
	seen := make(map[string]struct{}, len(request.Items))
	for _, item := range request.Items {
		skill, ok := active[strings.TrimSpace(item.ID)]
		if !ok {
			return SkillEmbeddingApplyResponse{}, fmt.Errorf("library: unknown active skill %q", item.ID)
		}
		if _, duplicate := seen[skill.ID]; duplicate {
			return SkillEmbeddingApplyResponse{}, fmt.Errorf("library: duplicate skill embedding %q", skill.ID)
		}
		seen[skill.ID] = struct{}{}
		if strings.TrimSpace(item.Digest) != skill.Digest {
			return SkillEmbeddingApplyResponse{}, fmt.Errorf("library: skill %q changed during embedding", skill.Name)
		}
		if len(item.Embedding) != request.Dimensions {
			return SkillEmbeddingApplyResponse{}, fmt.Errorf("library: skill %q embedding has %d dimensions; want %d", skill.Name, len(item.Embedding), request.Dimensions)
		}
		record, err := s.archive.Get(skill.ID)
		if err != nil {
			return SkillEmbeddingApplyResponse{}, err
		}
		record.UpdatedAt = time.Time{}
		record.Embedding = &sessionstore.Embedding{
			Model: request.Model, Dimensions: request.Dimensions, CreatedAt: now,
			Vector: append([]float32(nil), item.Embedding...),
		}
		records = append(records, record)
	}
	if _, err := s.archive.SaveBatch(records); err != nil {
		return SkillEmbeddingApplyResponse{}, err
	}
	return SkillEmbeddingApplyResponse{Updated: len(records)}, nil
}

func skillVectorMatches(config sessionstore.VectorConfig, model string, dimensions int) bool {
	return config.Enabled() && config.Model == model && config.Dimensions == dimensions
}

func skillEmbeddingText(skill agentskills.Skill) string {
	parts := []string{skill.Name, skill.Description}
	if len(skill.Tags) > 0 {
		parts = append(parts, strings.Join(skill.Tags, " "))
	}
	return strings.Join(parts, "\n")
}

func (s *skillService) get(id, path string) (SkillResource, error) {
	if s == nil || s.registry == nil {
		return SkillResource{}, errors.New("library: global Agent Skills are unavailable")
	}
	skill, relative, content, err := s.registry.ReadSkillFile(id, path)
	if err != nil {
		return SkillResource{}, err
	}
	if skill.Scope != "global" {
		return SkillResource{}, errors.New("library: requested skill is not global")
	}
	return SkillResource{Skill: skill, Path: relative, Content: content}, nil
}

func (s *skillService) reload(ctx context.Context) (SkillReloadResponse, error) {
	if s == nil || s.registry == nil || s.archive == nil {
		return SkillReloadResponse{}, errors.New("library: global Agent Skills are unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.registry.Reload(); err != nil {
		return SkillReloadResponse{}, err
	}
	// SyncRecords compares persisted digests and only saves changed/new skills
	// or deletes missing ones; unchanged skills are not reindexed.
	if err := s.registry.SyncRecords(ctx, s.archive); err != nil {
		return SkillReloadResponse{}, err
	}
	return SkillReloadResponse{Active: len(s.registry.Skills()), Issues: s.registry.Issues()}, nil
}

func registerSkillRoutes(mux *http.ServeMux, skills *skillService) {
	mux.Handle("POST /v1/skills/search", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input SkillSearchRequest
		if err := decodeLibraryJSON(writer, request, &input); err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		output, err := skills.search(request.Context(), input)
		if err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSON(writer, http.StatusOK, output)
	}))
	mux.Handle("GET /v1/skills/{id}/resources/{path...}", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		output, err := skills.get(request.PathValue("id"), request.PathValue("path"))
		if err != nil {
			writeLibraryError(writer, http.StatusNotFound, err)
			return
		}
		writeJSON(writer, http.StatusOK, output)
	}))
	mux.Handle("POST /v1/skills/reload", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		output, err := skills.reload(request.Context())
		if err != nil {
			writeLibraryError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, output)
	}))
	mux.Handle("POST /v1/skills/embeddings/configure", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input SkillVectorConfigRequest
		if err := decodeLibraryJSON(writer, request, &input); err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		if err := skills.configureVector(input); err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSON(writer, http.StatusOK, struct{}{})
	}))
	mux.Handle("POST /v1/skills/embeddings/sources", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input SkillEmbeddingSourceRequest
		if err := decodeLibraryJSON(writer, request, &input); err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		output, err := skills.embeddingSources(input)
		if err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSON(writer, http.StatusOK, output)
	}))
	mux.Handle("POST /v1/skills/embeddings", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input SkillEmbeddingApplyRequest
		if err := decodeLibraryJSONWithLimit(writer, request, &input, maximumSkillEmbedBody); err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		output, err := skills.applyEmbeddings(input)
		if err != nil {
			writeLibraryError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSON(writer, http.StatusOK, output)
	}))
}

func decodeLibraryJSON(writer http.ResponseWriter, request *http.Request, output any) error {
	return decodeLibraryJSONWithLimit(writer, request, output, maximumSkillRequestBody)
}

func decodeLibraryJSONWithLimit(writer http.ResponseWriter, request *http.Request, output any, limit int64) error {
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("library: decode request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("library: request contains multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("library: decode request: %w", err)
	}
	return nil
}

func writeLibraryError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]any{"error": map[string]any{
		"message": err.Error(), "type": "library_error", "code": "invalid_request",
	}})
}
