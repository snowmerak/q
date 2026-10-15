package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/diagnostics"
)

func TestSearchLogsConnectDiscoverySelectionReadingAndCoverage(t *testing.T) {
	var logs bytes.Buffer
	ctx := diagnostics.WithRequestID(diagnostics.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)).With("session_id", "session", "run_id", "run")), "")
	u := "https://official.test/article;jsessionid=SESSION_SECRET?boardSeq=7&token=TOKEN_SECRET#private"
	s := &fakeSession{pages: map[string]Document{u: doc(u, "BODY_SECRET an explicit fact")}}
	e := Engine{NewSession: func() Session { return s }, Discovery: metaSearchFunc(func(context.Context, string) (SearchResults, error) {
		return SearchResults{Candidates: []Candidate{{URL: "javascript:bad?key=KEY_SECRET"}, {URL: u}}}, nil
	}), Decision: decisionFunc(func(_ context.Context, req systemone.Request, _ systemone.CallOptions) (*systemone.Result, error) {
		return pageAnswers(req, func(key string, _ systemone.Question) string {
			switch {
			case key == "page":
				return "candidate_0"
			case key == "publisher":
				return "accept"
			case strings.HasPrefix(key, "block_"):
				return "keep"
			default:
				return "supported"
			}
		}), nil
	})}
	out, err := e.Search(ctx, Input{Goal: "GOAL_SECRET", Query: "QUERY_SECRET"})
	if err != nil || out.Status != "complete" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	for _, secret := range []string{"BODY_SECRET", "GOAL_SECRET", "QUERY_SECRET", "SESSION_SECRET", "TOKEN_SECRET", "KEY_SECRET"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("logged sensitive data: %s", secret)
		}
	}
	events := map[string]bool{}
	searchID := ""
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record["request_id"] != diagnostics.RequestID(ctx) || record["session_id"] != "session" || record["run_id"] != "run" {
			t.Fatalf("lost parent scope: %s", line)
		}
		id, _ := record["search_id"].(string)
		if searchID == "" {
			searchID = id
		}
		if id == "" || id != searchID {
			t.Fatal("search correlation lost", string(line))
		}
		events[record["msg"].(string)] = true
		if record["msg"] == "web_search.page.read.completed" && record["final_url"] != "https://official.test/article?boardSeq=7" {
			t.Fatalf("unexpected URL log %s", line)
		}
	}
	for _, event := range []string{"web_search.started", "web_search.metasearch.started", "web_search.candidate.filtered", "web_search.candidate.discovered", "web_search.metasearch.completed", "web_search.candidate.selected", "web_search.page.started", "web_search.page.read.started", "web_search.page.read.completed", "web_search.classification.progress", "web_search.model.waiting", "web_search.model.started", "web_search.model.completed", "web_search.page.completed", "web_search.coverage.completed", "web_search.completed"} {
		if !events[event] {
			t.Fatal("missing stage", event, logs.String())
		}
	}
}

func TestBatchLogsKeepSearchesSeparate(t *testing.T) {
	var logs bytes.Buffer
	ctx := diagnostics.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)))
	e := Engine{NewSession: func() Session { return &fakeSession{pages: map[string]Document{}} }, Discovery: metaSearchFunc(func(context.Context, string) (SearchResults, error) { return SearchResults{}, nil }), Decision: sequence(t)}
	if _, err := SearchBatch(ctx, &e, BatchInput{Tasks: []BatchTask{batchTask("alpha"), batchTask("beta")}, Concurrency: 2}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	batchID := ""
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] != "web_search.started" {
			continue
		}
		id := record["search_batch_id"].(string)
		if batchID == "" {
			batchID = id
		}
		if id != batchID {
			t.Fatal("batch IDs differ")
		}
		ids[record["search_task_id"].(string)] = record["search_id"].(string)
	}
	if len(ids) != 2 || ids["alpha"] == ids["beta"] {
		t.Fatal("searches mixed", ids)
	}
}
