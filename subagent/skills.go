package subagent

import (
	"context"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/tools/builtin"
)

type delegatedSkillHintSearcher interface {
	SearchSkillHints(context.Context, string, int) (builtin.SearchSkillsOutput, error)
}

type delegatedSkillHint struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Scope       string   `json:"scope"`
}

type delegatedSkillHintSet struct {
	Trigger    string               `json:"trigger"`
	Candidates []delegatedSkillHint `json:"candidates"`
}

func taskStartSkillHints(ctx context.Context, runtime ToolRuntime, available []client.Tool, input taskStartInput) *delegatedSkillHintSet {
	searcher, ok := runtime.(delegatedSkillHintSearcher)
	if !ok || !hasTool(available, "search_skills") || !hasTool(available, "get_skill") {
		return nil
	}
	query := strings.Join(append([]string{input.Objective}, input.CompletionCriteria...), "\n")
	query = strings.Join(strings.Fields(query), " ")
	queryRunes := []rune(query)
	if len(queryRunes) > 4000 {
		query = string(queryRunes[:4000])
	}
	if query == "" {
		return nil
	}
	result, err := searcher.SearchSkillHints(ctx, query, 8)
	if err != nil {
		return nil
	}
	hints := &delegatedSkillHintSet{Trigger: "task_start"}
	seen := make(map[string]bool)
	for _, hit := range result.Hits {
		if hit.ID == "" || seen[hit.ID] {
			continue
		}
		seen[hit.ID] = true
		description := []rune(hit.Description)
		if len(description) > 600 {
			description = description[:600]
		}
		tags := make([]string, 0, min(len(hit.Tags), 12))
		for _, tag := range hit.Tags {
			if len(tags) == 12 {
				break
			}
			value := []rune(strings.TrimSpace(tag))
			if len(value) > 80 {
				value = value[:80]
			}
			if len(value) > 0 {
				tags = append(tags, string(value))
			}
		}
		hints.Candidates = append(hints.Candidates, delegatedSkillHint{
			ID: hit.ID, Name: hit.Title, Description: string(description), Tags: tags, Scope: hit.Scope,
		})
		if len(hints.Candidates) == 4 {
			break
		}
	}
	if len(hints.Candidates) == 0 {
		return nil
	}
	return hints
}

func withRetrievalCatalog(prompt string, tools []client.Tool) string {
	if hasTool(tools, "search_skills") && hasTool(tools, "get_skill") {
		prompt += "\n\nAgent Skills are retrieved, not preloaded. At the start of work, or after receiving new information, call search_skills with concise, task-specific keywords when additional guidance is needed to perform the work or handle that information. Select a relevant result, then call get_skill and follow the complete resource text returned directly in content. Explicit $skill-name mentions should be searched by name."
	}
	if hasTool(tools, "search_archive") && hasTool(tools, "get_archive_record") {
		prompt += "\n\nWorkspace archive records are retrieved on demand. Before starting substantive work, call search_archive with concise, task-specific terms to check relevant prior conversations, decisions, agent results, and tool failures. Before finalizing substantive work, search again using any new decision terms, failures, or verification questions revealed by the work. Use get_archive_record only for selected results that need more detail."
	}
	if hasTool(tools, "search_propositions") && hasTool(tools, "get_proposition") {
		prompt += "\n\nDurable cross-workspace facts are stored as global propositions. Search them with search_propositions when prior preferences, decisions, constraints, or reusable resolutions may matter; call get_proposition only for a selected result that needs provenance or extraction details."
	}
	return prompt
}
