package studio

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/coordinator"
	"github.com/snowmerak/q/workspace"
)

func TestReservedRolesNativePromptAndReviewCatalog(t *testing.T) {
	requests := make(chan client.ChatRequest, 32)
	fixture := newStudioIntegrationConfigured(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			var request client.ChatRequest
			if err := json.Unmarshal(body, &request); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			requests <- request
		}
		studioTestModel(w, r)
	}, nil)
	created := serveJSON(t, fixture.handler, http.MethodPost, "/api/v1/projects", studioProjectUpdateRequest{Name: "Roles", WorkspaceRoots: []string{fixture.root}})
	var project studioProject
	if err := json.Unmarshal(created.Body.Bytes(), &project); err != nil || created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s, %v", created.Code, created.Body, err)
	}
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	for _, role := range []string{coordinator.RoleCoordinator, coordinator.RoleIntegrator} {
		response := serveJSON(t, fixture.handler, http.MethodGet, "/api/v1/projects/"+project.ID+"/"+role, nil)
		var registration registeredSessionTree
		if err := json.Unmarshal(response.Body.Bytes(), &registration); err != nil || response.Code != http.StatusOK {
			t.Fatalf("role: %d %s, %v", response.Code, response.Body, err)
		}
		fixture.root = registration.WorkspaceRoot
		fixture.session = workspace.Store{Root: fixture.root, SessionID: registration.Session.SessionID}
		run := fixture.start(t, "inspect the native catalog")
		page := waitStudioPage(t, server.URL+"/api/v1/sessions/"+fixture.session.SessionID+"/runs/"+run.ID+"/events?workspace_root="+url.QueryEscape(fixture.root), func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
		if page.Run.Status != "completed" {
			t.Fatalf("role turn: %+v", page.Run)
		}
		if len(requests) == 0 {
			t.Fatal("native role did not reach the model")
		}
		for len(requests) > 0 {
			request := <-requests
			names := map[string]bool{}
			for _, tool := range request.Tools {
				names[tool.Function.Name] = true
			}
			if !names["ticket_create"] || !names["issue_create"] || names["ticket_review"] != (role == coordinator.RoleIntegrator) || names["manager_inbox"] != (role == coordinator.RoleIntegrator) {
				t.Fatalf("wrong %s native review catalog: %+v", role, names)
			}
			instructions := false
			for _, message := range request.Messages {
				if message.Role == client.RoleSystem && strings.Contains(message.TextContent(), coordinator.RoleInstructions(role)) {
					instructions = true
				}
			}
			if !instructions {
				t.Fatalf("%s role instructions were not sent to the model", role)
			}
		}
	}
}

func TestProjectManagerIdentityClearProtectionAndArchive(t *testing.T) {
	for _, role := range []string{"coordinator", "integrator"} {
		t.Run(role, func(t *testing.T) {
			dir := t.TempDir()
			handler, err := newHandler(config.Store{Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			primary, auxiliary := t.TempDir(), t.TempDir()
			created := serveJSON(t, handler, http.MethodPost, "/api/v1/projects", studioProjectUpdateRequest{Name: "Q", WorkspaceRoots: []string{primary, auxiliary}})
			var project studioProject
			if err := json.Unmarshal(created.Body.Bytes(), &project); err != nil || created.Code != http.StatusCreated {
				t.Fatalf("create: %d %s, %v", created.Code, created.Body, err)
			}
			managerPath := "/api/v1/projects/" + project.ID + "/" + role
			expectedID := project.ManagerSessionID
			if role == "coordinator" {
				expectedID = project.CoordinatorSessionID
			}
			read := func() registeredSessionTree {
				t.Helper()
				response := serveJSON(t, handler, http.MethodGet, managerPath, nil)
				var result registeredSessionTree
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK {
					t.Fatalf("manager: %d %s, %v", response.Code, response.Body, err)
				}
				return result
			}
			manager := read()
			if manager.Kind != role || manager.ProjectID != project.ID || manager.Session.SessionID != expectedID || manager.WorkspaceRoot == primary {
				t.Fatalf("manager address: %+v", manager)
			}
			store := workspace.Store{Root: manager.WorkspaceRoot, SessionID: manager.Session.SessionID}
			value, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveSessionModelConfig(workspace.SessionModelConfig{Agent: "codex", AgentModel: "large"}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store.SessionDir(), workspace.ACPSessionFileName), []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			sessionPath := "/api/v1/sessions/" + store.SessionID
			for _, path := range []string{sessionPath + "?workspace_root=" + url.QueryEscape(store.Root), "/api/v1/registered-sessions/" + manager.RegistrationID} {
				response := serveJSON(t, handler, http.MethodDelete, path, nil)
				if response.Code != http.StatusConflict {
					t.Fatalf("manager deletion: %d %s", response.Code, response.Body)
				}
			}
			cleared := serveJSON(t, handler, http.MethodPost, sessionPath+"/clear", workspaceRequest{WorkspaceRoot: store.Root})
			if cleared.Code != http.StatusOK {
				t.Fatalf("clear: %d %s", cleared.Code, cleared.Body)
			}
			afterClear := read()
			if afterClear.RegistrationID != manager.RegistrationID || afterClear.Session.SessionID != store.SessionID || afterClear.Session.RunID == value.RunID {
				t.Fatalf("clear replaced manager binding: %+v", afterClear)
			}
			selection, err := store.LoadSessionModelConfig()
			if err != nil || selection.AgentModel != "large" || selection.Agent != "codex" {
				t.Fatalf("clear lost ACP selection: %+v, %v", selection, err)
			}
			updated := serveJSON(t, handler, http.MethodPut, "/api/v1/projects/"+project.ID, studioProjectUpdateRequest{Name: "Q renamed", WorkspaceRoots: []string{auxiliary}})
			if updated.Code != http.StatusOK || read().WorkspaceRoot != manager.WorkspaceRoot {
				t.Fatalf("project edit moved manager: %d %s", updated.Code, updated.Body)
			}
			archived := serveJSON(t, handler, http.MethodDelete, "/api/v1/projects/"+project.ID, nil)
			if archived.Code != http.StatusNoContent {
				t.Fatalf("archive: %d %s", archived.Code, archived.Body)
			}
			if _, err := store.Load(); err != nil {
				t.Fatalf("archive deleted manager history: %v", err)
			}
			response := serveJSON(t, handler, http.MethodGet, managerPath, nil)
			if response.Code != http.StatusNotFound {
				t.Fatalf("archived manager remained active: %d %s", response.Code, response.Body)
			}
			response = serveJSON(t, handler, http.MethodGet, "/api/v1/registered-sessions", nil)
			var active registeredSessionsResponse
			if err := json.Unmarshal(response.Body.Bytes(), &active); err != nil || len(active.Sessions) != 0 {
				t.Fatalf("archive leaked an independent manager: %s, %v", response.Body, err)
			}

		})
	}
}

func TestProjectManagerRealRuntimeNativeAndACPSelection(t *testing.T) {
	fixture := newStudioIntegration(t)
	primary := fixture.root
	created := serveJSON(t, fixture.handler, http.MethodPost, "/api/v1/projects", studioProjectUpdateRequest{Name: "Manager runtime", WorkspaceRoots: []string{primary, t.TempDir()}})
	var project studioProject
	if err := json.Unmarshal(created.Body.Bytes(), &project); err != nil || created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s, %v", created.Code, created.Body, err)
	}
	response := serveJSON(t, fixture.handler, http.MethodGet, "/api/v1/projects/"+project.ID+"/manager", nil)
	var manager registeredSessionTree
	if err := json.Unmarshal(response.Body.Bytes(), &manager); err != nil || response.Code != http.StatusOK {
		t.Fatalf("manager: %d %s, %v", response.Code, response.Body, err)
	}
	fixture.root, fixture.session = manager.WorkspaceRoot, workspace.Store{Root: manager.WorkspaceRoot, SessionID: manager.Session.SessionID}
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	base := "/api/v1/sessions/" + fixture.session.SessionID
	complete := func(prompt string) string {
		t.Helper()
		run := fixture.start(t, prompt)
		page := waitStudioPage(t, server.URL+base+"/runs/"+run.ID+"/events?workspace_root="+url.QueryEscape(fixture.root), func(page studioRunPage) bool { return page.Run.Status == "completed" || page.Run.Status == "failed" })
		if page.Run.Status != "completed" {
			t.Fatalf("manager turn: %+v", page.Run)
		}
		var result string
		for _, item := range page.Events {
			if item.Event.Type == "result" {
				result = item.Event.Content
			}
		}
		return result
	}
	complete("write the requested file")
	if body, err := os.ReadFile(filepath.Join(primary, "studio-result.txt")); err != nil || string(body) != "created through the real default loop" {
		t.Fatalf("manager tools did not write in the project: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "studio-result.txt")); !os.IsNotExist(err) {
		t.Fatalf("manager tools wrote in the persistence directory: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	response = serveJSON(t, fixture.handler, http.MethodPut, "/api/v1/settings/acp", map[string]any{"connections": map[string]config.AgentConnectionConfig{"fixture": {Command: executable, Env: map[string]string{"Q_STUDIO_ACP_FIXTURE": "1"}}}})
	if response.Code != http.StatusOK {
		t.Fatalf("ACP setup: %d %s", response.Code, response.Body)
	}
	response = serveJSON(t, fixture.handler, http.MethodPut, base+"/model", map[string]string{"workspace_root": fixture.root, "agent": "fixture"})
	if response.Code != http.StatusOK {
		t.Fatalf("switch provider: %d %s", response.Code, response.Body)
	}
	response = serveJSON(t, fixture.handler, http.MethodPut, base+"/acp/model", map[string]string{"workspace_root": fixture.root, "model": "fixture-b", "reasoning_effort": "high"})
	if response.Code != http.StatusOK {
		t.Fatalf("select ACP model/effort: %d %s", response.Code, response.Body)
	}
	if result := complete("model"); !strings.Contains(result, "ACP model: fixture-b") {
		t.Fatalf("manager ignored ACP model: %s", result)
	}
	response = serveJSON(t, fixture.handler, http.MethodPost, base+"/clear", workspaceRequest{WorkspaceRoot: fixture.root})
	if response.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", response.Code, response.Body)
	}
	if result := complete("reasoning"); !strings.Contains(result, "ACP reasoning: high") {
		t.Fatalf("clear lost effort selection: %s", result)
	}
	response = serveJSON(t, fixture.handler, http.MethodPut, base+"/model", map[string]string{"workspace_root": fixture.root, "model": "default/test-model", "agent": ""})
	if response.Code != http.StatusOK {
		t.Fatalf("switch back to API: %d %s", response.Code, response.Body)
	}
	complete("write the requested file again")
	saved, err := fixture.session.Load()
	if err != nil || saved.ID != project.ManagerSessionID || len(saved.Transcript) < 4 {
		t.Fatalf("provider changes replaced manager history: %+v, %v", saved, err)
	}
}
