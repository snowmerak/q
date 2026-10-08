package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/council"
	"github.com/snowmerak/q/worklock"
	"github.com/snowmerak/q/workspace"
)

type councilTestHost struct{}

func (councilTestHost) Run(context.Context, workspace.Store, string, string, app.SessionEventSink) error {
	return nil
}
func (councilTestHost) ReleaseSession(workspace.Store, string) error { return nil }
func (councilTestHost) RunWithOptions(_ context.Context, store workspace.Store, id, prompt string, options app.SessionOptions, emit app.SessionEventSink) error {
	text := "Independent answer"
	if options.Model == "test/chair" {
		text = "Final council answer"
	} else if strings.Contains(prompt, "anonymous peer reviewer") || strings.Contains(prompt, "continuing a multi-round") {
		text = "Peer review"
	}
	return emit(app.SessionEvent{Type: "result", SessionID: id, Content: text})
}

func councilAPI(t *testing.T, handler http.Handler, method, path string, body any, target any) int {
	t.Helper()
	var input *bytes.Reader
	if body == nil {
		input = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		input = bytes.NewReader(encoded)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, path, input))
	if target != nil {
		if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
			t.Fatalf("%s %s returned %d %s: %v", method, path, recorder.Code, recorder.Body.String(), err)
		}
	}
	return recorder.Code
}

func TestStudioIndependentCouncilPersistsWithoutWorkspaceAndRuns(t *testing.T) {
	dir := t.TempDir()
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), config.Store{Dir: dir}, councilTestHost{})
	if err != nil {
		t.Fatal(err)
	}
	defer commits.Close()
	defer sessions.Close()
	input := councilInput{Name: "General", Scope: council.Independent, Members: []council.Seat{{Model: "test/one"}, {Model: "test/two"}}, Chair: council.Seat{Model: "test/chair"}}
	var created council.Council
	if status := councilAPI(t, handler, http.MethodPost, "/api/v1/councils", input, &created); status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}
	if created.WorkspaceRoot != "" || created.ProjectID != "" {
		t.Fatalf("independent council acquired a workspace: %+v", created)
	}
	if created.Rounds != 2 {
		t.Fatalf("default rounds = %d", created.Rounds)
	}
	if _, err := os.Stat(filepath.Join(dir, "council", "independent", created.ID, "council.json")); err != nil {
		t.Fatal(err)
	}
	var turn council.Turn
	if status := councilAPI(t, handler, http.MethodPost, "/api/v1/councils/"+created.ID+"/turns", councilTurnInput{Prompt: "Hello?"}, &turn); status != http.StatusAccepted {
		t.Fatalf("turn status = %d", status)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		councilAPI(t, handler, http.MethodGet, "/api/v1/councils/"+created.ID+"/runs/"+turn.ID, nil, &turn)
		if turn.Status == "completed" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if turn.Status != "completed" || turn.Final != "Final council answer" {
		t.Fatalf("finished turn = %+v", turn)
	}
	var detail councilDetail
	if status := councilAPI(t, handler, http.MethodGet, "/api/v1/councils/"+created.ID, nil, &detail); status != http.StatusOK {
		t.Fatalf("detail status = %d", status)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].Final != turn.Final {
		t.Fatalf("detail = %+v", detail)
	}
	for _, format := range []string{"md", "json"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/councils/"+created.ID+"/export?format="+format, nil))
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Header().Get("Content-Disposition"), "council-"+created.ID+"."+format) {
			t.Fatalf("%s export status/headers = %d, %+v", format, response.Code, response.Header())
		}
		if format == "md" {
			for _, text := range []string{"Hello?", "Independent answer", "Peer review", "Final council answer"} {
				if !strings.Contains(response.Body.String(), text) {
					t.Fatalf("Markdown export omitted %q: %s", text, response.Body.String())
				}
			}
		} else {
			var exported councilDetail
			if err := json.Unmarshal(response.Body.Bytes(), &exported); err != nil || exported.Council.ID != created.ID || len(exported.Turns) != 1 || exported.Turns[0].Final != turn.Final {
				t.Fatalf("JSON export = %+v, %v", exported, err)
			}
		}
	}
	badExport := httptest.NewRecorder()
	handler.ServeHTTP(badExport, httptest.NewRequest(http.MethodGet, "/api/v1/councils/"+created.ID+"/export?format=html", nil))
	if badExport.Code != http.StatusBadRequest {
		t.Fatalf("unsupported export format status = %d", badExport.Code)
	}
	reopened := council.NewStore(dir)
	t.Cleanup(func() { _ = reopened.Close() })
	if loaded, err := reopened.Load(created.ID); err != nil || loaded.Scope != council.Independent {
		t.Fatalf("reopened = %+v, %v", loaded, err)
	}
}

func TestStudioCouncilResumeAndRerunKeepOriginalTurn(t *testing.T) {
	dir := t.TempDir()
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), config.Store{Dir: dir}, councilTestHost{})
	if err != nil {
		t.Fatal(err)
	}
	defer commits.Close()
	defer sessions.Close()
	input := councilInput{Name: "Retry council", Scope: council.Independent, Members: []council.Seat{{Model: "test/one"}, {Model: "test/two"}}, Chair: council.Seat{Model: "test/chair"}}
	var created council.Council
	if status := councilAPI(t, handler, http.MethodPost, "/api/v1/councils", input, &created); status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}
	partial, err := council.NewTurn(created, "Question to retry")
	if err != nil {
		t.Fatal(err)
	}
	partial.Status, partial.Stage, partial.Error = "failed", "opinions", "temporary timeout"
	partial.Responses = []council.Response{{Label: "A", Model: "test/one", Text: "Keep this original answer"}, {Label: "B", Model: "test/two", Error: "temporary timeout"}}
	partial.Rounds = []council.Round{{Number: 1, Responses: partial.Responses}}
	if err := sessions.councils.store.SaveTurn(partial); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/councils/" + created.ID + "/runs/" + partial.ID
	var resumed council.Turn
	if status := councilAPI(t, handler, http.MethodPost, base+"/retry", councilRetryInput{Mode: "resume"}, &resumed); status != http.StatusAccepted || resumed.ID != partial.ID {
		t.Fatalf("resume = %d, %+v", status, resumed)
	}
	readCompleted := func(id string) council.Turn {
		t.Helper()
		var result council.Turn
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			councilAPI(t, handler, http.MethodGet, "/api/v1/councils/"+created.ID+"/runs/"+id, nil, &result)
			if result.Status == "completed" {
				return result
			}
			time.Sleep(15 * time.Millisecond)
		}
		t.Fatalf("turn did not complete: %+v", result)
		return result
	}
	resumed = readCompleted(partial.ID)
	if resumed.Responses[0].Text != "Keep this original answer" || resumed.Responses[1].Text != "Independent answer" || resumed.Final != "Final council answer" {
		t.Fatalf("resume lost saved work: %+v", resumed)
	}
	var rerun council.Turn
	if status := councilAPI(t, handler, http.MethodPost, base+"/retry", councilRetryInput{Mode: "rerun"}, &rerun); status != http.StatusAccepted || rerun.ID == partial.ID || rerun.RerunOf != partial.ID {
		t.Fatalf("rerun = %d, %+v", status, rerun)
	}
	rerun = readCompleted(rerun.ID)
	if rerun.Responses[0].Text != "Independent answer" {
		t.Fatalf("rerun reused the original answer: %+v", rerun)
	}
	var detail councilDetail
	councilAPI(t, handler, http.MethodGet, "/api/v1/councils/"+created.ID, nil, &detail)
	if len(detail.Turns) != 2 || detail.Turns[0].ID != partial.ID || detail.Turns[1].ID != rerun.ID {
		t.Fatalf("rerun replaced history: %+v", detail.Turns)
	}
}

func TestStudioCouncilDoesNotInterruptAnotherWindowRun(t *testing.T) {
	dir := t.TempDir()
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), config.Store{Dir: dir}, councilTestHost{})
	if err != nil {
		t.Fatal(err)
	}
	defer commits.Close()
	defer sessions.Close()
	input := councilInput{Name: "Shared council", Scope: council.Independent, Members: []council.Seat{{Model: "test/one"}, {Model: "test/two"}}, Chair: council.Seat{Model: "test/chair"}}
	var created council.Council
	if status := councilAPI(t, handler, http.MethodPost, "/api/v1/councils", input, &created); status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}
	turn, err := council.NewTurn(created, "Another window is working")
	if err != nil {
		t.Fatal(err)
	}
	turn.Status, turn.Stage = "running", "opinions"
	if err := sessions.councils.store.SaveTurn(turn); err != nil {
		t.Fatal(err)
	}
	lock, err := worklock.AcquireFile(sessions.councils.store.Root, filepath.Join("locks", created.ID+".lock"), "other Studio")
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/councils/" + created.ID
	var observed council.Turn
	if status := councilAPI(t, handler, http.MethodGet, base+"/runs/"+turn.ID, nil, &observed); status != http.StatusOK || observed.Status != "running" {
		t.Fatalf("active cross-window turn = %d, %+v", status, observed)
	}
	if status := councilAPI(t, handler, http.MethodDelete, base, nil, nil); status != http.StatusConflict {
		t.Fatalf("delete while another window runs = %d", status)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if status := councilAPI(t, handler, http.MethodGet, base+"/runs/"+turn.ID, nil, &observed); status != http.StatusOK || observed.Status != "interrupted" {
		t.Fatalf("stopped cross-window turn = %d, %+v", status, observed)
	}
}

func TestStudioCouncilExportIncludesRunningTurnSnapshot(t *testing.T) {
	dir := t.TempDir()
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), config.Store{Dir: dir}, councilTestHost{})
	if err != nil {
		t.Fatal(err)
	}
	defer commits.Close()
	defer sessions.Close()
	input := councilInput{Name: "Live council", Scope: council.Independent, Members: []council.Seat{{Model: "test/one"}, {Model: "test/two"}}, Chair: council.Seat{Model: "test/chair"}}
	var created council.Council
	if status := councilAPI(t, handler, http.MethodPost, "/api/v1/councils", input, &created); status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}
	turn, err := council.NewTurn(created, "Question during run")
	if err != nil {
		t.Fatal(err)
	}
	turn.Status, turn.Stage = "running", "opinions"
	turn.Responses = []council.Response{{Label: "A", Model: "test/one", Text: "First partial answer"}, {Label: "B", Model: "test/two"}}
	turn.Rounds = []council.Round{{Number: 1, Responses: turn.Responses}}
	if err := sessions.councils.store.SaveTurn(turn); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/councils/"+created.ID+"/export?format=md", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "First partial answer") || !strings.Contains(response.Body.String(), "_Pending._") {
		t.Fatalf("running export = %d %s", response.Code, response.Body.String())
	}
}

func TestStudioCouncilCreationSupportsWorkspaceAndProject(t *testing.T) {
	dir := t.TempDir()
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), config.Store{Dir: dir}, councilTestHost{})
	if err != nil {
		t.Fatal(err)
	}
	defer commits.Close()
	defer sessions.Close()
	root := t.TempDir()
	input := councilInput{Name: "Repository", Scope: council.Workspace, WorkspaceRoot: root, Members: []council.Seat{{Model: "test/one"}, {Model: "test/two"}}, Chair: council.Seat{Model: "test/chair"}, Rounds: 3}
	var workspaceCouncil council.Council
	if status := councilAPI(t, handler, http.MethodPost, "/api/v1/councils", input, &workspaceCouncil); status != http.StatusCreated {
		t.Fatalf("workspace create status = %d", status)
	}
	if workspaceCouncil.WorkspaceRoot != root || workspaceCouncil.Rounds != 3 {
		t.Fatalf("workspace council = %+v", workspaceCouncil)
	}
	var project studioProject
	if status := councilAPI(t, handler, http.MethodPost, "/api/v1/projects", studioProjectUpdateRequest{Name: "Research", WorkspaceRoots: []string{root}}, &project); status != http.StatusCreated {
		t.Fatalf("project create status = %d", status)
	}
	input.Name, input.Scope, input.WorkspaceRoot, input.ProjectID = "Project", council.Project, "", project.ID
	var projectCouncil council.Council
	if status := councilAPI(t, handler, http.MethodPost, "/api/v1/councils", input, &projectCouncil); status != http.StatusCreated {
		t.Fatalf("project council create status = %d", status)
	}
	if projectCouncil.ProjectID != project.ID || projectCouncil.WorkspaceRoot != "" {
		t.Fatalf("project council = %+v", projectCouncil)
	}
}

func TestStudioCouncilACPConfigurationPersistsAndValidatesConnections(t *testing.T) {
	dir := t.TempDir()
	settings := config.Default()
	settings.Provider.Model = "test/one"
	settings.Agents.Connections = map[string]config.AgentConnectionConfig{
		"research": {Command: "test-acp"},
		"disabled": {Command: "test-acp", Disabled: true},
	}
	store := config.Store{Dir: dir}
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), store, councilTestHost{})
	if err != nil {
		t.Fatal(err)
	}
	defer commits.Close()
	defer sessions.Close()
	input := councilInput{Name: "Mixed council", Scope: council.Independent,
		Members: []council.Seat{{Model: "test/one"}, {Agent: "research"}}, Chair: council.Seat{Agent: "research"}, Rounds: 3}
	var created council.Council
	if status := councilAPI(t, handler, http.MethodPost, "/api/v1/councils", input, &created); status != http.StatusCreated {
		t.Fatalf("create = %d", status)
	}
	var detail councilDetail
	councilAPI(t, handler, http.MethodGet, "/api/v1/councils/"+created.ID, nil, &detail)
	if detail.Council.Members[1].Agent != "research" || detail.Council.Chair.Agent != "research" {
		t.Fatalf("lost ACP assignments: %+v", detail.Council)
	}
	for _, connection := range []string{"missing", "disabled"} {
		input.Members[1].Agent = connection
		if status := councilAPI(t, handler, http.MethodPost, "/api/v1/councils", input, nil); status != http.StatusUnprocessableEntity {
			t.Fatalf("invalid connection %s accepted: %d", connection, status)
		}
	}
	source, err := council.NewTurn(created, "Retry original ACP settings")
	if err != nil {
		t.Fatal(err)
	}
	source.Status = "failed"
	if err := sessions.councils.store.SaveTurn(source); err != nil {
		t.Fatal(err)
	}
	changed := created
	changed.Members = []council.Seat{{Model: "test/one"}, {Model: "test/two"}}
	changed.Chair = council.Seat{Model: "test/chair"}
	if _, err := sessions.councils.store.Update(changed); err != nil {
		t.Fatal(err)
	}
	var rerun council.Turn
	status := councilAPI(t, handler, http.MethodPost, "/api/v1/councils/"+created.ID+"/runs/"+source.ID+"/retry", councilRetryInput{Mode: "rerun"}, &rerun)
	if status != http.StatusAccepted || rerun.Members[1].Agent != "research" || rerun.Chair.Agent != "research" {
		t.Fatalf("retry did not retain original ACP assignments: %d %+v", status, rerun)
	}

}
