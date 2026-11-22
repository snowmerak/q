package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/coordinator"
	"github.com/snowmerak/q/internal/workitemtools"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspace"
)

func testManagerHost(t *testing.T) (*SessionHost, *qlibrary.Client, workitems.Workspace, workspace.Store) {
	t.Helper()
	settings := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	value.Agents.Connections = map[string]config.AgentConnectionConfig{"wire": {Command: executable, Args: []string{"-test.run=^TestManagerACPHelperProcess$", "--", "manager-acp-helper"}}}
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	if err := (qlibrary.ConfigStore{Dir: settings.Dir}).Save(qlibrary.Config{Host: "127.0.0.1", Port: reserveTestPort(t)}); err != nil {
		t.Fatal(err)
	}
	runtime, err := qlibrary.Ensure(t.Context(), settings.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	project, err := runtime.Client().CreateWorkspace(t.Context(), workitems.WorkspaceInput{Name: "Project", WorkspaceRoots: []string{t.TempDir(), t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := coordinator.EnsureManager(t.Context(), settings.Dir, project)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := providerhost.NewManager(t.Context(), providerhost.Store{Dir: settings.Dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	host := &SessionHost{ctx: t.Context(), store: settings, providerReady: true, manager: manager, factory: func(config.Config) (chatClient, error) { return &retainedSessionClient{}, nil }}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	return host, runtime.Client(), project, store
}

func TestManagerNativeContextAndRootChanges(t *testing.T) {
	for _, role := range []string{coordinator.RoleCoordinator, coordinator.RoleIntegrator} {
		t.Run(role, func(t *testing.T) {
			host, library, project, store := testManagerHost(t)
			if role == coordinator.RoleCoordinator {
				var err error
				store, err = coordinator.EnsureCoordinator(t.Context(), host.store.Dir, project)
				if err != nil {
					t.Fatal(err)
				}
			}

			options := SessionOptions{DisableLearning: true, RuntimeKey: "manager-test", RuntimeFactory: func(context.Context, workspace.Store) (AgentToolRuntime, io.Closer, error) {
				return &retainedSessionTools{}, nil, nil
			}}
			if err := host.RunWithOptions(t.Context(), store, store.SessionID, "opinion", options, nil); err != nil {
				t.Fatal(err)
			}
			live := host.liveSessions[liveSessionKey(store, store.SessionID)]
			if !strings.Contains(live.prepared.state.activeConfig().Provider.SystemPrompt, coordinator.RoleInstructions(role)) {
				t.Fatal("reserved role instructions missing from native system prompt")
			}

			root, auxiliary := live.prepared.state.executionRoots()
			if root != project.WorkspaceRoots[0] || len(auxiliary) != 1 || auxiliary[0] != project.WorkspaceRoots[1] {
				t.Fatalf("native execution roots: %s, %v", root, auxiliary)
			}
			saved, err := store.Load()
			if err != nil || len(saved.Transcript) < 2 {
				t.Fatalf("manager did not persist conversation centrally: %+v, %v", saved, err)
			}
			for _, root := range project.WorkspaceRoots {
				if _, err := os.Stat((workspace.Store{Root: root, SessionID: store.SessionID}).Path()); !os.IsNotExist(err) {
					t.Fatalf("manager transcript leaked into project directory: %v", err)
				}
			}
			before, err := host.sessionConfiguration(store, options)
			if err != nil {
				t.Fatal(err)
			}
			project, err = library.UpdateWorkspace(t.Context(), project.ID, workitems.WorkspaceInput{Name: "Renamed", WorkspaceRoots: []string{project.WorkspaceRoots[1]}})
			if err != nil {
				t.Fatal(err)
			}
			after, err := host.sessionConfiguration(store, options)
			if err != nil || before == after {
				t.Fatalf("root changes did not invalidate live runtime: %v", err)
			}
			if err := host.RunWithOptions(t.Context(), store, store.SessionID, "follow-up", options, nil); err != nil {
				t.Fatal(err)
			}
			live = host.liveSessions[liveSessionKey(store, store.SessionID)]
			root, auxiliary = live.prepared.state.executionRoots()
			if root != project.WorkspaceRoots[0] || len(auxiliary) != 0 || live.prepared.store != store {
				t.Fatalf("updated execution moved storage or kept old roots: %s, %v, %+v", root, auxiliary, live.prepared.store)
			}
			if err := library.ArchiveWorkspace(t.Context(), project.ID); err != nil {
				t.Fatal(err)
			}
			if err := host.RunWithOptions(t.Context(), store, store.SessionID, "archived", options, nil); err == nil {
				t.Fatal("archived manager executed another turn")
			}

		})
	}
}

func TestManagerACPOperationHonorsSelectedAgent(t *testing.T) {
	host, _, project, store := testManagerHost(t)
	if err := store.SaveSessionModelConfig(workspace.SessionModelConfig{Agent: "wire"}); err != nil {
		t.Fatal(err)
	}
	host.factory = func(config.Config) (chatClient, error) {
		return nil, fmt.Errorf("selected ACP agent was bypassed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var answer string
	options := SessionOptions{OperationID: "review-one", Control: NewSessionRunControl()}
	review := workitemtools.ReviewAssignment{AttemptID: "test-attempt", OperationID: options.OperationID}
	reviewCtx := workitemtools.WithReviewAssignment(ctx, review)
	if err := host.RunWithOptions(reviewCtx, store, store.SessionID, "review the worker result", options, func(event SessionEvent) error {
		if event.Type == "result" {
			answer = event.Content
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, project.WorkspaceRoots[0]) || !strings.Contains(answer, "role="+coordinator.RoleIntegrator) || !strings.Contains(answer, "review=test-attempt/review-one review-guidance=true") {
		t.Fatalf("ACP operation lost project role or directory: %s", answer)
	}
	live := host.liveSessions[liveSessionKey(store, store.SessionID)]
	if _, ok := live.prepared.client.(*acpRemoteClient); !ok {
		t.Fatal("manager operation did not use ACP")
	}
	if err := host.Run(ctx, store, store.SessionID, "continue the conversation", func(event SessionEvent) error {
		if event.Type == "result" {
			answer = event.Content
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "resumed=true") || !strings.Contains(answer, "role="+coordinator.RoleIntegrator) || !strings.Contains(answer, "review=/ review-guidance=false") {
		t.Fatalf("ACP did not resume with manual review tools: %s", answer)
	}
}

func TestInteractiveACPReceivesSkillsWithoutWorkTools(t *testing.T) {
	host, _, _, _ := testManagerHost(t)
	value, err := host.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	value.BrowserControl.Enabled = true
	value.BrowserControl.Preferred = "ego-browser"
	if err := host.store.Save(value); err != nil {
		t.Fatal(err)
	}
	store, lock, err := workspace.CreateSession(t.TempDir(), "test ACP skills")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionModelConfig(workspace.SessionModelConfig{Agent: "wire"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var answer string
	if err := host.Run(ctx, store, store.SessionID, "find an applicable skill", func(event SessionEvent) error {
		if event.Type == "result" {
			answer = event.Content
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "skills=true work-tools=false skill-guidance=true browser-guidance=false") {
		t.Fatalf("external agent received the wrong MCP capabilities or prompt: %s", answer)
	}
}

func TestManagerACPUsesProjectCWDAndResumesUntilClear(t *testing.T) {
	for _, role := range []string{coordinator.RoleCoordinator, coordinator.RoleIntegrator} {
		t.Run(role, func(t *testing.T) {
			host, library, project, store := testManagerHost(t)
			if role == coordinator.RoleCoordinator {
				var err error
				store, err = coordinator.EnsureCoordinator(t.Context(), host.store.Dir, project)
				if err != nil {
					t.Fatal(err)
				}
			}

			if err := store.SaveSessionModelConfig(workspace.SessionModelConfig{Agent: "wire"}); err != nil {
				t.Fatal(err)
			}
			run := func() string {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				var answer string
				if err := host.Run(ctx, store, store.SessionID, "report your execution directory", func(event SessionEvent) error {
					if event.Type == "result" {
						answer = event.Content
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				return answer
			}
			if answer := run(); !strings.Contains(answer, project.WorkspaceRoots[0]) || !strings.Contains(answer, "resumed=false") || !strings.Contains(answer, "role="+role) {
				t.Fatalf("initial ACP directory: %s", answer)
			}

			live := host.liveSessions[liveSessionKey(store, store.SessionID)]
			remote := live.prepared.client.(*acpRemoteClient)
			if remote.libraryRoleInstructions != coordinator.RoleInstructions(role) {
				t.Fatal("ACP role instructions are not bound to this conversation")
			}
			args := remote.mcpServers[0].Stdio.Args
			hasReviewPermission := false
			for _, arg := range args {
				if arg == "--manager" {
					hasReviewPermission = true
				}
			}
			if hasReviewPermission != (role == coordinator.RoleIntegrator) {
				t.Fatalf("wrong ACP review permission for %s: %v", role, args)
			}
			// Upgrade the role instructions without replacing a saved remote session.
			body, err := os.ReadFile(acpSessionPath(store))
			if err != nil {
				t.Fatal(err)
			}
			var old acpSessionCheckpoint
			if err := json.Unmarshal(body, &old); err != nil {
				t.Fatal(err)
			}
			old.LibraryInstructionsVersion = 5
			body, err = json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(acpSessionPath(store), body, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := host.ReleaseSession(store, store.SessionID); err != nil {
				t.Fatal(err)
			}
			if answer := run(); !strings.Contains(answer, "resumed=true") || !strings.Contains(answer, "role="+role) {
				t.Fatalf("ACP did not resume: %s", answer)
			}
			if err := host.ReleaseSession(store, store.SessionID); err != nil {
				t.Fatal(err)
			}
			if answer := run(); !strings.Contains(answer, "resumed=true") || !strings.Contains(answer, "role=none") {
				t.Fatalf("unchanged role instructions repeated on resume: %s", answer)
			}
			project, err = library.UpdateWorkspace(t.Context(), project.ID, workitems.WorkspaceInput{Name: project.Name, WorkspaceRoots: []string{project.WorkspaceRoots[1]}})
			if err != nil {
				t.Fatal(err)
			}
			if answer := run(); !strings.Contains(answer, project.WorkspaceRoots[0]) || !strings.Contains(answer, "resumed=false") {
				t.Fatalf("ACP kept old execution roots: %s", answer)
			}
			if err := host.ReleaseSession(store, store.SessionID); err != nil {
				t.Fatal(err)
			}
			if _, err := workspace.ResetSession(store.Root, store.SessionID, "clear manager"); err != nil {
				t.Fatal(err)
			}
			if answer := run(); !strings.Contains(answer, "resumed=false") {
				t.Fatalf("clear resumed old ACP state: %s", answer)
			}

		})
	}
}

func TestManagerRequiresLibraryBindingAndAvailableExecutionRoot(t *testing.T) {
	host, _, project, store := testManagerHost(t)
	impostor, err := workspace.EnsureManagerSession(t.Context(), t.TempDir(), project.ID, project.ManagerSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := host.resolveSessionExecution(t.Context(), impostor); err == nil {
		t.Fatal("a copied local marker granted manager execution")
	}
	if err := os.Remove(project.WorkspaceRoots[0]); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := host.resolveSessionExecution(t.Context(), store); err == nil {
		t.Fatal("manager executed without its project directory")
	}
	if value, err := store.Load(); err != nil || value.ID != project.ManagerSessionID {
		t.Fatalf("offline project lost central conversation: %+v, %v", value, err)
	}
}

// A deterministic ACP peer exercises the real stdio transport without sending
// a prompt to a paid provider. It verifies both process CWD and ACP's Cwd field.
func TestManagerACPHelperProcess(t *testing.T) {
	mode := os.Args[len(os.Args)-1]
	if mode != "manager-acp-helper" && mode != "subagent-acp-helper" {
		return
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	cwd, _ := os.Getwd()
	resumed, skills, workTools := false, false, false
	reviewAttempt, reviewOperation := "", ""
	model, effort := "base", "medium"
	efforts := func() []string {
		if model == "deep" {
			return []string{"high", "max"}
		}
		return []string{"low", "medium"}
	}
	options := func() []map[string]any {
		selectOption := func(id, category, current string, choices []string) map[string]any {
			values := make([]map[string]string, 0, len(choices))
			for _, choice := range choices {
				values = append(values, map[string]string{"value": choice, "name": choice})
			}
			return map[string]any{"id": id, "type": "select", "name": category, "category": category, "currentValue": current, "options": values}
		}
		return []map[string]any{
			selectOption("selected-model", "model", model, []string{"base", "fast", "deep"}),
			selectOption("depth", "thought_level", effort, efforts()),
		}
	}
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Cwd        string `json:"cwd"`
				SessionID  string `json:"sessionId"`
				ConfigID   string `json:"configId"`
				Value      string `json:"value"`
				MCPServers []struct {
					Name string   `json:"name"`
					Args []string `json:"args"`
				} `json:"mcpServers"`
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			} `json:"params"`
		}
		if err := decoder.Decode(&request); err != nil {
			os.Exit(0)
		}
		var result any = map[string]any{}
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}}
		case "session/new", "session/load":
			if filepath.Clean(request.Params.Cwd) != filepath.Clean(cwd) {
				os.Exit(2)
			}
			resumed = request.Method == "session/load"
			servers := request.Params.MCPServers
			if mode == "manager-acp-helper" && (len(servers) != 1 || servers[0].Name != "q-library") {
				fmt.Fprintf(os.Stderr, "expected one q-library server, got %+v\n", servers)
				os.Exit(3)
			}
			if len(servers) != 0 {
				skills = true
				workTools = !slices.Contains(servers[0].Args, "--skills-only")
				for i, arg := range servers[0].Args[:len(servers[0].Args)-1] {
					switch arg {
					case "--review-attempt":
						reviewAttempt = servers[0].Args[i+1]
					case "--review-operation":
						reviewOperation = servers[0].Args[i+1]
					}
				}
			}
			setup := map[string]any{"configOptions": options()}
			if !resumed {
				setup["sessionId"] = fmt.Sprintf("remote-%d", os.Getpid())
			}
			result = setup
		case "session/set_config_option":
			switch request.Params.ConfigID {
			case "selected-model":
				model = request.Params.Value
				effort = "medium"
				if model == "deep" {
					effort = "high"
				}
			case "depth":
				if !slices.Contains(efforts(), request.Params.Value) {
					os.Exit(4)
				}
				effort = request.Params.Value
			default:
				os.Exit(5)
			}
			result = map[string]any{"configOptions": options()}
		case "session/prompt":
			role, skillGuidance, browserGuidance, reviewGuidance := "none", false, false, false
			webTester := false
			for _, block := range request.Params.Prompt {
				webTester = webTester || strings.Contains(block.Text, "You are q's isolated external Web Tester")
				skillGuidance = skillGuidance || strings.Contains(block.Text, "search_skills") && strings.Contains(block.Text, "get_skill")
				browserGuidance = browserGuidance || strings.Contains(block.Text, "<q_browser_control>")
				reviewGuidance = reviewGuidance || strings.Contains(block.Text, workitemtools.AssignedReviewInstructions)
				for _, candidate := range []string{coordinator.RoleCoordinator, coordinator.RoleIntegrator} {
					if strings.Contains(block.Text, coordinator.RoleInstructions(candidate)) {
						role = candidate
					}
				}
			}
			text := fmt.Sprintf("cwd=%s resumed=%t role=%s skills=%t work-tools=%t skill-guidance=%t browser-guidance=%t review=%s/%s review-guidance=%t model=%s effort=%s", cwd, resumed, role, skills, workTools, skillGuidance, browserGuidance, reviewAttempt, reviewOperation, reviewGuidance, model, effort)
			if webTester {
				body, _ := json.Marshal(map[string]string{"outcome": "succeeded", "summary": text})
				text = string(body)
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": request.Params.SessionID, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}}})
			result = map[string]any{"stopReason": "end_turn"}
		}
		if len(request.ID) != 0 {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
	}
}
