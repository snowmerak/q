package builtin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/sessionstore"
)

type skillSearchArchive struct {
	result  sessionstore.SearchResult
	options sessionstore.SearchOptions
}

func (a *skillSearchArchive) Search(_ context.Context, options sessionstore.SearchOptions) (sessionstore.SearchResult, error) {
	a.options = options
	return a.result, nil
}

func (*skillSearchArchive) Get(string) (sessionstore.Record, error) {
	return sessionstore.Record{}, errors.New("not implemented")
}

type globalSkillSearch struct {
	result qlibrary.SkillSearchResponse
	last   qlibrary.SkillSearchRequest
}

func (s *globalSkillSearch) SearchSkills(_ context.Context, request qlibrary.SkillSearchRequest) (qlibrary.SkillSearchResponse, error) {
	s.last = request
	return s.result, nil
}

func (*globalSkillSearch) GetSkill(context.Context, string, string) (qlibrary.SkillResource, error) {
	return qlibrary.SkillResource{}, errors.New("not implemented")
}

type testSkillRanker struct {
	enabled    bool
	err        error
	candidates []SkillSearchHit
}

func (r *testSkillRanker) Enabled() bool { return r.enabled }

func (r *testSkillRanker) RankSkills(_ context.Context, _ string, hits []SkillSearchHit) ([]SkillSearchHit, error) {
	r.candidates = append([]SkillSearchHit(nil), hits...)
	if r.err != nil {
		return nil, r.err
	}
	ranked := append([]SkillSearchHit(nil), hits...)
	for index := range ranked {
		ranked[index].Score = float64(index)
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })
	return ranked, nil
}

func TestSearchSkillsRecomputesTotalAfterCollapsingReturnedHits(t *testing.T) {
	archive := &skillSearchArchive{result: sessionstore.SearchResult{
		Total: 7,
		Hits: []sessionstore.Hit{{Record: sessionstore.Record{
			ID: "workspace", Kind: sessionstore.KindSkill, Scope: "project", Summary: "shared-skill",
		}, Score: 1}},
	}}
	global := &globalSkillSearch{result: qlibrary.SkillSearchResponse{
		Total: 9,
		Hits: []qlibrary.SkillSearchHit{{
			ID: "global", Title: "shared-skill", Scope: "global", Score: 10,
		}},
	}}
	output, err := searchSkills(context.Background(), archive, global, SearchSkillsInput{Query: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if output.Total != 1 || len(output.Hits) != 1 || output.Hits[0].ID != "workspace" {
		t.Fatalf("merged skill output = %#v", output)
	}
}

func TestSearchSkillsAllowsGlobalLibraryWithoutWorkspaceStore(t *testing.T) {
	global := &globalSkillSearch{result: qlibrary.SkillSearchResponse{
		Total: 1,
		Hits: []qlibrary.SkillSearchHit{{
			ID: "global", Title: "global-skill", Scope: "global", Score: 1,
		}},
	}}
	output, err := searchSkills(context.Background(), nil, global, SearchSkillsInput{Query: "global"})
	if err != nil {
		t.Fatal(err)
	}
	if output.Total != 1 || len(output.Hits) != 1 || output.Hits[0].ID != "global" || len(output.Warnings) != 1 {
		t.Fatalf("global-only skill output = %#v", output)
	}
}

func TestSearchLocalSkillsUsesSkillFieldBoosts(t *testing.T) {
	archive := &skillSearchArchive{}
	if _, err := searchLocalSkills(context.Background(), archive, SearchSkillsInput{Query: "review"}, []string{"project"}); err != nil {
		t.Fatal(err)
	}
	boosts := archive.options.TextBoosts
	if boosts == nil || boosts.Summary <= boosts.SearchText || boosts.SearchText <= boosts.Content {
		t.Fatalf("skill text boosts = %#v", boosts)
	}
	gate := archive.options.HybridTextGate
	if gate == nil || gate.MinimumTextScore <= 0 || gate.MinimumVectorScore <= 0 {
		t.Fatalf("skill hybrid text gate = %#v", gate)
	}
}

func TestSearchSkillsUsesSystemOneCandidateAndResultLimits(t *testing.T) {
	global := &globalSkillSearch{}
	for index := range 40 {
		global.result.Hits = append(global.result.Hits, qlibrary.SkillSearchHit{
			ID: fmt.Sprintf("skill-%02d", index), Title: fmt.Sprintf("skill-%02d", index),
			Scope: "global", Score: float64(40 - index),
		})
	}
	for _, test := range []struct {
		name      string
		requested int
		want      int
	}{
		{name: "default", want: 20},
		{name: "smaller", requested: 5, want: 5},
		{name: "candidate pool size", requested: 30, want: 20},
		{name: "larger than candidate pool", requested: 31, want: 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			ranker := &testSkillRanker{enabled: true}
			output, err := searchSkillsWithRanker(t.Context(), nil, global, ranker, SearchSkillsInput{
				Query: "review Go code", Limit: test.requested,
			})
			if err != nil {
				t.Fatal(err)
			}
			if global.last.Limit != 30 || len(ranker.candidates) != 30 {
				t.Fatalf("candidate request = %d, ranked candidates = %d", global.last.Limit, len(ranker.candidates))
			}
			if !output.Reranked || len(output.Hits) != test.want || output.Hits[0].ID != "skill-29" {
				t.Fatalf("ranked output = %#v", output)
			}
		})
	}
}

func TestSearchSkillsFallsBackWhenSystemOneFails(t *testing.T) {
	global := &globalSkillSearch{}
	for index := range 30 {
		global.result.Hits = append(global.result.Hits, qlibrary.SkillSearchHit{
			ID: fmt.Sprintf("skill-%02d", index), Title: fmt.Sprintf("skill-%02d", index),
			Scope: "global", Score: float64(30 - index),
		})
	}
	output, err := searchSkillsWithRanker(t.Context(), nil, global, &testSkillRanker{
		enabled: true, err: errors.New("decision unavailable"),
	}, SearchSkillsInput{Query: "review", Scopes: []string{"global"}})
	if err != nil {
		t.Fatal(err)
	}
	if output.Reranked || len(output.Hits) != 20 || output.Hits[0].ID != "skill-00" || len(output.Warnings) != 1 {
		t.Fatalf("fallback output = %#v", output)
	}
}

func TestSearchSkillHintsUsesSystemOneCandidateAndResultLimits(t *testing.T) {
	global := &globalSkillSearch{}
	for index := range 30 {
		global.result.Hits = append(global.result.Hits, qlibrary.SkillSearchHit{
			ID: fmt.Sprintf("skill-%02d", index), Title: fmt.Sprintf("skill-%02d", index),
			Scope: "global", Score: float64(30 - index),
		})
	}
	ranker := &testSkillRanker{enabled: true}
	output, err := SearchSkillHints(t.Context(), nil, global, ranker, "review", 8)
	if err != nil {
		t.Fatal(err)
	}
	if global.last.Limit != 24 || len(ranker.candidates) != 24 || !output.Reranked || len(output.Hits) != 24 || output.Hits[0].ID != "skill-23" {
		t.Fatalf("hint output = %#v, candidate request = %d, ranked candidates = %d", output, global.last.Limit, len(ranker.candidates))
	}
}
