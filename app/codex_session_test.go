package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/workspace"
)

// This subprocess speaks the actual app-server stdio protocol. Its rollout
// files survive a provider restart; no external Codex account or model is used.
func TestCodexSessionServerProcess(t *testing.T) {
	directory := os.Getenv("Q_TEST_CODEX_SESSION_SERVER")
	if directory == "" {
		t.Skip("app-server subprocess helper")
	}
	encoder := json.NewEncoder(os.Stdout)
	decoder := json.NewDecoder(os.Stdin)
	turn := 0
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if err := decoder.Decode(&request); err != nil {
			if err == io.EOF {
				os.Exit(0)
			}
			t.Fatal(err)
		}
		trace, err := os.OpenFile(filepath.Join(directory, "requests.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		err = json.NewEncoder(trace).Encode(request)
		closeErr := trace.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("trace: %v %v", err, closeErr)
		}
		if len(request.ID) == 0 {
			continue
		}
		result := any(map[string]any{})
		var rpcError any
		switch request.Method {
		case "initialize":
			result = map[string]any{"userAgent": "Q test"}
		case "model/list":
			result = map[string]any{"data": []any{map[string]any{"id": "test-model", "model": "test-model", "displayName": "Test", "isDefault": true}}}
		case "config/read":
			result = map[string]any{"config": map[string]any{}}
		case "thread/start":
			if request.Params["ephemeral"] != false {
				t.Fatal("thread/start must be durable")
			}
			file, err := os.CreateTemp(directory, "thread-")
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			result = map[string]any{"thread": map[string]any{"id": filepath.Base(file.Name())}}
		case "thread/resume":
			id := request.Params["threadId"].(string)
			if _, err := os.Stat(filepath.Join(directory, id)); err != nil {
				rpcError = map[string]any{"code": -32600, "message": "no rollout found for thread id " + id}
			} else {
				result = map[string]any{"thread": map[string]any{"id": id}}
			}
		case "thread/inject_items":
		case "turn/start":
			turn++
			result = map[string]any{"turn": map[string]any{"id": fmt.Sprintf("turn-%d", turn), "status": "inProgress"}}
		default:
			t.Fatalf("unexpected method %s", request.Method)
		}
		response := map[string]any{"id": request.ID, "result": result}
		if rpcError != nil {
			delete(response, "result")
			response["error"] = rpcError
		}
		if err := encoder.Encode(response); err != nil {
			t.Fatal(err)
		}
		if request.Method == "turn/start" {
			params := map[string]any{"threadId": request.Params["threadId"], "turnId": fmt.Sprintf("turn-%d", turn), "itemId": "answer"}
			params["item"] = map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer"}
			if err := encoder.Encode(map[string]any{"method": "item/started", "params": params}); err != nil {
				t.Fatal(err)
			}
			delete(params, "item")
			params["delta"] = "Codex answer"
			if err := encoder.Encode(map[string]any{"method": "item/agentMessage/delta", "params": params}); err != nil {
				t.Fatal(err)
			}
			if err := encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": request.Params["threadId"], "turn": map[string]any{"id": fmt.Sprintf("turn-%d", turn), "status": "completed"}}}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

type codexChatOnlyClient struct{ chatClient }

func TestCodexSessionResumesAcrossRestartsAndReplacesMissingThread(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			store := workspace.Store{Root: t.TempDir()}
			settings := config.Store{Dir: t.TempDir()}
			routes := gateway.Config{Providers: []gateway.ProviderConfig{{ID: "account", Prefix: "custom", Type: "codex", Enabled: true, Codex: gateway.CodexConfig{Command: executable, Args: []string{"-test.run=^TestCodexSessionServerProcess$"}, Environment: map[string]string{"Q_TEST_CODEX_SESSION_SERVER": directory}}}}}
			value := config.Default()
			value.Provider.Model = "custom/test-model"
			start := func() (model, func()) {
				t.Helper()
				instance, err := gateway.NewContext(t.Context(), providerhost.LocalConfig(routes, settings.Dir))
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(instance.Handler())
				configured, err := client.New(client.Config{BaseURL: server.URL + "/v1", DefaultModel: value.Provider.Model, DisableAPIKey: true})
				if err != nil {
					t.Fatal(err)
				}
				var connection chatClient = configured
				if !streaming {
					connection = codexChatOnlyClient{configured}
				}
				m := newModel(t.Context(), settings, nil)
				m.workspaceStore, m.gatewayConfig, m.toolRuntime = &store, routes, &fakeAgentTools{}
				m.enterChat(value, connection)
				cleanup := sync.OnceFunc(func() {
					_ = configured.Close()
					server.Close()
					if err := instance.Close(); err != nil {
						t.Error(err)
					}
				})
				t.Cleanup(cleanup)
				return m, cleanup
			}
			assertSaved := func(m model) string {
				t.Helper()
				saved, err := store.Load()
				if err != nil {
					t.Fatal(err)
				}
				if m.turnErr != nil || saved.ResponseAffinity == nil || saved.ResponseAffinity.Model != value.Provider.Model || saved.ResponseAffinity.Key != m.conversationID || !strings.HasPrefix(m.conversationID, "thread-") {
					t.Fatalf("thread not saved: %v %+v %q", m.turnErr, saved.ResponseAffinity, m.conversationID)
				}
				return saved.ResponseAffinity.Key
			}
			m, closeRuntime := start()
			m = runTerminalTestTurn(t, m, "first question")
			first := assertSaved(m)
			closeRuntime()
			m, closeRuntime = start()
			if m.conversationID != first {
				t.Fatalf("restored %q, want %q", m.conversationID, first)
			}
			m = runTerminalTestTurn(t, m, "second question")
			if assertSaved(m) != first {
				t.Fatal("restart replaced a live thread")
			}
			closeRuntime()
			if err := os.Remove(filepath.Join(directory, first)); err != nil {
				t.Fatal(err)
			}
			m, closeRuntime = start()
			m = runTerminalTestTurn(t, m, "third question")
			replacement := assertSaved(m)
			if replacement == first {
				t.Fatal("missing thread was not replaced")
			}
			closeRuntime()
			m, closeRuntime = start()
			if m.conversationID != replacement {
				t.Fatal("replacement ID was not restored")
			}
			m = runTerminalTestTurn(t, m, "fourth question")
			if assertSaved(m) != replacement {
				t.Fatal("replacement thread was not reused")
			}
			closeRuntime()
			trace, err := os.ReadFile(filepath.Join(directory, "requests.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			starts, resumes, injections := 0, 0, 0
			for _, row := range strings.Split(strings.TrimSpace(string(trace)), "\n") {
				var request struct {
					Method string
					Params map[string]json.RawMessage
				}
				if err := json.Unmarshal([]byte(row), &request); err != nil {
					t.Fatal(err)
				}
				switch request.Method {
				case "thread/start":
					starts++
				case "thread/resume":
					resumes++
				case "thread/inject_items":
					injections++
					if !strings.Contains(string(request.Params["items"]), "first question") || !strings.Contains(string(request.Params["items"]), "second question") {
						t.Fatal("recreated thread lost prior history")
					}
				}
			}
			if starts != 2 || resumes != 3 || injections != 1 {
				t.Fatalf("starts=%d resumes=%d injections=%d", starts, resumes, injections)
			}
		})
	}
}

type checkpointingCodexTools struct {
	fakeAgentTools
	store  workspace.Store
	checks chan error
}

func (r *checkpointingCodexTools) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	saved, err := r.store.Load()
	if err == nil && (saved.ResponseAffinity == nil || saved.ResponseAffinity.Key != "tool-conversation") {
		err = fmt.Errorf("thread ID was not checkpointed before tool execution: %+v", saved.ResponseAffinity)
	}
	r.checks <- err
	return r.fakeAgentTools.Call(ctx, call)
}

func TestCodexThreadCheckpointPrecedesToolExecutionAndClearRemovesIt(t *testing.T) {
	store := workspace.Store{Root: t.TempDir()}
	settings := config.Store{Dir: t.TempDir()}
	runtime := &checkpointingCodexTools{store: store, checks: make(chan error, 1)}
	routes := gateway.Config{Providers: []gateway.ProviderConfig{{ID: "local", Type: "codex-app-server", Enabled: true}}}
	value := config.Default()
	value.Provider.Model = "local/test-model"
	open := func(value config.Config, routes gateway.Config) model {
		m := newModel(t.Context(), settings, nil)
		m.workspaceStore, m.gatewayConfig, m.toolRuntime = &store, routes, runtime
		m.enterChat(value, &toolCallingClient{})
		return m
	}
	m := runTerminalTestTurn(t, open(value, routes), "Create a project")
	if m.turnErr != nil {
		t.Fatal(m.turnErr)
	}
	select {
	case err := <-runtime.checks:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("ordinary tool was not executed")
	}
	changed := value
	changed.Provider.Model = "local/other-model"
	if other := open(changed, routes); other.conversationID != "" {
		t.Fatal("thread crossed model boundary")
	}
	otherRoutes := gateway.Config{Providers: []gateway.ProviderConfig{{ID: "local", Type: "openai-compatible", Enabled: true}}}
	if other := open(value, otherRoutes); other.conversationID != "" {
		t.Fatal("thread crossed provider-type boundary")
	}
	m = open(value, routes)
	if m.conversationID != "tool-conversation" {
		t.Fatal("thread was not restored")
	}
	m.resetConversation()
	if err := m.saveWorkspaceSession(); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load()
	if err != nil || saved.ResponseAffinity != nil || len(saved.Transcript) != 0 {
		t.Fatalf("clear retained thread state: %+v %v", saved.ResponseAffinity, err)
	}
}
