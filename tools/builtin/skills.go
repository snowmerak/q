package builtin

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/snowmerak/q/agentskills"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/sessionstore"
)

type SearchSkillsInput struct {
	Query  string   `json:"query" jsonschema:"Keywords describing the procedure or expertise needed"`
	Scopes []string `json:"scopes,omitempty" jsonschema:"Optional exact scopes: global or project"`
	Tags   []string `json:"tags,omitempty" jsonschema:"Optional tags; a skill must match at least one"`
	Limit  int      `json:"limit,omitempty" jsonschema:"Maximum results; uses the Session Store default when omitted"`
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

type SearchSkillsOutput struct {
	Total    uint64           `json:"total"`
	Hits     []SkillSearchHit `json:"hits"`
	Warnings []string         `json:"warnings,omitempty"`
}

type GlobalSkillLibrary interface {
	SearchSkills(context.Context, qlibrary.SkillSearchRequest) (qlibrary.SkillSearchResponse, error)
	GetSkill(context.Context, string, string) (qlibrary.SkillResource, error)
}

type GetSkillInput struct {
	ID   string `json:"id" jsonschema:"Exact skill ID returned by search_skills"`
	Path string `json:"path,omitempty" jsonschema:"Optional skill-relative resource path; defaults to SKILL.md"`
}

type GetSkillOutput struct {
	Skill    agentskills.Skill `json:"skill"`
	Path     string            `json:"path"`
	Artifact loom.Artifact     `json:"artifact"`
	Stored   bool              `json:"stored"`
}

func searchSkills(ctx context.Context, archive Archive, global GlobalSkillLibrary, input SearchSkillsInput) (SearchSkillsOutput, error) {
	if archive == nil {
		return SearchSkillsOutput{}, errors.New("[E_SKILLS] Session Store is unavailable")
	}
	if global == nil {
		return searchLocalSkills(ctx, archive, input, input.Scopes)
	}
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return SearchSkillsOutput{}, errors.New("[E_SKILLS] limit must be between 1 and 100")
	}
	includeGlobal, includeProject := requestedSkillScopes(input.Scopes)
	output := SearchSkillsOutput{}
	if includeProject {
		local, err := searchLocalSkills(ctx, archive, SearchSkillsInput{
			Query: input.Query, Tags: input.Tags, Limit: limit,
		}, []string{"project"})
		if err != nil {
			return SearchSkillsOutput{}, err
		}
		output.Total += local.Total
		output.Hits = append(output.Hits, local.Hits...)
	}
	if includeGlobal {
		remote, err := global.SearchSkills(ctx, qlibrary.SkillSearchRequest{
			Query: input.Query, Tags: input.Tags, Limit: limit,
		})
		if err != nil {
			output.Warnings = append(output.Warnings, "global Library skills unavailable: "+err.Error())
		} else {
			output.Total += remote.Total
			for _, hit := range remote.Hits {
				output.Hits = append(output.Hits, SkillSearchHit{
					ID: hit.ID, Title: hit.Title, Description: hit.Description, Tags: hit.Tags,
					Scope: hit.Scope, Location: hit.Location, Score: hit.Score,
				})
			}
		}
	}
	output.Hits = collapseShadowedSkills(output.Hits)
	sort.SliceStable(output.Hits, func(i, j int) bool {
		if output.Hits[i].Score != output.Hits[j].Score {
			return output.Hits[i].Score > output.Hits[j].Score
		}
		if output.Hits[i].Title != output.Hits[j].Title {
			return output.Hits[i].Title < output.Hits[j].Title
		}
		return output.Hits[i].ID < output.Hits[j].ID
	})
	if len(output.Hits) > limit {
		output.Hits = output.Hits[:limit]
	}
	return output, nil
}

func collapseShadowedSkills(hits []SkillSearchHit) []SkillSearchHit {
	result := make([]SkillSearchHit, 0, len(hits))
	byName := make(map[string]int, len(hits))
	for _, hit := range hits {
		name := strings.ToLower(strings.TrimSpace(hit.Title))
		if index, exists := byName[name]; exists {
			if result[index].Scope != "project" && hit.Scope == "project" {
				result[index] = hit
			}
			continue
		}
		byName[name] = len(result)
		result = append(result, hit)
	}
	return result
}

func searchLocalSkills(ctx context.Context, archive Archive, input SearchSkillsInput, scopes []string) (SearchSkillsOutput, error) {
	result, err := archive.Search(ctx, sessionstore.SearchOptions{
		Text: input.Query, Sort: sessionstore.SortRelevance, Limit: input.Limit,
		Filters: sessionstore.Filters{
			Kinds: []string{sessionstore.KindSkill}, Scopes: scopes, Tags: input.Tags,
		},
	})
	if err != nil {
		return SearchSkillsOutput{}, fmt.Errorf("[E_SKILLS] search: %w", err)
	}
	output := SearchSkillsOutput{Total: result.Total, Hits: make([]SkillSearchHit, 0, len(result.Hits))}
	for _, hit := range result.Hits {
		output.Hits = append(output.Hits, SkillSearchHit{
			ID: hit.Record.ID, Title: hit.Record.Summary, Description: hit.Record.Content,
			Tags: hit.Record.Tags, Scope: hit.Record.Scope, Location: hit.Record.Location, Score: hit.Score,
		})
	}
	return output, nil
}

func requestedSkillScopes(scopes []string) (global, project bool) {
	if len(scopes) == 0 {
		return true, true
	}
	for _, scope := range scopes {
		switch strings.ToLower(strings.TrimSpace(scope)) {
		case "global":
			global = true
		case "project":
			project = true
		}
	}
	return global, project
}

func getSkill(ctx context.Context, registry *agentskills.Registry, global GlobalSkillLibrary, runtime *LoomRuntime, input GetSkillInput) (GetSkillOutput, error) {
	if registry == nil || runtime == nil || runtime.Store == nil {
		return GetSkillOutput{}, errors.New("[E_SKILLS] skill or Loom runtime is unavailable")
	}
	var skill agentskills.Skill
	var path string
	var content []byte
	local, localErr := registry.SkillByID(input.ID)
	if localErr == nil && (local.Scope == "project" || global == nil) {
		var err error
		skill, path, content, err = registry.ReadSkillFile(input.ID, input.Path)
		if err != nil {
			return GetSkillOutput{}, err
		}
	} else {
		if global == nil {
			return GetSkillOutput{}, localErr
		}
		resource, err := global.GetSkill(ctx, input.ID, input.Path)
		if err != nil {
			return GetSkillOutput{}, fmt.Errorf("[E_SKILLS] get global skill: %w", err)
		}
		skill, path, content = resource.Skill, resource.Path, resource.Content
	}
	mediaType := "text/plain"
	if strings.EqualFold(filepath.Ext(path), ".md") {
		mediaType = "text/markdown"
	}
	artifact, err := runtime.Store.Put(ctx, content, loom.PutOptions{
		Kind: "agent-skill", MediaType: mediaType,
		Source: map[string]string{
			"skill_id": skill.ID, "skill": skill.Name, "scope": skill.Scope,
			"location": skill.Directory, "path": path,
		},
	})
	if err != nil {
		return GetSkillOutput{}, err
	}
	return GetSkillOutput{Skill: skill, Path: path, Artifact: artifact, Stored: true}, nil
}
