package builtin

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/sessionstore"
)

type SearchSkillsInput struct {
	Query  string   `json:"query" jsonschema:"Keywords describing the procedure or expertise needed"`
	Scopes []string `json:"scopes,omitempty" jsonschema:"Optional exact scopes: global or project"`
	Tags   []string `json:"tags,omitempty" jsonschema:"Optional tags; a skill must match at least one"`
	Limit  int      `json:"limit,omitempty" jsonschema:"Maximum results from 1 to 20; defaults to 5"`
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
	Total uint64           `json:"total"`
	Hits  []SkillSearchHit `json:"hits"`
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

func searchSkills(ctx context.Context, archive Archive, registry *agentskills.Registry, input SearchSkillsInput) (SearchSkillsOutput, error) {
	if archive == nil {
		return SearchSkillsOutput{}, errors.New("[E_SKILLS] Session Store is unavailable")
	}
	if store, ok := archive.(agentskills.RecordStore); ok && registry != nil {
		if err := registry.Reload(); err != nil {
			return SearchSkillsOutput{}, fmt.Errorf("[E_SKILLS] refresh discovery: %w", err)
		}
		if err := registry.SyncRecords(ctx, store); err != nil {
			return SearchSkillsOutput{}, fmt.Errorf("[E_SKILLS] refresh index: %w", err)
		}
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" {
		return SearchSkillsOutput{}, errors.New("[E_SKILLS] query is required")
	}
	if utf8.RuneCountInString(input.Query) > 4000 {
		return SearchSkillsOutput{}, errors.New("[E_SKILLS] query exceeds 4000 characters")
	}
	limit := input.Limit
	if limit == 0 {
		limit = 5
	}
	if limit < 1 || limit > 20 {
		return SearchSkillsOutput{}, errors.New("[E_SKILLS] limit must be between 1 and 20")
	}
	for index := range input.Scopes {
		input.Scopes[index] = strings.ToLower(strings.TrimSpace(input.Scopes[index]))
		if input.Scopes[index] != "global" && input.Scopes[index] != "project" {
			return SearchSkillsOutput{}, errors.New("[E_SKILLS] scopes must contain only global or project")
		}
	}
	for index := range input.Tags {
		input.Tags[index] = strings.ToLower(strings.TrimSpace(input.Tags[index]))
	}
	result, err := archive.Search(ctx, sessionstore.SearchOptions{
		Text: input.Query, Sort: sessionstore.SortRelevance, Limit: limit,
		Filters: sessionstore.Filters{
			Kinds: []string{sessionstore.KindSkill}, Scopes: input.Scopes, Tags: input.Tags,
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

func getSkill(ctx context.Context, registry *agentskills.Registry, runtime *LoomRuntime, input GetSkillInput) (GetSkillOutput, error) {
	if registry == nil || runtime == nil || runtime.Store == nil {
		return GetSkillOutput{}, errors.New("[E_SKILLS] skill or Loom runtime is unavailable")
	}
	skill, path, content, err := registry.ReadSkillFile(input.ID, input.Path)
	if err != nil {
		return GetSkillOutput{}, err
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
