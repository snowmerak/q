package studio

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/change"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/usagelog"
	"github.com/snowmerak/q/workspace"
	"github.com/snowmerak/q/workspacememory"
)

// SessionHost starts Q's Gateway and Loom in the current executable. Let the
// test binary host those real children, with the same stdin-owned lifetime.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == providerhost.ChildCommand || os.Args[1] == loom.ChildCommand) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var err error
		if os.Args[1] == loom.ChildCommand {
			err = loom.RunChild(ctx, os.Stdin, os.Stdout)
		} else {
			flags := flag.NewFlagSet("gateway-test-child", flag.ContinueOnError)
			path := flags.String("config", "", "runtime snapshot")
			err = flags.Parse(os.Args[2:])
			if err == nil {
				go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
				err = providerhost.RunChild(ctx, *path, os.Getenv(providerhost.ChildAPIKeyEnv), providerhost.EncodeReady(json.NewEncoder(os.Stdout)))
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type studioIntegration struct {
	handler  http.Handler
	root     string
	session  workspace.Store
	modelURI string
}

func newStudioIntegration(t *testing.T) studioIntegration {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	store := config.Store{Dir: filepath.Join(home, ".q")}
	port := func() int {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		value := listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if err := (qlibrary.ConfigStore{Dir: store.Dir}).Save(qlibrary.Config{Version: qlibrary.ConfigVersion, Host: "127.0.0.1", Port: port()}); err != nil {
		t.Fatal(err)
	}
	if err := (workspacememory.ConfigStore{Dir: store.Dir}).Save(workspacememory.Config{Version: workspacememory.ConfigVersion, Host: "127.0.0.1", Port: port()}); err != nil {
		t.Fatal(err)
	}
	if err := (usagelog.ConfigStore{Dir: store.Dir}).Save(usagelog.Config{Version: usagelog.ConfigVersion, Host: "127.0.0.1", Port: port()}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(studioTestModel))
	t.Cleanup(upstream.Close)
	value := config.Default()
	value.Provider.Model, value.Provider.BaseURL, value.Provider.APIKeyEnv = "test-model", upstream.URL+"/v1", ""
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	host, err := app.NewSessionHost(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	handler, commits, runs, err := newHandlerRuntime(t.Context(), store, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runs.Close(); err != nil {
			t.Error(err)
		}
		if err := commits.Close(); err != nil {
			t.Error(err)
		}
	})
	root := t.TempDir()
	session, lock, err := workspace.CreateSession(root, "Studio integration")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	return studioIntegration{handler: handler, root: root, session: session, modelURI: upstream.URL + "/v1"}
}

// Only the model is substituted: HTTP, Gateway, SessionHost, tools, event log
// and workspace storage all run normally against disposable local resources.
func studioTestModel(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/models" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"test-model","object":"model"}]}`)
		return
	}
	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	var request struct {
		client.ChatRequest
		Stream bool `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	latestUser := -1
	for i, message := range request.Messages {
		if message.Role == client.RoleUser {
			latestUser = i
		}
	}
	text := ""
	if latestUser >= 0 {
		text = request.Messages[latestUser].TextContent()
	}
	completed := map[string]bool{}
	activeTask := false
	for _, message := range request.Messages {
		if message.Role == client.RoleAssistant {
			for _, call := range message.ToolCalls {
				if call.Function.Name == "task_start" {
					activeTask = true
				}
				if call.Function.Name == "task_complete" {
					activeTask = false
				}
			}
		}
	}
	for _, message := range request.Messages[latestUser+1:] {
		if message.Role == client.RoleAssistant {
			for _, call := range message.ToolCalls {
				completed[call.Function.Name] = true
			}
		}
	}
	// Guidance continues the interrupted lifecycle rather than starting it twice.
	completed["task_start"] = completed["task_start"] || activeTask
	if completed["task_start"] && strings.Contains(text, "fail this turn") {
		http.Error(w, `{"error":{"message":"fixture model failure: connection closed"}}`, http.StatusInternalServerError)
		return
	}
	name, arguments := "task_start", `{"objective":"Exercise Studio runtime"}`
	if completed["task_start"] {
		switch {
		case strings.Contains(text, "delegate controlled work") && !completed["delegate"]:
			name, arguments = "delegate", `{"subagent_name":"builtin/research","prompt":"controlled child"}`
		case strings.Contains(text, "controlled child") && !strings.Contains(text, "finish"):
			select {
			case <-r.Context().Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			name, arguments = "read_file", `{"path":"README.md"}`
		case strings.Contains(text, "controlled"):
			name, arguments = "task_complete", `{"outcome":"succeeded","summary":"Controlled work finished"}`
		case strings.Contains(text, "wait for guidance") && !completed["ask_to_user"]:
			name, arguments = "ask_to_user", `{"question":"Which direction?","choices":[{"id":"blue","label":"Blue"},{"id":"green","label":"Green"}]}`
		case !strings.Contains(text, "wait for guidance") && !completed["write_file"]:
			name, arguments = "write_file", `{"path":"studio-result.txt","content":"created through the real default loop"}`
		default:
			name, arguments = "task_complete", `{"outcome":"succeeded","summary":"## Studio result\n\nCreated **studio-result.txt**.\n\n\u0060\u0060\u0060go\npackage main\n\u0060\u0060\u0060"}`
		}
	}
	message := client.Message{Role: client.RoleAssistant}
	usage := client.Usage{PromptTokens: 2048, CompletionTokens: 96, TotalTokens: 2144}
	if !strings.Contains(text, "unreported cache") {
		usage.PromptDetails = &client.TokenDetails{CachedTokens: 1536}
		if strings.Contains(text, "zero cache") {
			usage.PromptDetails.CachedTokens = 0
		}
	}
	finish := "stop"
	if fmt.Sprint(request.ToolChoice) != string(client.ToolChoiceNone) {
		finish = "tool_calls"
		message.ToolCalls = []client.ToolCall{{ID: "test-" + name, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: name, Arguments: arguments}}}
	}
	if request.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		calls := []map[string]any{}
		for i, call := range message.ToolCalls {
			calls = append(calls, map[string]any{"index": i, "id": call.ID, "type": call.Type, "function": call.Function})
		}
		chunk := map[string]any{"id": "studio-test", "model": "test-model", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": calls}, "finish_reason": finish}}, "usage": usage}
		body, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "studio-test", "model": "test-model", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}, "usage": usage})
}

func (fixture studioIntegration) start(t *testing.T, prompt string) studioRunSnapshot {
	t.Helper()
	response := serveJSON(t, fixture.handler, http.MethodPost, "/api/v1/sessions/"+fixture.session.SessionID+"/messages", map[string]string{"workspace_root": fixture.root, "content": prompt})
	if response.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", response.Code, response.Body.String())
	}
	var run studioRunSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestStudioRealRuntimeGuidanceRedirectsAndPersistsTools(t *testing.T) {
	fixture := newStudioIntegration(t)
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	run := fixture.start(t, "wait for guidance")
	base := server.URL + "/api/v1/sessions/" + fixture.session.SessionID
	query := "?workspace_root=" + url.QueryEscape(fixture.root)
	page := waitStudioPage(t, base+"/runs/"+run.ID+"/events"+query, func(page studioRunPage) bool { return page.Run.Status == "waiting" })
	if page.Run.PendingQuestion == nil {
		t.Fatal("question missing")
	}
	command := serveJSON(t, fixture.handler, http.MethodPost, "/api/v1/sessions/"+fixture.session.SessionID+"/runs/"+run.ID+"/commands", studioRunCommandRequest{WorkspaceRoot: fixture.root, Action: "guidance", Content: "write the requested file"})
	if command.Code != http.StatusOK {
		t.Fatalf("guidance = %d %s", command.Code, command.Body.String())
	}
	page = waitStudioPage(t, base+"/runs/"+run.ID+"/events"+query, func(page studioRunPage) bool { return page.Run.Status == "redirected" })
	nextID := ""
	for _, record := range page.Events {
		if record.Event.Type == "redirect" {
			nextID = record.Event.RunID
		}
	}
	if nextID == "" || nextID == run.ID {
		t.Fatalf("redirect events = %#v", page.Events)
	}
	page = waitStudioPage(t, base+"/runs/"+nextID+"/events"+query, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
	if page.Run.Status != "completed" || page.Run.Outcome != "succeeded" {
		t.Fatalf("guided run = %#v", page.Run)
	}
	body, err := os.ReadFile(filepath.Join(fixture.root, "studio-result.txt"))
	if err != nil || string(body) != "created through the real default loop" {
		t.Fatalf("tool side effect = %q, %v", body, err)
	}
	session, err := fixture.session.Load()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(session.Transcript)
	if !strings.Contains(string(encoded), "write the requested file") || !strings.Contains(string(encoded), "write_file") {
		t.Fatalf("transcript lost guidance/tools: %s", encoded)
	}
	response, err := http.Get(base + "/runs/latest" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	var latest studioRunSnapshot
	if err := json.NewDecoder(response.Body).Decode(&latest); err != nil {
		t.Fatal(err)
	}
	if latest.ID != nextID {
		t.Fatalf("latest = %q, want %q", latest.ID, nextID)
	}
}

func TestStudioRealRuntimeReportsResponseUsage(t *testing.T) {
	fixture := newStudioIntegration(t)
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	run := fixture.start(t, "write the requested file")
	base := server.URL + "/api/v1/sessions/" + fixture.session.SessionID
	query := "?workspace_root=" + url.QueryEscape(fixture.root)
	page := waitStudioPage(t, base+"/runs/"+run.ID+"/events"+query, func(page studioRunPage) bool { return page.Run.Status == "completed" || page.Run.Status == "failed" })
	if page.Run.Status != "completed" {
		t.Fatalf("run failed: %#v", page.Run)
	}
	resultUsageFound := false
	for _, record := range page.Events {
		if record.Event.Type == "result" {
			usage := record.Event.Usage
			resultUsageFound = usage != nil && usage.InputTokens == 2048 && usage.OutputTokens == 96 && usage.CachedTokens != nil && *usage.CachedTokens == 1536
		}
	}
	if !resultUsageFound {
		t.Fatal("result event omitted provider usage")
	}
	saved, err := fixture.session.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.ResponseUsage) < 3 {
		t.Fatalf("tool rounds and final response usage not saved: %#v", saved.ResponseUsage)
	}
	detail := detailFromSession(fixture.root, fixture.session, saved)
	for _, message := range detail.Transcript {
		if message.Role != "assistant" {
			continue
		}
		usage := message.Usage
		if usage == nil || usage.InputTokens != 2048 || usage.OutputTokens != 96 || usage.CachedTokens == nil || *usage.CachedTokens != 1536 {
			t.Fatalf("persisted response usage = %#v", usage)
		}
	}
}

func TestStudioLiveSessionClearAndDeleteBetweenTurns(t *testing.T) {
	fixture := newStudioIntegration(t)
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	base := "/api/v1/sessions/" + fixture.session.SessionID
	query := "?workspace_root=" + url.QueryEscape(fixture.root)
	complete := func(prompt string) {
		t.Helper()
		run := fixture.start(t, prompt)
		page := waitStudioPage(t, server.URL+base+"/runs/"+run.ID+"/events"+query, func(page studioRunPage) bool {
			return page.Run.Status == "completed" || page.Run.Status == "failed"
		})
		if page.Run.Status != "completed" {
			t.Fatalf("turn failed: %#v", page.Run)
		}
	}
	complete("first turn")
	complete("second turn")
	response := serveJSON(t, fixture.handler, http.MethodPost, base+"/clear", workspaceRequest{WorkspaceRoot: fixture.root})
	if response.Code != http.StatusOK {
		t.Fatalf("idle session clear: %d %s", response.Code, response.Body.String())
	}
	complete("after clear")
	saved, err := fixture.session.Load()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(saved.Transcript)
	if strings.Contains(string(encoded), "first turn") || strings.Contains(string(encoded), "second turn") {
		t.Fatal("cleared live state returned in a later turn")
	}
	response = serveJSON(t, fixture.handler, http.MethodDelete, base+query, nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("idle session delete: %d %s", response.Code, response.Body.String())
	}
	if _, err := fixture.session.Load(); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("deleted session still exists: %v", err)
	}
}

func waitStudioPage(t *testing.T, endpoint string, ready func(studioRunPage) bool) studioRunPage {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var page studioRunPage
	for ctx.Err() == nil {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"&after=0&wait_ms=100", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("poll: %v; last run %#v", err, page.Run)
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&page)
		_ = response.Body.Close()
		if response.StatusCode != 200 || decodeErr != nil {
			t.Fatalf("poll = %d, %v", response.StatusCode, decodeErr)
		}
		if ready(page) {
			return page
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("run did not reach expected state: %#v", page.Run)
	return page
}

// Launched only by the opt-in Playwright suite, on a supplied loopback port.
func TestStudioBrowserFixture(t *testing.T) {
	address := os.Getenv("Q_STUDIO_BROWSER_ADDRESS")
	if address == "" {
		t.Skip("launched by npm run test:e2e; browser boundary is not part of go test")
	}
	fixture := newStudioIntegration(t)
	other := t.TempDir()
	gatewayProvider := serveJSON(t, fixture.handler, http.MethodPost, "/api/v1/settings/gateway/providers",
		map[string]any{"id": "default", "type": "openai-compatible", "enabled": true, "base_url": fixture.modelURI})
	if gatewayProvider.Code != http.StatusOK {
		t.Fatalf("gateway fixture = %d %s", gatewayProvider.Code, gatewayProvider.Body.String())
	}
	model := serveJSON(t, fixture.handler, http.MethodPut, "/api/v1/settings/models/default",
		map[string]string{"model": "default/test-model", "reasoning_effort": ""})
	if model.Code != http.StatusOK {
		t.Fatalf("model fixture = %d %s", model.Code, model.Body.String())
	}
	decisions := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": []map[string]string{{"name": "decision-test"}}})
	}))
	t.Cleanup(decisions.Close)
	t.Setenv("Q_STUDIO_DECISION_KEY", "fixture-decision-key")
	decisionURI := decisions.URL + "/v1/systemone"
	configured := serveJSON(t, fixture.handler, http.MethodPut, "/api/v1/settings/system-one/providers/typesafe",
		map[string]string{"id": "typesafe", "uri": decisionURI, "api_key_env": ""})
	if configured.Code != http.StatusOK {
		t.Fatalf("decision fixture = %d %s", configured.Code, configured.Body.String())
	}
	skillDirectory := filepath.Join(fixture.root, ".agents", "skills", "studio-review")
	if err := os.MkdirAll(skillDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte("---\nname: studio-review\ndescription: Review the requested Go package.\ntags: [go, review]\n---\n\nRead the package and report findings.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	globalSkillDirectory := filepath.Join(home, ".agents", "skills", "studio-global-review")
	if err := os.MkdirAll(globalSkillDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalSkillDirectory, "SKILL.md"), []byte("---\nname: studio-global-review\ndescription: Review code in any repository.\n---\n\nRead the code and report findings.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projects := serveJSON(t, fixture.handler, http.MethodPost, "/api/v1/projects", studioProjectUpdateRequest{Name: "Integration", WorkspaceRoots: []string{fixture.root, other}})
	if projects.Code != 201 {
		t.Fatalf("project = %d %s", projects.Code, projects.Body.String())
	}
	projects = serveJSON(t, fixture.handler, http.MethodPost, "/api/v1/projects", studioProjectUpdateRequest{Name: "Solo", WorkspaceRoots: []string{t.TempDir()}})
	if projects.Code != 201 {
		t.Fatalf("project = %d %s", projects.Code, projects.Body.String())
	}
	closed := make(chan struct{})
	var once sync.Once
	projectRoot, projectOther := t.TempDir(), t.TempDir()
	tree := seedStudioBrowserDelegations(t)
	gitRoot, gitOther := seedStudioBrowserChanges(t, 42), seedStudioBrowserChanges(t, 84)
	files, filesWorktree := seedStudioBrowserFiles(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_test/fixture", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{
			"root": fixture.root, "other": other, "session_id": fixture.session.SessionID,
			"project_root": projectRoot, "project_other": projectOther,
			"tree_root": tree.Root, "tree_session_id": tree.SessionID,
			"git_root": gitRoot, "git_other": gitOther,
			"decision_uri": decisionURI,
			"files_root":   files.Root, "files_session_id": files.SessionID, "files_worktree": filesWorktree,
		})
	})
	mux.HandleFunc("POST /_test/shutdown", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204); once.Do(func() { close(closed) }) })
	mux.Handle("/", fixture.handler)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	select {
	case <-closed:
	case <-t.Context().Done():
	}
}

// Seed persisted records through workspace APIs; this fixture checks tree UI
// and deletion, without claiming to execute a model-driven delegation.
func seedStudioBrowserDelegations(t *testing.T) workspace.Store {
	t.Helper()
	root, lock, err := workspace.CreateSession(t.TempDir(), "studio browser tree fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	}()
	session, err := root.Load()
	if err != nil {
		t.Fatal(err)
	}
	session.Title = "Delegation fixture"
	session.Transcript = []client.Message{{Role: client.RoleAssistant, Content: "# Root fixture"}}
	if err := root.Save(session); err != nil {
		t.Fatal(err)
	}
	parent := root
	for _, item := range []struct{ id, agent, content string }{
		{"senior-child", "builtin/senior-developer", "# Senior fixture"},
		{"junior-child", "builtin/junior-developer", "# Junior fixture"},
	} {
		bookmark := workspace.DelegationBookmark{InvocationID: item.id, CallID: item.id,
			Agent: item.agent, Prompt: "Review the fixture", RunID: session.RunID, CreatedAt: time.Now().UTC()}
		if _, err := parent.AddDelegation(bookmark); err != nil {
			t.Fatal(err)
		}
		child, err := parent.ChildStore(item.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Save(workspace.Session{RunID: session.RunID,
			Transcript: []client.Message{{Role: client.RoleAssistant, Content: item.content}}}); err != nil {
			t.Fatal(err)
		}
		if err := child.SaveDelegationState(workspace.DelegationState{Agent: item.agent,
			Prompt: bookmark.Prompt, RunID: session.RunID, Status: "completed",
			Result: &client.ToolResult{Content: item.content}}); err != nil {
			t.Fatal(err)
		}
		parent = child
	}
	external := workspace.DelegationBookmark{InvocationID: "external-child", CallIndex: 1, CallID: "external", Agent: "builtin/web-search", Prompt: "External fixture", RunID: session.RunID}
	if _, err := root.AddDelegation(external); err != nil {
		t.Fatal(err)
	}
	child, err := root.ChildStore(external.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Save(workspace.Session{RunID: session.RunID, Transcript: []client.Message{{Role: client.RoleAssistant, Content: "# External fixture"}}}); err != nil {
		t.Fatal(err)
	}
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: external.Agent, Kind: "external", Prompt: external.Prompt, Status: "completed", Result: &client.ToolResult{Content: "done"}}); err != nil {
		t.Fatal(err)
	}
	return root
}

func seedStudioBrowserChanges(t *testing.T, answer int) string {
	t.Helper()
	root := t.TempDir()
	studioGit(t, root, "init")
	studioGit(t, root, "config", "user.name", "Studio Browser Fixture")
	studioGit(t, root, "config", "user.email", "studio@example.test")
	writeStudioGitFile(t, root, "main.go", fmt.Sprintf("package main\n\nvar answer = %d\n", answer))
	writeStudioGitFile(t, root, "secondary.go", "package main\n\nvar count = 1\n")
	studioGit(t, root, "add", ".")
	studioGit(t, root, "commit", "-m", "chore: fixture")
	writeStudioGitFile(t, root, "main.go", fmt.Sprintf("package main\n\nvar answer  = %d\n", answer))
	writeStudioGitFile(t, root, "secondary.go", "package main\n\nvar count  = 1\n")
	return root
}

func seedStudioBrowserFiles(t *testing.T) (workspace.Store, string) {
	t.Helper()
	root := seedStudioBrowserChanges(t, 128)
	if err := os.Mkdir(filepath.Join(root, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	writeStudioGitFile(t, root, "nested/config.json", "{\"enabled\":true}\n")
	writeStudioGitFile(t, root, "clean.txt", "unchanged content\n")
	writeStudioGitFile(t, root, "deleted.txt", "deleted content\n")
	writeStudioGitFile(t, root, "binary.bin", "\x00\x01\xff")
	writeStudioGitFile(t, root, "large.txt", strings.Repeat("preview line\n", 4500))
	fullLines := []string{"package main", "", "/*"}
	for index := 4; index <= 120; index++ {
		fullLines = append(fullLines, fmt.Sprintf("comment line %d", index))
	}
	fullLines = append(fullLines, "*/", "var tail = 7")
	writeStudioGitFile(t, root, "full.go", strings.Join(fullLines, "\n")+"\n")
	studioGit(t, root, "add", "nested", "clean.txt", "deleted.txt", "binary.bin", "large.txt", "full.go")
	studioGit(t, root, "commit", "-m", "chore: file viewer fixtures")
	fullLines[19] = "changed comment line 20"
	fullLines = append(fullLines[:55], fullLines[56:]...)
	fullLines = append(fullLines[:85], append([]string{"added comment line"}, fullLines[85:]...)...)
	writeStudioGitFile(t, root, "full.go", strings.Join(fullLines, "\n")+"\n")
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	writeStudioGitFile(t, root, "new.txt", "new content\n")
	worktree := t.TempDir()
	studioGit(t, root, "worktree", "add", "-b", "viewer-child", worktree, "HEAD")
	writeStudioGitFile(t, worktree, "main.go", "package main\n\nvar answer = 999\n")
	store, lock, err := workspace.CreateSession(root, "file viewer fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	}()
	session, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	session.Title = "File viewer fixture"
	session.Transcript = []client.Message{{Role: client.RoleAssistant, Content: "# Files root\n[Inspect main](main.go#L3)"}}
	if err := store.Save(session); err != nil {
		t.Fatal(err)
	}
	bookmark := workspace.DelegationBookmark{InvocationID: "viewer-child", CallID: "viewer-child", Agent: "builtin/junior-developer", Prompt: "Inspect files", RunID: session.RunID, WorkingDirectory: root, CreatedAt: time.Now().UTC()}
	if _, err := store.AddDelegation(bookmark); err != nil {
		t.Fatal(err)
	}
	child, err := store.ChildStore(bookmark.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Save(workspace.Session{RunID: session.RunID, Transcript: []client.Message{{Role: client.RoleAssistant, Content: "# Files child\n[Inspect child main](main.go#L3)"}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	base := strings.TrimSpace(studioGit(t, root, "rev-parse", "HEAD"))
	if err := child.SaveDelegationState(workspace.DelegationState{Agent: bookmark.Agent, Prompt: bookmark.Prompt, RunID: session.RunID, Status: "completed", Result: &client.ToolResult{Content: "Files inspected"}, ChangeRequest: &change.Request{Version: change.Version, ID: "viewer-request", RepositoryRoot: root, WorktreePath: worktree, BaseRef: "HEAD", BaseCommit: base, HeadRef: "viewer-child", Status: change.StatusWorking, CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	return store, worktree
}
