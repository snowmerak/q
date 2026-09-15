package builtin

import (
	"context"
	"errors"
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
}

func (s *globalSkillSearch) SearchSkills(context.Context, qlibrary.SkillSearchRequest) (qlibrary.SkillSearchResponse, error) {
	return s.result, nil
}

func (*globalSkillSearch) GetSkill(context.Context, string, string) (qlibrary.SkillResource, error) {
	return qlibrary.SkillResource{}, errors.New("not implemented")
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
