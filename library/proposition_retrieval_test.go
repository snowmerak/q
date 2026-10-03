package library

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
)

func TestPropositionQueueRetrievalMergesBeyondInitialCandidates(t *testing.T) {
	dir := t.TempDir()
	seedLibraryRecords(t, dir, sessionstore.Record{
		ID: "earlier", Kind: sessionstore.KindProposition, Scope: "global", Content: "SQLite",
		Refs: []string{"run:earlier"}, Payload: json.RawMessage(`{}`),
	})
	configured := &fakePropositionJudgeClient{messages: []client.Message{
		propositionToolMessage("search_propositions", `{"query":"SQLite"}`),
		propositionToolMessage("get_proposition", `{"id":"earlier"}`),
		propositionToolMessage("resolve_proposition", `{"action":"merge","target_id":"earlier","reason":"same database decision"}`),
	}}
	options := testOptions(dir, testConfig(t))
	options.Judge = &modelPropositionJudge{client: configured, model: "librarian"}
	runtime, err := EnsureWithOptions(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	request := PropositionRegisterRequest{
		Content: "Embedded relational database", Queries: []string{"database choice"}, Confidence: 0.9,
		Refs: []string{"run:new"}, ExtractorModel: "thinker", ExtractorVersion: "v1",
	}
	result, err := runtime.Client().RegisterProposition(t.Context(), "lookup/merge", request)
	if err != nil || !result.Merged || result.ID != "earlier" {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	var input struct {
		Candidates []PropositionSearchHit `json:"candidates"`
	}
	_, body, _ := strings.Cut(configured.requests[0].Messages[1].Content, "\n")
	if err := json.Unmarshal([]byte(body), &input); err != nil || len(input.Candidates) != 0 {
		t.Fatalf("test must start without a matching seed candidate: input=%s err=%v", body, err)
	}
	got, err := runtime.Client().GetProposition(t.Context(), "earlier")
	if err != nil || got.Content != "SQLite" || !reflect.DeepEqual(got.Refs, []string{"run:earlier", "run:new"}) {
		t.Fatalf("stored proposition=%#v err=%v", got, err)
	}
	retried, err := runtime.Client().RegisterProposition(t.Context(), "lookup/merge", request)
	if err != nil || retried != result || len(configured.requests) != 3 {
		t.Fatalf("receipt repeated research: result=%#v err=%v rounds=%d", retried, err, len(configured.requests))
	}
}

func TestPropositionQueueRetrievalFailurePersistsNoDecisionAndCanRetry(t *testing.T) {
	dir := t.TempDir()
	seedLibraryRecords(t, dir, sessionstore.Record{ID: "earlier", Kind: sessionstore.KindProposition, Scope: "global", Content: "SQLite", Payload: json.RawMessage(`{}`)})
	configured := &fakePropositionJudgeClient{message: propositionToolMessage("search_propositions", `{"query":"SQLite"}`)}
	options := testOptions(dir, testConfig(t))
	options.Judge = &modelPropositionJudge{client: configured, model: "librarian"}
	runtime, err := EnsureWithOptions(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	request := PropositionRegisterRequest{
		Content: "Embedded relational database", Confidence: 0.9, Refs: []string{"run:new"}, ExtractorModel: "thinker", ExtractorVersion: "v1",
	}
	_, err = runtime.Client().RegisterProposition(t.Context(), "lookup/retry", request)
	if err == nil || !strings.Contains(err.Error(), "search budget") {
		t.Fatalf("unbounded research error=%v", err)
	}
	var state string
	var decision []byte
	if err := runtime.leader.queue.db.QueryRow(`SELECT state, decision_json FROM proposition_jobs WHERE idempotency_key = ?`, "lookup/retry").Scan(&state, &decision); err != nil || state != "failed" || len(decision) != 0 {
		t.Fatalf("state=%q decision=%s err=%v", state, decision, err)
	}
	got, err := runtime.Client().GetProposition(t.Context(), "earlier")
	if err != nil || len(got.Refs) != 0 {
		t.Fatalf("failed research mutated memory: %#v %v", got, err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	configured = &fakePropositionJudgeClient{messages: []client.Message{
		propositionToolMessage("search_propositions", `{"query":"SQLite"}`),
		propositionToolMessage("resolve_proposition", `{"action":"merge","target_id":"earlier","reason":"equivalent"}`),
	}}
	options.Judge = &modelPropositionJudge{client: configured, model: "librarian"}
	runtime, err = EnsureWithOptions(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Client().RegisterProposition(t.Context(), "lookup/retry", request)
	if err != nil || !result.Merged || result.ID != "earlier" || len(configured.requests) != 2 {
		t.Fatalf("retry=%#v error=%v rounds=%d", result, err, len(configured.requests))
	}
}

func TestPropositionQueueReplaysPersistedRetrievalDecisionWithoutModel(t *testing.T) {
	dir := t.TempDir()
	seedLibraryRecords(t, dir, sessionstore.Record{ID: "earlier", Kind: sessionstore.KindProposition, Scope: "global", Content: "SQLite", Payload: json.RawMessage(`{}`)})
	request := PropositionRegisterRequest{
		Content: "Embedded relational database", Confidence: 0.9, Refs: []string{"run:replay"}, ExtractorModel: "thinker", ExtractorVersion: "v1",
	}
	_, _, digest, err := propositionRegistrationIdentity("lookup/replay", request)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := openPropositionQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = queue.close() }()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	// Model a crash after the final decision was durably saved, before apply.
	_, err = queue.db.Exec(`INSERT INTO proposition_jobs(idempotency_key, request_digest, request_json, state, created_at, updated_at) VALUES(?, ?, ?, 'running', ?, ?)`,
		"lookup/replay", digest, body, time.Now().UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.saveDecision(t.Context(), "lookup/replay", PropositionDecision{Action: PropositionActionMerge, TargetID: "earlier", Reason: "found by additional lookup"}); err != nil {
		t.Fatal(err)
	}
	if err := queue.close(); err != nil {
		t.Fatal(err)
	}
	configured := &fakePropositionJudgeClient{errors: map[string]error{"librarian": errors.New("model must not run during replay")}}
	options := testOptions(dir, testConfig(t))
	options.Judge = &modelPropositionJudge{client: configured, model: "librarian"}
	runtime, err := EnsureWithOptions(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	result, err := runtime.Client().RegisterProposition(t.Context(), "lookup/replay", request)
	if err != nil || !result.Merged || result.ID != "earlier" || len(configured.requests) != 0 {
		t.Fatalf("replayed=%#v err=%v rounds=%d", result, err, len(configured.requests))
	}
	got, err := runtime.Client().GetProposition(t.Context(), "earlier")
	if err != nil || !reflect.DeepEqual(got.Refs, []string{"run:replay"}) {
		t.Fatalf("replay did not apply: %#v %v", got, err)
	}
}

type unobservedTargetJudge struct{ createTestPropositionJudge }

func (unobservedTargetJudge) JudgePropositionWithRetrieval(context.Context, PropositionRegisterRequest, []PropositionSearchHit, PropositionLookup) (PropositionDecision, error) {
	return PropositionDecision{Action: PropositionActionMerge, TargetID: "earlier"}, nil
}

func TestPropositionQueueValidatesTargetsOutsideModelJudge(t *testing.T) {
	dir := t.TempDir()
	seedLibraryRecords(t, dir, sessionstore.Record{ID: "earlier", Kind: sessionstore.KindProposition, Scope: "global", Content: "SQLite", Payload: json.RawMessage(`{}`)})
	options := testOptions(dir, testConfig(t))
	options.Judge = unobservedTargetJudge{}
	runtime, err := EnsureWithOptions(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	_, err = runtime.Client().RegisterProposition(t.Context(), "lookup/forged", PropositionRegisterRequest{
		Content: "Embedded relational database", Confidence: 0.9, Refs: []string{"forged"}, ExtractorModel: "thinker", ExtractorVersion: "v1",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown candidate") {
		t.Fatalf("err=%v", err)
	}
	got, err := runtime.Client().GetProposition(t.Context(), "earlier")
	if err != nil || len(got.Refs) != 0 {
		t.Fatalf("unobserved target mutated: %#v %v", got, err)
	}
}
