package agentskills

import "testing"

func TestSearchOptionsUsesSkillHybridRankingPolicy(t *testing.T) {
	options := SearchOptions("review", 8, []string{"project"}, []string{"go"})
	if options.TextBoosts == nil || options.TextBoosts.Summary != 4 ||
		options.TextBoosts.SearchText != 3 || options.TextBoosts.Content != 2 {
		t.Fatalf("text boosts = %#v", options.TextBoosts)
	}
	if options.HybridTextGate == nil || options.HybridTextGate.MinimumTextScore != 0.05 ||
		options.HybridTextGate.MinimumVectorScore != 0.8 {
		t.Fatalf("hybrid text gate = %#v", options.HybridTextGate)
	}
	if len(options.Filters.Kinds) != 1 || options.Filters.Kinds[0] != "skill" ||
		len(options.Filters.Scopes) != 1 || options.Filters.Scopes[0] != "project" ||
		len(options.Filters.Tags) != 1 || options.Filters.Tags[0] != "go" {
		t.Fatalf("filters = %#v", options.Filters)
	}
}
