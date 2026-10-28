package librarymcp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/tools/builtin"
)

type createJudge struct{}

func (createJudge) JudgeProposition(_ context.Context, request library.PropositionRegisterRequest, candidates []library.PropositionSearchHit) (library.PropositionDecision, error) {
	for _, candidate := range candidates {
		if candidate.Content == request.Content {
			return library.PropositionDecision{Action: library.PropositionActionDiscard, TargetID: candidate.ID}, nil
		}
	}
	return library.PropositionDecision{Action: library.PropositionActionCreate}, nil
}

func connect(t *testing.T, client *library.Client) *mcp.ClientSession {
	t.Helper()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	server, err := NewServer(client).Connect(t.Context(), serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(t.Context(), clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(); _ = server.Wait() })
	return session
}

func call(t *testing.T, session *mcp.ClientSession, name string, input any, output any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: input})
	if err != nil {
		t.Fatal(err)
	}
	if output != nil {
		if result.IsError {
			t.Fatalf("%s: %+v", name, result.Content)
		}
		body, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, output); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func TestLibraryToolsLookupAndReviewedLearning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".q")
	skillDir := filepath.Join(dir, "skills", "orchard")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const skillText = "---\nname: orchard\ndescription: Orchard pruning instructions\n---\n\n# Pruning\nRead references/pruning.md.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillText), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "references", "pruning.md"), []byte("Prune dormant branches."), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	runtime, err := library.EnsureWithOptions(t.Context(), library.EnsureOptions{Dir: dir, Config: library.Config{Host: "127.0.0.1", Port: port}, Judge: createJudge{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	session := connect(t, runtime.Client())
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"get_proposition", "get_skill", "request_learning", "search_propositions", "search_skills"}) {
		t.Fatalf("tools: %v", names)
	}
	var skills library.SkillSearchResponse
	call(t, session, "search_skills", map[string]any{"query": "orchard"}, &skills)
	if len(skills.Hits) != 1 || skills.Hits[0].Scope != "global" {
		t.Fatalf("skills: %+v", skills)
	}
	var resource skillOutput
	call(t, session, "get_skill", map[string]any{"id": skills.Hits[0].ID}, &resource)
	if resource.Content != skillText {
		t.Fatalf("skill text: %q", resource.Content)
	}
	call(t, session, "get_skill", map[string]any{"id": skills.Hits[0].ID, "path": "references/pruning.md"}, &resource)
	if resource.Content != "Prune dormant branches." {
		t.Fatalf("resource: %+v", resource)
	}
	request := map[string]any{"content": "The user keeps an orchard.", "queries": []string{"What does the user grow?"}, "refs": []string{"user:confirmed"}, "confidence": 1.0}
	var acknowledged builtin.LearnOutput
	result := call(t, session, "request_learning", request, &acknowledged)
	body, err := json.Marshal(result.StructuredContent)
	if err != nil || string(body) != `{"enqueued":true}` || !acknowledged.Enqueued {
		t.Fatalf("learning acknowledgement: %s, %v", body, err)
	}
	// A fresh bridge retains durable idempotency through the Library service.
	var retry builtin.LearnOutput
	call(t, connect(t, runtime.Client()), "request_learning", request, &retry)
	if !retry.Enqueued {
		t.Fatalf("retry: %+v", retry)
	}
	// Review is asynchronous; lookups become visible after it finishes.
	var propositions library.PropositionSearchResponse
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		call(t, session, "search_propositions", map[string]any{"query": "orchard"}, &propositions)
		if len(propositions.Hits) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("review did not finish")
		case <-ticker.C:
		}
	}
	if len(propositions.Hits) != 1 {
		t.Fatalf("propositions: %+v", propositions)
	}
	var proposition library.Proposition
	call(t, session, "get_proposition", map[string]any{"id": propositions.Hits[0].ID}, &proposition)
	if proposition.Content != request["content"] || len(proposition.Refs) != 1 {
		t.Fatalf("proposition: %+v", proposition)
	}
	if result := call(t, session, "get_skill", map[string]any{"id": skills.Hits[0].ID, "path": "../outside"}, nil); !result.IsError {
		t.Fatal("skill traversal accepted")
	}
	if result := call(t, session, "request_learning", map[string]any{"content": "unsupported", "refs": []string{}, "confidence": 1}, nil); !result.IsError {
		t.Fatal("missing evidence accepted")
	}
	if result := call(t, session, "request_learning", map[string]any{"content": "unsupported", "refs": []string{"  "}, "confidence": 1}, nil); !result.IsError {
		t.Fatal("blank evidence accepted")
	}
	if result := call(t, session, "request_learning", map[string]any{"content": "api_key: sk-thisisasecretcredential123456", "refs": []string{"user:secret"}, "confidence": 1}, nil); !result.IsError {
		t.Fatal("Library credential validation bypassed")
	}
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "execute_command", Arguments: map[string]any{}}); err == nil {
		t.Fatal("non-Library tool accepted")
	}
}

func TestLibraryUnavailableDoesNotPreventMCPInitialization(t *testing.T) {
	session := connect(t, library.NewClient("http://127.0.0.1:1/v1", "", 100*time.Millisecond))
	if listed, err := session.ListTools(t.Context(), nil); err != nil || len(listed.Tools) != 5 {
		t.Fatalf("tools = %+v, %v", listed, err)
	}
	result := call(t, session, "search_propositions", map[string]any{"query": "unavailable"}, nil)
	if !result.IsError || len(result.Content) == 0 {
		t.Fatalf("error result: %+v", result)
	}
	if strings.Contains(Instructions, "task_complete") || strings.Contains(Instructions, "ask_to_user") {
		t.Fatal("Q workflow leaked into Library instructions")
	}
}

func TestLearningReturnsOnlyQueueAcknowledgement(t *testing.T) {
	for _, response := range []struct {
		name     string
		status   int
		body     string
		accepted bool
	}{
		{"accepted", http.StatusAccepted, `{"enqueued":true}`, true},
		{"write failed", http.StatusInternalServerError, `{"error":{"message":"queue write failed"}}`, false},
		{"not accepted", http.StatusOK, `{"enqueued":false}`, false},
	} {
		t.Run(response.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost || request.URL.Path != "/v1/propositions/enqueue" {
					t.Errorf("unexpected registration call: %s %s", request.Method, request.URL.Path)
					http.NotFound(writer, request)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(response.status)
				_, _ = writer.Write([]byte(response.body))
			}))
			defer server.Close()
			session := connect(t, library.NewClient(server.URL+"/v1", "", 0))
			result := call(t, session, "request_learning", map[string]any{"content": "An established decision.", "refs": []string{"user:confirmed"}, "confidence": 1}, nil)
			if result.IsError == response.accepted {
				t.Fatalf("queue acceptance=%v, result=%+v", response.accepted, result)
			}
			if response.accepted {
				body, err := json.Marshal(result.StructuredContent)
				if err != nil || string(body) != `{"enqueued":true}` {
					t.Fatalf("acknowledgement: %s, %v", body, err)
				}
			}
		})
	}
}
