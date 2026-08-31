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

	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/sessionstore"
)

const (
	defaultSkillSearchLimit = 20
	maximumSkillSearchLimit = 100
	maximumSkillRequestBody = 64 << 10
)

type SkillSearchRequest struct {
	Query string   `json:"query"`
	Tags  []string `json:"tags,omitempty"`
	Limit int      `json:"limit,omitempty"`
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
	result, err := s.archive.Search(ctx, sessionstore.SearchOptions{
		Text: request.Query, Sort: sessionstore.SortRelevance, Limit: request.Limit,
		Filters: sessionstore.Filters{
			Kinds: []string{sessionstore.KindSkill}, Scopes: []string{"global"}, Tags: request.Tags,
		},
	})
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
}

func decodeLibraryJSON(writer http.ResponseWriter, request *http.Request, output any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumSkillRequestBody)
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
