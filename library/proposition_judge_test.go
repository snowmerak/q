package library

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

type fakePropositionJudgeClient struct {
	requests         []client.ChatRequest
	terminalRequests []client.ChatRequest
	terminalErr      error
	message          client.Message
	errors           map[string]error
	messages         []client.Message
	beforeChat       func(context.Context, client.ChatRequest) error
}

func (f *fakePropositionJudgeClient) Chat(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	if request.ToolChoice == client.ToolChoiceNone && len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Role == client.RoleTool {
		f.terminalRequests = append(f.terminalRequests, request)
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant}, FinishReason: "stop"}}}, f.terminalErr
	}
	f.requests = append(f.requests, request)
	if f.beforeChat != nil {
		if err := f.beforeChat(ctx, request); err != nil {
			return nil, err
		}
	}
	if err := f.errors[request.Model]; err != nil {
		return nil, err
	}
	message := f.message
	if len(f.messages) > 0 {
		message = f.messages[0]
		f.messages = f.messages[1:]
	}
	return &client.ChatResponse{ConversationID: "conversation-" + request.Model, Choices: []client.Choice{{Message: message}}}, nil
}

func (*fakePropositionJudgeClient) Close() error { return nil }

func TestConfiguredPropositionJudgeUsesLibrarianRole(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()

	directory := t.TempDir()
	value := config.Default()
	value.Provider.Model = "main-model"
	value.Provider.BaseURL = server.URL
	value.Embedding = config.EmbeddingConfig{Model: "embed-model", Dimensions: 3}
	value.ModelGroups = map[string]config.ModelGroupConfig{
		"library": {Candidates: []config.ModelCandidateConfig{
			{Model: "librarian-model", ReasoningEffort: "medium", Timeout: 20 * time.Second},
			{Model: "backup-model", ReasoningEffort: "high"},
		}},
	}
	value.Agents.Roles = map[string]config.AgentConfig{
		config.AgentRoleThinker:   {Model: "thinker-model", ReasoningEffort: "high"},
		config.AgentRoleLibrarian: {Group: "library"},
	}
	if err := (config.Store{Dir: directory}).Save(value); err != nil {
		t.Fatal(err)
	}

	judge, err := newConfiguredPropositionJudge(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := judge.Close(); err != nil {
			t.Error(err)
		}
	}()
	if judge.model != "librarian-model" || judge.effort != "medium" || judge.group != "library" ||
		len(judge.candidates) != 2 || judge.candidates[0].Timeout != 20*time.Second {
		t.Fatalf("configured judge = %#v", judge)
	}
	if judge.embedding.provider == nil || judge.embedding.model != "embed-model" || judge.embedding.dimensions != 3 {
		t.Fatalf("embedding = %#v", judge.embedding)
	}
}

type fakePropositionLookup struct {
	searches []PropositionSearchRequest
	gets     []string
	hits     []PropositionSearchHit
	detail   Proposition
	err      error
}

func (f *fakePropositionLookup) SearchPropositions(_ context.Context, request PropositionSearchRequest) (PropositionSearchResponse, error) {
	f.searches = append(f.searches, request)
	return PropositionSearchResponse{Hits: f.hits}, f.err
}

func (f *fakePropositionLookup) GetProposition(_ context.Context, id string) (Proposition, error) {
	f.gets = append(f.gets, id)
	return f.detail, f.err
}

func propositionToolMessage(name, arguments string) client.Message {
	return client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
		ID: "call-" + name, Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: name, Arguments: arguments},
	}}}
}

func TestModelPropositionJudgeSearchesThenReadsAndResolvesNewCandidate(t *testing.T) {
	configured := &fakePropositionJudgeClient{messages: []client.Message{
		propositionToolMessage("search_propositions", `{"query":"Q earlier database decision"}`),
		propositionToolMessage("get_proposition", `{"id":"found"}`),
		propositionToolMessage("resolve_proposition", `{"action":"merge","target_id":"found","reason":"same scoped decision"}`),
	}}
	lookup := &fakePropositionLookup{
		hits:   []PropositionSearchHit{{ID: "found", Content: "Q uses SQLite."}},
		detail: Proposition{ID: "found", Content: "Q uses SQLite.", Refs: []string{"run:evidence"}, Payload: PropositionPayload{ExtractorModel: "thinker"}},
	}
	judge := &modelPropositionJudge{client: configured, model: "librarian"}
	decision, err := judge.JudgePropositionWithRetrieval(t.Context(), PropositionRegisterRequest{
		Content:    "Q stores its data in SQLite.",
		Embeddings: &PropositionEmbeddings{Model: "embedding-omitted", Vectors: [][]float32{{1, 0}}},
	}, []PropositionSearchHit{{ID: "initial", Content: "An unrelated fact."}}, lookup)
	if err != nil || decision.TargetID != "found" || decision.Action != PropositionActionMerge {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
	if len(lookup.searches) != 1 || lookup.searches[0].Query != "Q earlier database decision" ||
		lookup.searches[0].RecencyWeight == nil || *lookup.searches[0].RecencyWeight != 0 || lookup.searches[0].Limit != 5 ||
		!reflect.DeepEqual(lookup.gets, []string{"found"}) {
		t.Fatalf("lookup=%#v", lookup)
	}
	if len(configured.requests) != 3 || len(configured.terminalRequests) != 1 {
		t.Fatalf("model rounds=%d terminal=%d", len(configured.requests), len(configured.terminalRequests))
	}
	for i, request := range configured.requests {
		if len(request.Tools) != 3 || !reflect.DeepEqual(request.Tools, configured.requests[0].Tools) || len(request.Messages) != 2+2*i {
			t.Fatalf("round %d changed tools or lost history: %#v", i, request)
		}
		if i > 0 && request.ConversationID != "conversation-librarian" {
			t.Fatalf("round %d lost conversation: %q", i, request.ConversationID)
		}
	}
	if strings.Contains(configured.requests[0].Messages[1].Content, "embedding-omitted") {
		t.Fatal("proposal vectors entered the model context")
	}
	last := configured.requests[2].Messages[5]
	if last.Name != "get_proposition" || last.ToolCallID != "call-get_proposition" ||
		!strings.Contains(last.Content, "run:evidence") || !strings.Contains(last.Content, `"remaining_gets":4`) {
		t.Fatalf("detail result not delivered: %#v", last)
	}

	// A later registration cannot select a candidate found by an earlier one.
	configured.message = propositionToolMessage("resolve_proposition", `{"action":"merge","target_id":"found","reason":"stale"}`)
	_, err = judge.JudgePropositionWithRetrieval(t.Context(), PropositionRegisterRequest{Content: "Other fact"}, nil, lookup)
	if err == nil || !strings.Contains(err.Error(), "unknown candidate") || configured.requests[3].ConversationID != "" || len(configured.requests[3].Messages) != 2 {
		t.Fatalf("registration reused prior session: err=%v request=%#v", err, configured.requests[3])
	}
}

func TestModelPropositionJudgeBoundsAndRejectsIncompleteResearch(t *testing.T) {
	lookupErr := errors.New("search index unavailable")
	for _, test := range []struct {
		name           string
		message        client.Message
		lookupErr      error
		want           string
		searches, gets int
	}{
		{name: "search limit", message: propositionToolMessage("search_propositions", `{"query":"same query"}`), want: "search budget", searches: 3},
		{name: "detail limit", message: propositionToolMessage("get_proposition", `{"id":"known"}`), want: "detail budget", gets: 5},
		{name: "search failure", message: propositionToolMessage("search_propositions", `{"query":"Q"}`), lookupErr: lookupErr, want: "search index unavailable", searches: 1},
		{name: "detail failure", message: propositionToolMessage("get_proposition", `{"id":"known"}`), lookupErr: lookupErr, want: "search index unavailable", gets: 1},
		{name: "unseen detail", message: propositionToolMessage("get_proposition", `{"id":"invented"}`), want: "unknown candidate"},
		{name: "unseen decision", message: propositionToolMessage("resolve_proposition", `{"action":"merge","target_id":"invented","reason":"guessed"}`), want: "unknown candidate"},
		{name: "empty query", message: propositionToolMessage("search_propositions", `{"query":" "}`), want: "query must contain"},
		{name: "query too long", message: propositionToolMessage("search_propositions", `{"query":"`+strings.Repeat("a", 513)+`"}`), want: "query must contain"},
		{name: "unknown input", message: propositionToolMessage("search_propositions", `{"query":"Q","limit":100}`), want: "unknown field"},
		{name: "trailing JSON", message: propositionToolMessage("search_propositions", `{"query":"Q"} {}`), want: "trailing data"},
		{name: "null arguments", message: propositionToolMessage("get_proposition", `null`), want: "must be an object"},
		{name: "unknown tool", message: propositionToolMessage("delete_proposition", `{"id":"known"}`), want: "unsupported tool"},
		{name: "batch", message: client.Message{ToolCalls: []client.ToolCall{{}, {}}}, want: "one tool exactly once"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configured := &fakePropositionJudgeClient{message: test.message}
			lookup := &fakePropositionLookup{err: test.lookupErr}
			judge := &modelPropositionJudge{client: configured, model: "librarian"}
			_, err := judge.JudgePropositionWithRetrieval(t.Context(), PropositionRegisterRequest{Content: "Fact"}, []PropositionSearchHit{{ID: "known"}}, lookup)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want=%q", err, test.want)
			}
			if len(lookup.searches) != test.searches || len(lookup.gets) != test.gets || len(configured.terminalRequests) != 0 {
				t.Fatalf("searches=%d gets=%d terminal=%d", len(lookup.searches), len(lookup.gets), len(configured.terminalRequests))
			}
		})
	}
}

func TestModelPropositionJudgeFallsBackAfterSearchWithFullHistory(t *testing.T) {
	configured := &fakePropositionJudgeClient{
		messages: []client.Message{
			propositionToolMessage("search_propositions", `{"query":"Q old decision"}`),
			propositionToolMessage("resolve_proposition", `{"action":"merge","target_id":"found","reason":"same fact"}`),
		},
		beforeChat: func(_ context.Context, request client.ChatRequest) error {
			if request.Model == "primary" && len(request.Messages) > 2 {
				return &client.APIError{StatusCode: http.StatusServiceUnavailable, Message: "temporary"}
			}
			return nil
		},
	}
	judge := &modelPropositionJudge{client: configured, model: "primary", group: "library", router: client.NewModelRouter(), candidates: []client.ModelCandidate{{Model: "primary"}, {Model: "secondary"}}}
	decision, err := judge.JudgePropositionWithRetrieval(t.Context(), PropositionRegisterRequest{Content: "Fact"}, nil,
		&fakePropositionLookup{hits: []PropositionSearchHit{{ID: "found", Content: "Fact"}}})
	if err != nil || decision.TargetID != "found" || len(configured.requests) != 3 {
		t.Fatalf("decision=%#v error=%v rounds=%d", decision, err, len(configured.requests))
	}
	fallback := configured.requests[2]
	if fallback.Model != "secondary" || fallback.ConversationID != "" || len(fallback.Messages) != 4 || !strings.Contains(fallback.Messages[3].Content, "found") {
		t.Fatalf("fallback lost evidence or reused provider conversation: %#v", fallback)
	}
	if len(configured.terminalRequests) != 1 || configured.terminalRequests[0].Model != "secondary" || configured.terminalRequests[0].ConversationID != "conversation-secondary" {
		t.Fatalf("terminal=%#v", configured.terminalRequests)
	}
}

func TestModelPropositionJudgeHonorsCancellationAndContextBudget(t *testing.T) {
	t.Run("canceled response", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		configured := &fakePropositionJudgeClient{
			message:    propositionToolMessage("search_propositions", `{"query":"Q"}`),
			beforeChat: func(context.Context, client.ChatRequest) error { cancel(); return nil },
		}
		lookup := &fakePropositionLookup{}
		_, err := (&modelPropositionJudge{client: configured}).JudgePropositionWithRetrieval(ctx, PropositionRegisterRequest{}, nil, lookup)
		if !errors.Is(err, context.Canceled) || len(lookup.searches) != 0 {
			t.Fatalf("err=%v searches=%d", err, len(lookup.searches))
		}
	})
	t.Run("oversized retrieval", func(t *testing.T) {
		configured := &fakePropositionJudgeClient{message: propositionToolMessage("search_propositions", `{"query":"Q"}`)}
		_, err := (&modelPropositionJudge{client: configured}).JudgePropositionWithRetrieval(t.Context(), PropositionRegisterRequest{}, nil,
			&fakePropositionLookup{hits: []PropositionSearchHit{{ID: "big", Content: strings.Repeat("x", maximumPropositionJudgeBytes)}}})
		if err == nil || !strings.Contains(err.Error(), "context byte budget") || len(configured.requests) != 1 {
			t.Fatalf("err=%v model rounds=%d", err, len(configured.requests))
		}
	})
	t.Run("oversized input", func(t *testing.T) {
		configured := &fakePropositionJudgeClient{}
		_, err := (&modelPropositionJudge{client: configured}).JudgePropositionWithRetrieval(t.Context(), PropositionRegisterRequest{},
			[]PropositionSearchHit{{Content: strings.Repeat("x", maximumPropositionJudgeBytes)}}, &fakePropositionLookup{})
		if err == nil || !strings.Contains(err.Error(), "context byte budget") || len(configured.requests) != 0 {
			t.Fatalf("err=%v model rounds=%d", err, len(configured.requests))
		}
	})
}

func TestModelPropositionJudgeEmbedsRewrittenQuery(t *testing.T) {
	var input client.EmbeddingRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0,0]}]}`))
	}))
	defer server.Close()
	embedder, err := client.New(client.Config{BaseURL: server.URL, DisableAPIKey: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = embedder.Close() }()
	configured := &fakePropositionJudgeClient{messages: []client.Message{
		propositionToolMessage("search_propositions", `{"query":"rewritten query"}`),
		propositionToolMessage("resolve_proposition", `{"action":"create","target_id":"","reason":"distinct"}`),
	}}
	lookup := &fakePropositionLookup{}
	judge := &modelPropositionJudge{client: configured, embedding: embeddingClientConfig{provider: embedder, model: "embed", dimensions: 3}}
	_, err = judge.JudgePropositionWithRetrieval(t.Context(), PropositionRegisterRequest{}, nil, lookup)
	if err != nil || len(lookup.searches) != 1 || !reflect.DeepEqual(lookup.searches[0].Embedding, []float32{1, 0, 0}) || input.Model != "embed" {
		t.Fatalf("err=%v lookup=%#v embedding input=%#v", err, lookup, input)
	}
}

func TestModelPropositionJudgeUsesFreshStrictDecisionSession(t *testing.T) {
	configured := &fakePropositionJudgeClient{message: client.Message{
		Role: client.RoleAssistant,
		ToolCalls: []client.ToolCall{{
			ID: "decision", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{
				Name:      "resolve_proposition",
				Arguments: `{"action":"merge","target_id":"prop-existing","reason":"same truth condition"}`,
			},
		}},
	}}
	judge := &modelPropositionJudge{client: configured, model: "librarian-model", effort: "high"}
	decision, err := judge.JudgeProposition(context.Background(), PropositionRegisterRequest{
		Content: "The Library uses a durable queue.", Confidence: 0.9,
	}, []PropositionSearchHit{{
		ID: "prop-existing", Content: "Library registration is durably queued.", Score: 0.91,
	}})
	if err != nil || decision.Action != PropositionActionMerge || decision.TargetID != "prop-existing" {
		t.Fatalf("decision = %#v, err = %v", decision, err)
	}
	if len(configured.requests) != 1 {
		t.Fatalf("requests = %d", len(configured.requests))
	}
	if len(configured.terminalRequests) != 1 {
		t.Fatalf("decision result was not returned: %#v", configured.terminalRequests)
	}
	terminal := configured.terminalRequests[0]
	last := terminal.Messages[len(terminal.Messages)-1]
	if last.ToolCallID != "decision" || last.Name != "resolve_proposition" || !strings.Contains(last.Content, `"target_id":"prop-existing"`) {
		t.Fatalf("decision result = %#v", last)
	}
	request := configured.requests[0]
	if request.Model != "librarian-model" || request.ReasoningEffort != "high" ||
		request.ToolChoice != client.ToolChoiceRequired || request.ParallelToolCalls == nil || *request.ParallelToolCalls ||
		len(request.Messages) != 2 || !strings.Contains(request.Messages[1].Content, `"score":0.91`) {
		t.Fatalf("judge request = %#v", request)
	}
}

func TestModelPropositionJudgeFallsBackOnTransientModelFailure(t *testing.T) {
	configured := &fakePropositionJudgeClient{
		message: client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
			ID: "decision", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: "resolve_proposition", Arguments: `{"action":"create","target_id":"","reason":"distinct"}`},
		}}},
		errors: map[string]error{
			"primary": &client.APIError{StatusCode: http.StatusServiceUnavailable, Message: "temporary"},
		},
	}
	judge := &modelPropositionJudge{
		client: configured, model: "primary", group: "library", router: client.NewModelRouter(),
		candidates: []client.ModelCandidate{{Model: "primary"}, {Model: "secondary"}},
	}
	decision, err := judge.JudgeProposition(t.Context(), PropositionRegisterRequest{Content: "A distinct fact."}, nil)
	if err != nil || decision.Action != PropositionActionCreate {
		t.Fatalf("decision = %#v, err = %v", decision, err)
	}
	if len(configured.requests) != 2 || configured.requests[0].Model != "primary" || configured.requests[1].Model != "secondary" {
		t.Fatalf("requests = %#v", configured.requests)
	}
	if len(configured.terminalRequests) != 1 || configured.terminalRequests[0].Model != "secondary" {
		t.Fatalf("terminal result must stay on the selected provider: %#v", configured.terminalRequests)
	}
}

func TestModelPropositionJudgePropagatesTerminalDeliveryFailure(t *testing.T) {
	failure := errors.New("terminal delivery failed")
	configured := &fakePropositionJudgeClient{terminalErr: failure, message: client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
		ID: "decision", Function: client.FunctionCall{Name: "resolve_proposition", Arguments: `{"action":"create","target_id":"","reason":"distinct"}`},
	}}}}
	_, err := (&modelPropositionJudge{client: configured, model: "librarian"}).JudgeProposition(t.Context(), PropositionRegisterRequest{Content: "A fact."}, nil)
	if !errors.Is(err, failure) || len(configured.terminalRequests) != 1 {
		t.Fatalf("err=%v terminalRequests=%d", err, len(configured.terminalRequests))
	}
}

func TestModelPropositionJudgeDoesNotFallbackOnStructuredResponseFailure(t *testing.T) {
	configured := &fakePropositionJudgeClient{message: client.Message{Role: client.RoleAssistant, Content: "plain text"}}
	judge := &modelPropositionJudge{
		client: configured, model: "primary", group: "library", router: client.NewModelRouter(),
		candidates: []client.ModelCandidate{{Model: "primary"}, {Model: "secondary"}},
	}
	_, err := judge.JudgeProposition(t.Context(), PropositionRegisterRequest{Content: "A fact."}, nil)
	if err == nil || !strings.Contains(err.Error(), "exactly once") {
		t.Fatalf("error = %v", err)
	}
	if len(configured.requests) != 1 || configured.requests[0].Model != "primary" {
		t.Fatalf("requests = %#v", configured.requests)
	}
}
