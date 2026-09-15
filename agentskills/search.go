package agentskills

import "github.com/snowmerak/q/sessionstore"

const (
	minimumSkillHybridTextScore   = 0.05
	minimumSkillHybridVectorScore = 0.8
)

// SearchOptions applies the shared lexical and semantic ranking policy used by
// global and workspace Agent Skill retrieval.
func SearchOptions(query string, limit int, scopes, tags []string) sessionstore.SearchOptions {
	return sessionstore.SearchOptions{
		Text: query, Sort: sessionstore.SortRelevance, Limit: limit,
		TextBoosts: &sessionstore.TextFieldBoosts{Summary: 4, Content: 2, SearchText: 3},
		HybridTextGate: &sessionstore.HybridTextGate{
			MinimumTextScore: minimumSkillHybridTextScore, MinimumVectorScore: minimumSkillHybridVectorScore,
		},
		Filters: sessionstore.Filters{
			Kinds: []string{sessionstore.KindSkill}, Scopes: append([]string(nil), scopes...), Tags: append([]string(nil), tags...),
		},
	}
}
