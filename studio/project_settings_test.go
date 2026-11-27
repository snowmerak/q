package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workitems"
)

func TestNewProjectJoinsBuiltinSubagents(t *testing.T) {
	dir := studioCatalogTestDirectory(t, t.TempDir())
	handler, err := newHandler(config.Store{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		subagent.BuiltinManagerID, subagent.BuiltinSeniorDeveloperID,
		subagent.BuiltinJuniorDeveloperID, subagent.BuiltinCodeReviewerID, subagent.BuiltinResearchID,
	}
	readDefaults := func() workitems.ProjectDefaults {
		t.Helper()
		response := serveJSON(t, handler, http.MethodGet, "/api/v1/project-defaults", nil)
		var defaults workitems.ProjectDefaults
		if err := json.Unmarshal(response.Body.Bytes(), &defaults); response.Code != 200 || err != nil {
			t.Fatalf("defaults: %d %s (%v)", response.Code, response.Body.String(), err)
		}
		return defaults
	}
	defaults := readDefaults()
	if !slices.Equal(defaults.AgentIDs, want) {
		t.Fatalf("initial defaults = %v, want %v", defaults.AgentIDs, want)
	}
	createProject := func(name string, initial *[]workitems.MemberInput, expected []string) {
		t.Helper()
		root := t.TempDir()
		response := serveJSON(t, handler, http.MethodPost, "/api/v1/projects", studioProjectUpdateRequest{
			Name: name, WorkspaceRoots: []string{root}, InitialMembers: initial,
		})
		var project studioProject
		if err := json.Unmarshal(response.Body.Bytes(), &project); response.Code != 201 || err != nil {
			t.Fatalf("create project: %d %s (%v)", response.Code, response.Body.String(), err)
		}
		response = serveJSON(t, handler, http.MethodGet, "/api/v1/projects/"+project.ID+"/members", nil)
		var members library.MemberList
		if err := json.Unmarshal(response.Body.Bytes(), &members); response.Code != 200 || err != nil {
			t.Fatalf("members: %d %s (%v)", response.Code, response.Body.String(), err)
		}
		var ids []string
		for _, member := range members.Members {
			if member.DefinitionRoot != project.WorkspaceRoots[0] || member.ExecutionRoot != project.WorkspaceRoots[0] || member.Disabled {
				t.Fatalf("unexpected binding: %+v", member)
			}
			ids = append(ids, member.AgentID)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, slices.Sorted(slices.Values(expected))) {
			t.Fatalf("project members = %v, want %v", ids, expected)
		}
	}
	createProject("Initial built-ins", nil, want)
	createProject("Explicitly empty", &[]workitems.MemberInput{}, nil)

	defaults.AgentIDs = []string{"builtin/interviewer", subagent.BuiltinResearchID}
	if _, err := withProjectCatalog(newStudioProjectStore(dir), func(ctx context.Context, c *library.Client) (workitems.ProjectDefaults, error) {
		return c.UpdateProjectDefaults(ctx, defaults)
	}); err != nil {
		t.Fatal(err)
	}
	defaults = readDefaults()
	if !slices.Equal(defaults.AgentIDs, []string{subagent.BuiltinResearchID}) {
		t.Fatalf("legacy defaults = %v", defaults.AgentIDs)
	}
	createProject("Legacy defaults", nil, defaults.AgentIDs)

	defaults.AgentIDs = []string{}
	response := serveJSON(t, handler, http.MethodPut, "/api/v1/project-defaults", defaults)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	createProject("Saved empty defaults", nil, nil)
}

func TestProjectSettingsAPIValidatesProfilesAndSnapshotsDefaults(t *testing.T) {
	dir := studioCatalogTestDirectory(t, t.TempDir())
	handler, err := newHandler(config.Store{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	response := serveJSON(t, handler, http.MethodGet, "/api/v1/project-options", nil)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var options struct {
		Agents      []projectAgentOption `json:"agents"`
		MaxParallel int                  `json:"max_parallel"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range options.Agents {
		if o.ID == "builtin/research" && o.Available {
			found = true
		}
	}
	if !found || options.MaxParallel <= 0 {
		t.Fatalf("options: %+v", options)
	}
	response = serveJSON(t, handler, http.MethodGet, "/api/v1/project-defaults", nil)
	var defaults workitems.ProjectDefaults
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &defaults); err != nil {
		t.Fatal(err)
	}
	defaults.AgentIDs = []string{"global/missing"}
	if response := serveJSON(t, handler, http.MethodPut, "/api/v1/project-defaults", defaults); response.Code != 400 {
		t.Fatalf("missing profile accepted: %d %s", response.Code, response.Body.String())
	}
	defaults.AgentIDs = []string{"builtin/research"}
	response = serveJSON(t, handler, http.MethodPut, "/api/v1/project-defaults", defaults)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if response := serveJSON(t, handler, http.MethodPut, "/api/v1/project-defaults", defaults); response.Code != 409 {
		t.Fatalf("stale defaults: %d %s", response.Code, response.Body.String())
	}
	root := t.TempDir()
	response = serveJSON(t, handler, http.MethodPost, "/api/v1/projects", studioProjectUpdateRequest{Name: "Defaults", WorkspaceRoots: []string{root}})
	if response.Code != 201 {
		t.Fatal(response.Body.String())
	}
	var project studioProject
	if err := json.Unmarshal(response.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	response = serveJSON(t, handler, http.MethodGet, "/api/v1/projects/"+project.ID+"/members", nil)
	var members library.MemberList
	if err := json.Unmarshal(response.Body.Bytes(), &members); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(members.Members) != 1 || members.Members[0].AgentID != "builtin/research" {
		t.Fatalf("atomic initial snapshot: %+v %s", members, response.Body.String())
	}
	initial := []workitems.MemberInput{{AgentID: "global/missing", DefinitionRoot: root, ExecutionRoot: root}}
	if response := serveJSON(t, handler, http.MethodPost, "/api/v1/projects", studioProjectUpdateRequest{Name: "Invalid", WorkspaceRoots: []string{t.TempDir()}, InitialMembers: &initial}); response.Code != 400 {
		t.Fatalf("bad initial profile: %d %s", response.Code, response.Body.String())
	}
	response = serveJSON(t, handler, http.MethodGet, "/api/v1/projects", nil)
	var catalog studioProjectsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Projects) != 1 {
		t.Fatalf("invalid create left project: %+v", catalog)
	}
}

func TestProjectOptionsKeepQualifiedGlobalAndWorkspaceProfiles(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	main := config.Store{Dir: dir}
	profiles := profileStore(main, root)
	profile := subagent.Profile{Version: 1, Name: "shared", Role: config.AgentRoleResearch, SystemPrompt: "Investigate the request.", Tools: []string{}, Delegates: []string{}}
	for _, scope := range []string{"global", "workspace"} {
		if err := profiles.Save(profile, scope, nil); err != nil {
			t.Fatal(err)
		}
	}
	s := &sessionsService{main: main}
	options, _, err := s.projectAgentOptions(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"global/shared", "workspace/shared", subagent.BuiltinJuniorDeveloperID, subagent.BuiltinCodeReviewerID} {
		found := false
		for _, option := range options {
			found = found || option.ID == id && option.Available
		}
		if !found {
			t.Fatalf("qualified profile %s disappeared: %+v", id, options)
		}
		if err := s.validateProjectAgent(id, root); err != nil {
			t.Fatal(err)
		}
	}
}
