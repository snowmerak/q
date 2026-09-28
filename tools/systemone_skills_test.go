package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/systemoneconfig"
	"github.com/snowmerak/q/tools/builtin"
)

func TestSystemOneSkillRankerScoresAllCandidatesInOneRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodPost || request.URL.Path != "/v1/systemone" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("authorization header was not resolved from the configured environment variable")
		}
		var body systemone.Request
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if body.Model != "jev-skill" || len(body.Questions) == 0 || len(body.Questions) > 30 {
			t.Errorf("decision request = %#v", body)
		}
		answers := make(map[string]systemone.Answer, len(body.Questions))
		for index := range len(body.Questions) {
			score := float64(index % len(skillRelevanceCriteria))
			answers[skillDecisionQuestionName(index)] = systemone.Answer{
				Type: systemone.QuestionScore, Score: &score,
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(systemone.Result{Model: body.Model, Answers: answers})
	}))
	t.Cleanup(server.Close)

	store := systemoneconfig.Store{Dir: t.TempDir()}
	ranker := &systemOneSkillRanker{store: store}
	if ranker.Enabled() {
		t.Fatal("ranker enabled without saved System One settings")
	}
	t.Setenv("TEST_SYSTEM_ONE_KEY", "test-key")
	value := systemoneconfig.Default()
	value.Providers[0].URI = server.URL + "/v1/systemone"
	value.Providers[0].APIKeyEnv = "TEST_SYSTEM_ONE_KEY"
	value.RoleModels = map[string]string{
		systemoneconfig.RoleAgentSkillDecision: "typesafe/jev-skill",
	}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	if !ranker.Enabled() {
		t.Fatal("ranker did not enable after settings were saved")
	}

	hits := []builtin.SkillSearchHit{
		{ID: "first", Title: "First", Description: "First candidate"},
		{ID: "second", Title: "Second", Description: "Second candidate"},
		{ID: "third", Title: "Third", Description: "Third candidate"},
	}
	ranked, err := ranker.RankSkills(t.Context(), "Review a Go change", hits)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || len(ranked) != 3 || ranked[0].ID != "third" || ranked[1].ID != "second" || ranked[2].ID != "first" {
		t.Fatalf("requests = %d, ranked = %#v", requests, ranked)
	}

	largeHits := make([]builtin.SkillSearchHit, 30)
	for index := range largeHits {
		largeHits[index] = builtin.SkillSearchHit{
			ID:          fmt.Sprintf("large-%02d", index),
			Title:       strings.Repeat("제목", 200),
			Description: strings.Repeat("설명", 1000),
			Tags:        []string{strings.Repeat("태그", 100), strings.Repeat("분류", 100)},
		}
	}
	large, err := ranker.RankSkills(t.Context(), strings.Repeat("작업", 3000), largeHits)
	if err != nil || requests != 2 || len(large) != 30 {
		t.Fatalf("bounded request: requests = %d, hits = %d, err = %v", requests, len(large), err)
	}
}

func TestSystemOneSkillRankerRejectsIncompleteScores(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body systemone.Request
		_ = json.NewDecoder(request.Body).Decode(&body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"model":%q,"answers":{}}`, body.Model)
	}))
	t.Cleanup(server.Close)

	store := systemoneconfig.Store{Dir: t.TempDir()}
	value := systemoneconfig.Default()
	value.Providers[0].URI = server.URL + "/v1/systemone"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	_, err := (&systemOneSkillRanker{store: store}).RankSkills(t.Context(), "task", []builtin.SkillSearchHit{{ID: "one"}})
	if err == nil {
		t.Fatal("missing System One score was accepted")
	}
}
