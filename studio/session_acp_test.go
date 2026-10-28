package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
	"github.com/snowmerak/q/workspace"
)

type studioACPFixture struct {
	acp.Agent
	connection *acp.AgentSideConnection
	id         acp.SessionId
	model      string
	effort     string
}

func (agent *studioACPFixture) configOptions() []acp.SessionConfigOption {
	if os.Getenv("Q_STUDIO_ACP_NO_MODELS") == "1" {
		return nil
	}
	options := acp.SessionConfigSelectOptionsUngrouped{{Value: "fixture-a", Name: "Fixture A"}, {Value: "fixture-b", Name: "Fixture B"}, {Value: "rejected", Name: "Rejected model"}}
	result := []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id: "fixture-model", Name: "Model", Type: "select", Category: new(acp.SessionConfigOptionCategoryModel), CurrentValue: acp.SessionConfigValueId(agent.model),
		Options: acp.SessionConfigSelectOptions{Ungrouped: &options},
	}}}
	if os.Getenv("Q_STUDIO_ACP_NO_REASONING") != "1" {
		efforts := acp.SessionConfigSelectOptionsUngrouped{{Value: "medium", Name: "Medium"}, {Value: "high", Name: "High"}}
		if agent.model == "fixture-a" {
			efforts = append(acp.SessionConfigSelectOptionsUngrouped{{Value: "low", Name: "Low"}}, efforts...)
		}
		result = append(result, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
			Id: "fixture-effort", Name: "Reasoning effort", Type: "select", Category: new(acp.SessionConfigOptionCategoryThoughtLevel), CurrentValue: acp.SessionConfigValueId(agent.effort),
			Options: acp.SessionConfigSelectOptions{Ungrouped: &efforts},
		}})
	}
	return result
}

func (agent *studioACPFixture) SetSessionConfigOption(_ context.Context, request acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if request.ValueId == nil || request.ValueId.SessionId != agent.id {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("invalid model request")
	}
	if request.ValueId.Value == "rejected" {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("model change rejected")
	}
	switch request.ValueId.ConfigId {
	case "fixture-model":
		agent.model = string(request.ValueId.Value)
		if agent.model == "fixture-b" && agent.effort == "low" {
			agent.effort = "medium"
		}
	case "fixture-effort":
		agent.effort = string(request.ValueId.Value)
	default:
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("unknown config option")
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: agent.configOptions()}, nil
}

func runStudioACPFixture() {
	agent := &studioACPFixture{}
	agent.connection = acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	<-agent.connection.Done()
}

func (agent *studioACPFixture) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	capabilities := acp.AgentCapabilities{}
	if os.Getenv("Q_STUDIO_ACP_NO_RESUME") != "1" {
		capabilities.SessionCapabilities.Resume = &acp.SessionResumeCapabilities{}
	}
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber, AgentCapabilities: capabilities}, nil
}

func (agent *studioACPFixture) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	agent.id = acp.SessionId(fmt.Sprintf("fixture-%d", os.Getpid()))
	agent.model = "fixture-a"
	agent.effort = "medium"
	return acp.NewSessionResponse{SessionId: agent.id, ConfigOptions: agent.configOptions()}, nil
}

func (agent *studioACPFixture) ResumeSession(_ context.Context, request acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	agent.id = request.SessionId
	agent.model = "fixture-a"
	agent.effort = "medium"
	return acp.ResumeSessionResponse{ConfigOptions: agent.configOptions()}, nil
}

func (agent *studioACPFixture) Cancel(context.Context, acp.CancelNotification) error { return nil }

func (agent *studioACPFixture) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	text := request.Prompt[0].Text.Text
	if path := os.Getenv("Q_STUDIO_ACP_LOG"); path != "" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return acp.PromptResponse{}, err
		}
		_ = json.NewEncoder(file).Encode(map[string]string{"session": string(agent.id), "prompt": text, "model": agent.model, "reasoning_effort": agent.effort})
		_ = file.Close()
	}
	if text == "wait" {
		<-ctx.Done()
		return acp.PromptResponse{}, ctx.Err()
	}
	if text == "permission" {
		kind := acp.ToolKindEdit
		answer, err := agent.connection.RequestPermission(ctx, acp.RequestPermissionRequest{
			SessionId: agent.id, ToolCall: acp.ToolCallUpdate{ToolCallId: "permission", Title: new("Edit fixture file"), Kind: &kind},
			Options: []acp.PermissionOption{{OptionId: "allow", Name: "Allow", Kind: acp.PermissionOptionKindAllowOnce}},
		})
		if err != nil {
			return acp.PromptResponse{}, err
		}
		if answer.Outcome.Selected == nil {
			return acp.PromptResponse{}, fmt.Errorf("permission was not granted")
		}
	}
	if _, current, found := strings.Cut(text, "\n\nCurrent request:\n"); found {
		text = current
	}
	answer := "ACP answer: " + text
	if text == "model" {
		answer = "ACP model: " + agent.model
	}
	if text == "reasoning" {
		answer = "ACP reasoning: " + agent.effort
	}
	err := agent.connection.SessionUpdate(ctx, acp.SessionNotification{SessionId: agent.id, Update: acp.UpdateAgentMessageText(answer)})
	usage := &acp.Usage{InputTokens: 20, OutputTokens: 10, TotalTokens: 110, CachedReadTokens: new(60), CachedWriteTokens: new(20)}
	if text == "uncached" {
		usage.InputTokens = 80
		usage.CachedReadTokens = new(0)
	}
	if text == "unknown cache" {
		usage.InputTokens = 100
		usage.CachedReadTokens = nil
		usage.CachedWriteTokens = nil
	}
	if text == "no usage" {
		usage = nil
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn, Usage: usage}, err
}

func TestStudioACPSessionsStreamResumeAndRequireApproval(t *testing.T) {
	for _, resume := range []bool{true, false} {
		t.Run(fmt.Sprint(resume), func(t *testing.T) {
			fixture := newStudioIntegrationConfigured(t, studioTestModel, func(value *config.Config) {
				value.UseManagedGateway()
				value.Provider.Model = ""
			})
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(t.TempDir(), "prompts.jsonl")
			environment := map[string]string{"Q_STUDIO_ACP_FIXTURE": "1", "Q_STUDIO_ACP_LOG": log}
			if !resume {
				environment["Q_STUDIO_ACP_NO_RESUME"] = "1"
			}
			created := serveJSON(t, fixture.handler, http.MethodPut, "/api/v1/settings/acp", map[string]any{
				"connections": map[string]any{"fixture": map[string]any{"command": executable, "env": environment}},
			})
			if created.Code != http.StatusOK {
				t.Fatalf("ACP settings = %d %s", created.Code, created.Body.String())
			}
			server := httptest.NewServer(fixture.handler)
			defer server.Close()
			base := "/api/v1/sessions/" + fixture.session.SessionID
			query := "?workspace_root=" + url.QueryEscape(fixture.root)
			selectAgent := func() {
				t.Helper()
				response := serveJSON(t, fixture.handler, http.MethodPut, base+"/model", map[string]string{"workspace_root": fixture.root, "agent": "fixture"})
				if response.Code != 200 {
					t.Fatalf("select ACP = %d %s", response.Code, response.Body.String())
				}
			}
			selectAgent()
			var last studioRunPage
			run := func(prompt string) {
				t.Helper()
				started := fixture.start(t, prompt)
				endpoint := server.URL + base + "/runs/" + started.ID + "/events" + query
				if prompt == "permission" {
					waiting := waitStudioPage(t, endpoint, func(page studioRunPage) bool {
						return page.Run.Status == "waiting" || terminalRunStatus(page.Run.Status)
					})
					if waiting.Run.PendingQuestion == nil {
						t.Fatalf("missing approval: %+v", waiting.Run)
					}
					answer := serveJSON(t, fixture.handler, http.MethodPost, base+"/runs/"+started.ID+"/commands", studioRunCommandRequest{WorkspaceRoot: fixture.root, Action: "answer", Answer: "allow", CallID: waiting.Run.PendingQuestion.CallID})
					if answer.Code != 200 {
						t.Fatalf("answer = %d %s", answer.Code, answer.Body.String())
					}
				}
				last = waitStudioPage(t, endpoint, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
				if last.Run.Status != "completed" {
					t.Fatalf("ACP run = %+v", last.Run)
				}
				stream, result := false, false
				for _, event := range last.Events {
					stream = stream || (event.Event.Type == "stream" && strings.Contains(event.Event.Content, "ACP answer:"))
					result = result || (event.Event.Type == "result" && event.Event.Content == "ACP answer: "+prompt)
				}
				if !result || !stream {
					t.Fatalf("missing ACP result: %+v", last.Events)
				}
			}
			run("first")
			run("second")
			selectAgent() // Release the live process, then reopen the same Q session.
			run("third")
			body, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			var calls []map[string]string
			for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
				var call map[string]string
				if err := json.Unmarshal([]byte(line), &call); err != nil {
					t.Fatal(err)
				}
				calls = append(calls, call)
			}
			if len(calls) != 3 || calls[0]["session"] != calls[1]["session"] {
				t.Fatalf("session not retained: %+v", calls)
			}
			if resume && (calls[2]["session"] != calls[0]["session"] || calls[2]["prompt"] != "third") {
				t.Fatalf("session not resumed: %+v", calls)
			}
			if !resume && !strings.Contains(calls[2]["prompt"], "ACP answer: first") {
				t.Fatalf("completed history not restored: %+v", calls)
			}
			run("permission")
			waiting := fixture.start(t, "wait")
			endpoint := server.URL + base + "/runs/" + waiting.ID + "/events" + query
			waitStudioPage(t, endpoint, func(page studioRunPage) bool {
				return page.Run.Status == "running" || terminalRunStatus(page.Run.Status)
			})
			cancelled := serveJSON(t, fixture.handler, http.MethodPost, base+"/runs/"+waiting.ID+"/commands", studioRunCommandRequest{WorkspaceRoot: fixture.root, Action: "cancel"})
			if cancelled.Code != 200 {
				t.Fatalf("cancel = %d %s", cancelled.Code, cancelled.Body.String())
			}
			terminal := waitStudioPage(t, endpoint, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
			if terminal.Run.Status != "cancelled" {
				t.Fatalf("cancelled ACP run = %+v", terminal.Run)
			}
			other, lock, err := workspace.CreateSession(fixture.root, "second ACP session")
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			selected := serveJSON(t, fixture.handler, http.MethodPut, "/api/v1/sessions/"+other.SessionID+"/model", map[string]string{"workspace_root": fixture.root, "agent": "fixture"})
			if selected.Code != 200 {
				t.Fatalf("select other = %d %s", selected.Code, selected.Body.String())
			}
			otherFixture := fixture
			otherFixture.session = other
			otherRun := otherFixture.start(t, "isolated")
			otherEndpoint := server.URL + "/api/v1/sessions/" + other.SessionID + "/runs/" + otherRun.ID + "/events" + query
			otherResult := waitStudioPage(t, otherEndpoint, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
			if otherResult.Run.Status != "completed" {
				t.Fatalf("isolated run = %+v", otherResult.Run)
			}
			body, err = os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(body)), "\n")
			var isolated map[string]string
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &isolated); err != nil {
				t.Fatal(err)
			}
			if isolated["session"] == calls[0]["session"] || isolated["prompt"] != "isolated" {
				t.Fatalf("ACP context leaked: %+v", isolated)
			}
			invalid := serveJSON(t, fixture.handler, http.MethodPut, base+"/model", map[string]string{"workspace_root": fixture.root, "agent": "missing"})
			if invalid.Code != 422 {
				t.Fatalf("unknown ACP accepted: %d", invalid.Code)
			}
		})
	}
}

func TestStudioACPSettingsInitializeWithoutAPIModel(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	initial := serveJSON(t, handler, http.MethodGet, "/api/v1/settings/acp", nil)
	if initial.Code != 200 {
		t.Fatalf("initial ACP settings = %d %s", initial.Code, initial.Body.String())
	}
	saved := serveJSON(t, handler, http.MethodPut, "/api/v1/settings/acp", map[string]any{
		"connections": map[string]config.AgentConnectionConfig{"fixture": {Command: "fixture-agent", Env: map[string]string{"TOKEN": "secret"}}},
	})
	if saved.Code != 200 {
		t.Fatalf("save initial ACP settings = %d %s", saved.Code, saved.Body.String())
	}
	loaded, err := store.Load()
	if err != nil || !loaded.Provider.Managed || loaded.Provider.Model != "" {
		t.Fatalf("ACP-only settings = %+v, %v", loaded, err)
	}
	bound := serveJSON(t, handler, http.MethodPut, "/api/v1/settings/subagents", map[string]any{"bindings": map[string]string{"search": "fixture"}})
	if bound.Code != 200 {
		t.Fatalf("bind ACP = %d %s", bound.Code, bound.Body.String())
	}
	updated := serveJSON(t, handler, http.MethodPut, "/api/v1/settings/acp", map[string]any{
		"connections": map[string]config.AgentConnectionConfig{"fixture": {Command: "updated-agent", Env: map[string]string{"TOKEN": redactedConnectionSecret}}},
	})
	if updated.Code != 200 {
		t.Fatalf("update ACP = %d %s", updated.Code, updated.Body.String())
	}
	loaded, err = store.Load()
	if err != nil || loaded.Agents.Connections["fixture"].Env["TOKEN"] != "secret" || loaded.Agents.Roles["search"].Agent != "fixture" {
		t.Fatalf("connection edit lost bindings or secrets: %+v, %v", loaded.Agents, err)
	}
	models := serveJSON(t, handler, http.MethodGet, "/api/v1/settings/models", nil)
	if models.Code != 200 || !strings.Contains(models.Body.String(), `"models":[]`) {
		t.Fatalf("ACP-only catalog = %d %s", models.Code, models.Body.String())
	}
}
