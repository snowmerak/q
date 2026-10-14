package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client/systemone"
)

func TestTerminalModelFailureSurvivesSearchErrors(t *testing.T) {
	r := run{}
	for i := range 16 {
		r.fail(fmt.Errorf("metasearch engine failure %d", i))
	}
	r.fail(context.DeadlineExceeded)
	if len(r.out.Errors) != 8 || r.out.Errors[7] != context.DeadlineExceeded.Error() {
		t.Fatal(r.out.Errors)
	}
}

func TestDecisionStateOnlyIncludesSelectableCandidates(t *testing.T) {
	r := run{engine: &Engine{}, visited: map[string]bool{}, blockedURLs: map[string]bool{}, rejected: map[string]bool{}, blockedActions: map[string]bool{}}
	for i := range 20 {
		r.out.Candidates = append(r.out.Candidates, Candidate{URL: fmt.Sprintf("https://example.com/%d", i), Title: "Candidate", Snippet: strings.Repeat("snippet ", 70)})
	}
	for i := range 10 {
		r.blockedURLs[fmt.Sprintf("https://example.com/%d", i)] = true
	}
	state := r.state().(map[string]any)
	candidates := state["candidates"].([]map[string]any)
	choices, _ := r.actions()
	if len(candidates) != 8 || candidates[0]["id"] != "candidate_10" {
		t.Fatal(candidates)
	}
	for _, candidate := range candidates {
		if choices[candidate["id"].(string)] == "" || len(candidate["snippet"].(string)) > 240 {
			t.Fatal("unselectable or oversized candidate in decision state")
		}
	}
	compact, _ := json.Marshal(candidates)
	full, _ := json.Marshal(r.out.Candidates)
	if len(compact) >= len(full) {
		t.Fatal("decision state did not shrink")
	}
}

func TestNativeRequestTimingRetainsFailureAndPhase(t *testing.T) {
	r := run{}
	_, err := r.chooseWith(t.Context(), decisionFunc(func(ctx context.Context, _ systemone.Request, _ systemone.CallOptions) (*systemone.Result, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("native deadline lost")
		}
		return nil, context.DeadlineExceeded
	}), "decision-model", time.Second, false, map[string]systemone.Question{"publisher": {Type: systemone.QuestionChoice, Criteria: map[string]string{"accept": "Accept", "reject": "Reject"}}}, "source")
	if !errors.Is(err, context.DeadlineExceeded) || len(r.out.SystemOneTimings) != 1 {
		t.Fatal(r.out.SystemOneTimings, err)
	}
	timing := r.out.SystemOneTimings[0]
	if timing.Phase != "source" || timing.Model != "decision-model" || timing.InputBytes == 0 || timing.Error == "" || timing.QueueSeconds < 0 || timing.RequestSeconds < 0 {
		t.Fatal(timing)
	}
}
