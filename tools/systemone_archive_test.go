package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/lsp"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/systemoneconfig"
	"github.com/snowmerak/q/tools/builtin"
)

func TestSystemOneArchiveSearchRanks32BeforePagination(t *testing.T) {
	requests := 0
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var body systemone.Request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Model != "archive-model" || len(body.Questions) != 32 {
			t.Errorf("decision model = %q, candidates = %d", body.Model, len(body.Questions))
		}
		answers := make(map[string]systemone.Answer)
		for index := range 32 {
			question := body.Questions[decisionQuestionName(index)]
			candidate := question.Instructions.(map[string]any)["candidate"].(map[string]any)
			if candidate["summary"] == "own-response" {
				t.Error("own response reached System One")
			}
			score := float64(index / 8)
			answers[decisionQuestionName(index)] = systemone.Answer{Type: systemone.QuestionScore, Score: &score}
		}
		_ = json.NewEncoder(w).Encode(systemone.Result{Model: body.Model, Answers: answers})
	}))
	t.Cleanup(server.Close)
	settings := systemoneconfig.Store{Dir: t.TempDir()}
	value := systemoneconfig.Default()
	value.Providers[0].URI, value.Providers[0].APIKeyEnv = server.URL+"/v1/systemone", ""
	value.RoleModels = map[string]string{systemoneconfig.RoleArchiveDecision: "typesafe/archive-model"}
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	archive, err := sessionstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archive.Close() })
	created := time.Now().UTC().Add(-time.Hour)
	for index := 1; index <= 41; index++ {
		record := sessionstore.Record{
			ID: fmt.Sprintf("record-%02d", index), Kind: sessionstore.KindMessage, RunID: "run",
			Content: "archive fixture", CreatedAt: created.Add(time.Duration(index) * time.Minute),
		}
		if index == 41 {
			record.Summary = "own-response"
			record.Payload = json.RawMessage(`{"message":{"role":"assistant","tool_calls":[{"id":"search"}]}}`)
		}
		if _, err := archive.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := NewRuntimeWithArchiveAndLoomOptionsAndLSPAndLibrary(t.Context(), root, archive,
		loom.StoreOptions{}, lsp.GlobalConfig{}, lsp.WorkspaceConfig{}, nil, WithSystemOneArchiveRanking(settings))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	ctx := sessionstore.WithSearchScope(t.Context(), "run", "")
	search := func(runtime *Runtime, arguments string) builtin.SearchArchiveOutput {
		t.Helper()
		result, err := runtime.Call(ctx, client.ToolCall{ID: "search", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: "search_archive", Arguments: arguments}})
		if err != nil || result.IsError {
			t.Fatalf("search = %#v, error = %v", result, err)
		}
		var output builtin.SearchArchiveOutput
		if err := decodeReceiptResult(result.Content, &output); err != nil {
			t.Fatal(err)
		}
		return output
	}
	for _, test := range []struct {
		arguments string
		count     int
		first     string
	}{
		{`{"query":"archive fixture"}`, 8, "record-16"},
		{`{"query":"archive fixture","limit":12}`, 12, "record-16"},
		{`{"query":"archive fixture","limit":1}`, 1, "record-16"},
		{`{"query":"archive fixture","offset":8}`, 8, "record-24"},
		{`{"query":"archive fixture","offset":31}`, 1, "record-33"},
		{`{"query":"archive fixture","offset":32}`, 0, ""},
	} {
		output := search(runtime, test.arguments)
		if output.Total != 32 || len(output.Hits) != test.count || len(output.Warnings) != 0 ||
			(test.count > 0 && output.Hits[0].ID != test.first) {
			t.Fatalf("%s: %#v", test.arguments, output)
		}
	}
	if requests != 5 {
		t.Fatalf("decision calls = %d, want one per nonempty page", requests)
	}
	for _, arguments := range []string{`{"limit":13}`, `{"limit":-1}`, `{"offset":33}`} {
		result, err := runtime.Call(ctx, client.ToolCall{ID: "search", Function: client.FunctionCall{Name: "search_archive", Arguments: arguments}})
		if err == nil && !result.IsError {
			t.Fatalf("accepted invalid bounds %s", arguments)
		}
	}
	for _, arguments := range []string{`{}`, `{"query":"archive fixture","sort":"newest"}`, `{"query":"archive fixture","sort":"oldest"}`, `{"query":"absentword"}`} {
		search(runtime, arguments)
	}
	if requests != 5 {
		t.Fatal("chronological, empty, or invalid searches invoked System One")
	}
	cloned, err := runtime.NewCheckoutRuntime(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cloned.Close() })
	if output := search(cloned, `{"query":"archive fixture"}`); output.Hits[0].ID != "record-16" || requests != 6 {
		t.Fatalf("checkout lost archive ranking: %#v, calls = %d", output, requests)
	}
	fail = true
	output := search(runtime, `{"query":"archive fixture","limit":12}`)
	if len(output.Hits) != 12 || output.Hits[0].ID != "record-40" || len(output.Warnings) != 1 {
		t.Fatalf("fallback = %#v", output)
	}
}

func TestSystemOneArchiveRankerBoundsRequestsAndReloadsRole(t *testing.T) {
	requests := 0
	wantModel := "archive-model"
	invalid := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		encoded, err := io.ReadAll(r.Body)
		if err != nil || len(encoded) > 65536 {
			t.Errorf("decision request bytes = %d, error = %v", len(encoded), err)
		}
		var body systemone.Request
		if err := json.Unmarshal(encoded, &body); err != nil {
			t.Error(err)
			return
		}
		if body.Model != wantModel {
			t.Errorf("model = %q, want %q", body.Model, wantModel)
		}
		answers := make(map[string]systemone.Answer)
		for index := range len(body.Questions) {
			score := 2.0
			answer := systemone.Answer{Type: systemone.QuestionScore, Score: &score}
			if index == 1 {
				switch invalid {
				case "missing":
					continue
				case "type":
					answer.Type = systemone.QuestionChoice
				case "null":
					answer.Score = nil
				case "range":
					score = 5
				}
			}
			answers[decisionQuestionName(index)] = answer
		}
		_ = json.NewEncoder(w).Encode(systemone.Result{Answers: answers})
	}))
	t.Cleanup(server.Close)
	settings := systemoneconfig.Store{Dir: t.TempDir()}
	ranker := &systemOneArchiveRanker{store: settings}
	if ranker.Enabled() {
		t.Fatal("enabled without settings")
	}
	value := systemoneconfig.Default()
	value.Providers[0].URI, value.Providers[0].APIKeyEnv = server.URL+"/v1/systemone", ""
	value.DefaultModel = "typesafe/default-model"
	value.RoleModels = map[string]string{systemoneconfig.RoleArchiveDecision: "typesafe/archive-model"}
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	if !ranker.Enabled() {
		t.Fatal("live settings did not enable decisions")
	}
	hits := make([]builtin.ArchiveSearchHit, 32)
	for index := range hits {
		long := strings.Repeat("\x00\n\\\"<>&한글", 2000)
		hits[index] = builtin.ArchiveSearchHit{ID: fmt.Sprint(index), Summary: long, Excerpt: long, Kind: long, Role: long, Status: long, Score: 100}
	}
	ranked, err := ranker.RankArchive(t.Context(), strings.Repeat("<query>", 2000), hits)
	if err != nil || requests != 1 || len(ranked) != 32 {
		t.Fatalf("bounded evaluation: calls=%d, error=%v", requests, err)
	}
	for index := range hits {
		if ranked[index].ID != hits[index].ID || ranked[index].Score != 2 || hits[index].Score != 100 {
			t.Fatal("ties changed order or scoring mutated retrieval hits")
		}
	}
	delete(value.RoleModels, systemoneconfig.RoleArchiveDecision)
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	wantModel = "default-model"
	for _, failure := range []string{"missing", "type", "null", "range"} {
		invalid = failure
		if _, err := ranker.RankArchive(t.Context(), "query", hits[:2]); err == nil {
			t.Errorf("accepted %s scores", failure)
		}
	}
	invalid = ""
	if _, err := ranker.RankArchive(t.Context(), "query", hits[:2]); err != nil {
		t.Fatal(err)
	}
	before := requests
	if _, err := ranker.RankArchive(t.Context(), "query", nil); err != nil || requests != before {
		t.Fatalf("empty search invoked System One: %v", err)
	}
}
