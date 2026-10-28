package studio

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
)

func TestStudioACPModelsSwitchInPlaceAndPersistUsage(t *testing.T) {
	for _, resume := range []bool{true, false} {
		t.Run(fmt.Sprint(resume), func(t *testing.T) {
			fixture := newStudioIntegration(t)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(t.TempDir(), "prompts.jsonl")
			env := map[string]string{"Q_STUDIO_ACP_FIXTURE": "1", "Q_STUDIO_ACP_LOG": log}
			if !resume {
				env["Q_STUDIO_ACP_NO_RESUME"] = "1"
			}
			response := serveJSON(t, fixture.handler, http.MethodPut, "/api/v1/settings/acp", map[string]any{"connections": map[string]config.AgentConnectionConfig{
				"fixture": {Command: executable, Env: env}, "no-models": {Command: executable, Env: map[string]string{"Q_STUDIO_ACP_FIXTURE": "1", "Q_STUDIO_ACP_NO_MODELS": "1"}},
			}})
			if response.Code != 200 {
				t.Fatal(response.Body.String())
			}
			base := "/api/v1/sessions/" + fixture.session.SessionID
			selectAgent := func(agent string) {
				t.Helper()
				response := serveJSON(t, fixture.handler, http.MethodPut, base+"/model", map[string]string{"workspace_root": fixture.root, "agent": agent})
				if response.Code != 200 {
					t.Fatalf("select ACP: %d %s", response.Code, response.Body.String())
				}
			}
			modelRequest := func(method, model string) app.ACPModelSettings {
				t.Helper()
				response := serveJSON(t, fixture.handler, method, base+"/acp/model", map[string]string{"workspace_root": fixture.root, "model": model})
				if response.Code != 200 {
					t.Fatalf("ACP models: %d %s", response.Code, response.Body.String())
				}
				var result app.ACPModelSettings
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			selectAgent("fixture")
			reasoningRequest := func(effort string) app.ACPModelSettings {
				t.Helper()
				response := serveJSON(t, fixture.handler, http.MethodPut, base+"/acp/model", map[string]string{"workspace_root": fixture.root, "reasoning_effort": effort})
				if response.Code != 200 {
					t.Fatalf("ACP reasoning: %d %s", response.Code, response.Body.String())
				}
				var result app.ACPModelSettings
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			initial := modelRequest(http.MethodPost, "")
			if !initial.Supported || initial.CurrentModel != "fixture-a" || len(initial.Models) != 3 {
				t.Fatalf("models = %+v", initial)
			}
			if !initial.ReasoningSupported || initial.CurrentReasoningEffort != "medium" || len(initial.ReasoningEfforts) != 3 {
				t.Fatalf("reasoning options = %+v", initial)
			}
			reasoningRequest("low")
			changed := modelRequest(http.MethodPut, "fixture-b")
			if changed.CurrentReasoningEffort != "medium" || len(changed.ReasoningEfforts) != 2 {
				t.Fatalf("model-specific efforts = %+v", changed)
			}
			reasoningRequest("high")
			invalidPair := serveJSON(t, fixture.handler, http.MethodPut, base+"/acp/model", map[string]string{"workspace_root": fixture.root, "model": "fixture-a", "reasoning_effort": "unknown"})
			if invalidPair.Code != 422 {
				t.Fatalf("invalid effort accepted: %d %s", invalidPair.Code, invalidPair.Body.String())
			}
			if rolledBack := modelRequest(http.MethodPost, ""); rolledBack.CurrentModel != "fixture-b" || rolledBack.CurrentReasoningEffort != "high" {
				t.Fatalf("failed pair was not rolled back: %+v", rolledBack)
			}
			if changed.CurrentModel != "fixture-b" {
				t.Fatalf("change = %+v", changed)
			}
			for _, model := range []string{"unknown", "rejected"} {
				response := serveJSON(t, fixture.handler, http.MethodPut, base+"/acp/model", map[string]string{"workspace_root": fixture.root, "model": model})
				if response.Code < 400 {
					t.Fatalf("accepted %q: %s", model, response.Body.String())
				}
			}
			selection, err := fixture.session.LoadSessionModelConfig()
			if err != nil || selection.AgentModel != "fixture-b" || selection.AgentReasoningEffort != "high" {
				t.Fatalf("saved model = %+v, %v", selection, err)
			}
			server := httptest.NewServer(fixture.handler)
			defer server.Close()
			endpoint := func(id string) string {
				return server.URL + base + "/runs/" + id + "/events?workspace_root=" + url.QueryEscape(fixture.root)
			}
			run := func(prompt string) *app.SessionEvent {
				t.Helper()
				started := fixture.start(t, prompt)
				page := waitStudioPage(t, endpoint(started.ID), func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
				if page.Run.Status != "completed" {
					t.Fatalf("run = %+v", page.Run)
				}
				for _, event := range page.Events {
					if event.Event.Type == "result" {
						return &event.Event
					}
				}
				t.Fatal("no result")
				return nil
			}
			result := run("model")
			if result.Content != "ACP model: fixture-b" || result.Usage == nil || result.Usage.InputTokens != 100 || result.Usage.CachedTokens == nil || *result.Usage.CachedTokens != 60 {
				t.Fatalf("result = %+v", result)
			}
			run("second")
			selectAgent("fixture") // Reopen; the requested model must be reapplied.
			if reopened := modelRequest(http.MethodPost, ""); reopened.CurrentModel != "fixture-b" || reopened.CurrentReasoningEffort != "high" {
				t.Fatalf("restored model = %+v", reopened)
			}
			if result := run("model"); result.Content != "ACP model: fixture-b" {
				t.Fatalf("reopened response = %+v", result)
			}
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
			if len(calls) != 3 || calls[0]["session"] != calls[1]["session"] || calls[0]["model"] != "fixture-b" || calls[0]["reasoning_effort"] != "high" || calls[2]["reasoning_effort"] != "high" {
				t.Fatalf("model switch reopened the session: %+v", calls)
			}
			if resume && calls[0]["session"] != calls[2]["session"] {
				t.Fatalf("ACP session was not resumed: %+v", calls)
			}
			saved, err := fixture.session.Load()
			if err != nil || len(saved.ResponseUsage) != 3 || *saved.ResponseUsage[0].CachedTokens != 60 {
				t.Fatalf("saved usage = %+v, %v", saved.ResponseUsage, err)
			}
			if result := run("uncached"); result.Usage == nil || result.Usage.CachedTokens == nil || *result.Usage.CachedTokens != 0 {
				t.Fatalf("zero cache = %+v", result)
			}
			if result := run("unknown cache"); result.Usage == nil || result.Usage.CachedTokens != nil {
				t.Fatalf("unknown cache = %+v", result)
			}
			if result := run("no usage"); result.Usage != nil {
				t.Fatalf("invented usage = %+v", result)
			}
			waiting := fixture.start(t, "wait")
			waitStudioPage(t, endpoint(waiting.ID), func(page studioRunPage) bool { return page.Run.Status == "running" })
			busy := serveJSON(t, fixture.handler, http.MethodPut, base+"/acp/model", map[string]string{"workspace_root": fixture.root, "model": "fixture-a"})
			if busy.Code != 409 {
				t.Fatalf("changed model during a prompt: %d %s", busy.Code, busy.Body.String())
			}
			serveJSON(t, fixture.handler, http.MethodPost, base+"/runs/"+waiting.ID+"/commands", studioRunCommandRequest{WorkspaceRoot: fixture.root, Action: "cancel"})
			waitStudioPage(t, endpoint(waiting.ID), func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
			selectAgent("no-models")
			if unsupported := modelRequest(http.MethodPost, ""); unsupported.Supported || len(unsupported.Models) != 0 {
				t.Fatalf("unsupported models = %+v", unsupported)
			}
			selection, err = fixture.session.LoadSessionModelConfig()
			if err != nil || selection.AgentModel != "" || selection.AgentReasoningEffort != "" {
				t.Fatalf("provider change retained a foreign model: %+v, %v", selection, err)
			}
		})
	}
}
