package tools

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/client"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/lsp"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/tools/builtin"
	"github.com/snowmerak/q/worklock"
	"github.com/snowmerak/q/workspace"
)

type fakeGlobalSkillLibrary struct {
	search   qlibrary.SkillSearchResponse
	items    map[string]qlibrary.SkillResource
	searches int
	gets     int
}

func (f *fakeGlobalSkillLibrary) SearchSkills(_ context.Context, _ qlibrary.SkillSearchRequest) (qlibrary.SkillSearchResponse, error) {
	f.searches++
	return f.search, nil
}

func (f *fakeGlobalSkillLibrary) GetSkill(_ context.Context, id, _ string) (qlibrary.SkillResource, error) {
	f.gets++
	return f.items[id], nil
}

func TestRuntimeListsAndCallsBuiltinTools(t *testing.T) {
	root := t.TempDir()
	skillDirectory := filepath.Join(root, ".agents", "skills", "test-skill")
	if err := os.MkdirAll(skillDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte("---\nname: test-skill\ndescription: Use for runtime tests.\n---\n\nFollow these instructions.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if len(runtime.Tools()) != 17 {
		t.Fatalf("runtime tools = %d", len(runtime.Tools()))
	}
	tools := runtime.Tools()
	for index := 1; index < len(tools); index++ {
		if tools[index-1].Function.Name >= tools[index].Function.Name {
			t.Fatalf("runtime tools are not sorted: %q before %q", tools[index-1].Function.Name, tools[index].Function.Name)
		}
	}
	for _, tool := range tools {
		if _, ok := tool.Function.Parameters["properties"].(map[string]any); !ok {
			t.Fatalf("runtime tool %q has invalid properties: %#v", tool.Function.Name, tool.Function.Parameters["properties"])
		}
	}
	if !runtimeHasTool(runtime, "search_skills") || !runtimeHasTool(runtime, "get_skill") || !runtimeHasTool(runtime, "learn") {
		t.Fatalf("skill retrieval tools are unavailable: %#v", runtime.Tools())
	}
	learned, err := runtime.client.CallTool(context.Background(), &mcp.CallToolParams{Name: "learn"})
	if err != nil {
		t.Fatal(err)
	}
	structured, ok := learned.StructuredContent.(map[string]any)
	if learned.IsError || !ok || structured["enqueued"] != true {
		t.Fatalf("unexpected learn result: %+v", learned)
	}
	environment := runtime.Environment()
	if environment.OS != goruntime.GOOS || environment.Architecture != goruntime.GOARCH || environment.Shell == "" {
		t.Fatalf("runtime environment = %#v", environment)
	}
	result, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-1", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{
			Name: "write_file", Arguments: `{"path":"created.txt","content":"from tool"}`,
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("tool result = %#v, err = %v", result, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "created.txt"))
	if err != nil || string(body) != "from tool" {
		t.Fatalf("created file = %q, err = %v", body, err)
	}
	var receipt loomReceipt
	if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.LoomRef == "" || !receipt.Stored {
		t.Fatalf("Loom receipt = %#v", receipt)
	}
	artifact, err := runtime.loom.Store.Inspect(context.Background(), receipt.LoomRef)
	if err != nil || artifact.Source["tool"] != "write_file" {
		t.Fatalf("captured artifact = %#v, err = %v", artifact, err)
	}
}

func TestRuntimeExplainsAnchorSeparatorsInToolSchemaAndErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	found := 0
	for _, tool := range runtime.Tools() {
		if tool.Function.Name != "read_file" && tool.Function.Name != "edit_file" {
			continue
		}
		found++
		for _, want := range []string{"LINE#HASH:CONTENT", "before the first ':'", "never include ':' or the following file content"} {
			if !strings.Contains(tool.Function.Description, want) {
				t.Fatalf("%s description is missing %q: %s", tool.Function.Name, want, tool.Function.Description)
			}
		}
		if tool.Function.Name == "edit_file" {
			schema, err := json.Marshal(tool.Function.Parameters)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(schema), "Copy only the part before the first ':'") != 2 {
				t.Fatalf("pos/end schema descriptions lost separator guidance: %s", schema)
			}
		}
	}
	if found != 2 {
		t.Fatalf("expected read_file and edit_file, found %d", found)
	}
	arguments, err := json.Marshal(builtin.EditFileInput{Path: "sample.txt", Edits: []builtin.EditOperation{{
		Op: "replace", Pos: "1#" + builtin.HashLine("one", 1) + ":one", Lines: []string{"updated"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Call(t.Context(), client.ToolCall{
		ID: "colon-anchor", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "edit_file", Arguments: string(arguments)},
	})
	if err != nil || !result.IsError || !strings.Contains(result.Content, "anchor contains ':'") ||
		!strings.Contains(result.Content, "remove ':' and all following file content") {
		t.Fatalf("MCP result lost separator guidance: result=%#v, err=%v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "sample.txt"))
	if err != nil || string(data) != "one" {
		t.Fatalf("invalid MCP edit changed the file: data=%q, err=%v", data, err)
	}
}

func TestRuntimeExposesConfiguredLSPTools(t *testing.T) {
	root := t.TempDir()
	runtime, err := NewRuntimeWithArchiveAndLoomOptionsAndLSP(
		context.Background(), root, nil, loom.StoreOptions{}, lsp.GlobalConfig{},
		lsp.WorkspaceConfig{Version: lsp.WorkspaceConfigVersion},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	for _, name := range []string{
		"lsp_status", "lsp_diagnostics", "lsp_hover", "lsp_definition",
		"lsp_references", "lsp_document_symbols", "lsp_workspace_symbols",
	} {
		if !runtimeHasTool(runtime, name) {
			t.Fatalf("LSP tool %q is unavailable", name)
		}
	}
	result, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-lsp-status", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "lsp_status", Arguments: `{}`},
	})
	if err != nil || result.IsError {
		t.Fatalf("status result = %#v, err = %v", result, err)
	}
	var status lsp.StatusResult
	if err := decodeReceiptResult(result.Content, &status); err != nil {
		t.Fatal(err)
	}
	if status.Workspace != root || len(status.Sessions) != 0 {
		t.Fatalf("status = %#v", status)
	}
}

func TestRuntimeAutomaticallyDiscoversLSPRoots(t *testing.T) {
	for _, withLibrary := range []bool{false, true} {
		name := "without library"
		if withLibrary {
			name = "with library"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for name, body := range map[string]string{"go.mod": "module example.test\n", "main.go": "package main\n"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			global := lsp.GlobalConfig{
				Servers:   map[string]lsp.ServerConfig{"test": {Languages: []string{"go"}, Command: "disabled-server", Disabled: true}},
				Languages: map[string]string{"go": "test"},
			}
			var runtime *Runtime
			var err error
			if withLibrary {
				runtime, err = NewRuntimeWithArchiveAndLoomOptionsAndLSPAndLibrary(t.Context(), root, nil, loom.StoreOptions{}, global, lsp.WorkspaceConfig{}, nil)
			} else {
				runtime, err = NewRuntimeWithArchiveAndLoomOptionsAndLSP(t.Context(), root, nil, loom.StoreOptions{}, global, lsp.WorkspaceConfig{})
			}
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			result, err := runtime.Call(t.Context(), client.ToolCall{
				ID: "auto-discover", Type: client.ToolTypeFunction,
				Function: client.FunctionCall{Name: "lsp_document_symbols", Arguments: `{"path":"main.go"}`},
			})
			if err != nil || !result.IsError || !strings.Contains(result.Content, "no server resolved for go at .") {
				t.Fatalf("expected discovered root with preserved disabled server: result=%+v err=%v", result, err)
			}
			status := runtime.lsp.Status(t.Context())
			if len(status.Sessions) != 1 || status.Sessions[0].Root != "." || status.Sessions[0].Source != lsp.RootSourceDiscovered || status.Sessions[0].State == "running" {
				t.Fatalf("discovered status = %+v", status)
			}
			if _, err := os.Stat((workspace.Store{Root: root}).LSPPath()); !os.IsNotExist(err) {
				t.Fatalf("automatic discovery wrote workspace settings: %v", err)
			}
		})
	}
}

func TestRuntimeExposesAndCallsArchiveTools(t *testing.T) {
	root := t.TempDir()
	skillDirectory := filepath.Join(root, ".agents", "skills", "archive-test-skill")
	if err := os.MkdirAll(skillDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte("---\nname: archive-test-skill\ndescription: Diagnose unique archive skill fixtures.\ntags: [testing]\n---\n\nFollow the archived skill.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive, err := sessionstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if _, err := archive.Save(sessionstore.Record{
		ID: "decision-1", Kind: sessionstore.KindMessage, Role: "planner",
		Status: sessionstore.StatusSucceeded, Summary: "approved storage design",
		Content: "Use the workspace archive for durable agent history.",
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntimeWithArchive(context.Background(), root, archive)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if len(runtime.Tools()) != 19 || !runtimeHasTool(runtime, "search_archive") || !runtimeHasTool(runtime, "get_archive_record") {
		t.Fatalf("runtime tools = %#v", runtime.Tools())
	}

	searched, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-search", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "search_archive", Arguments: `{"query":"durable agent history"}`},
	})
	if err != nil || searched.IsError {
		t.Fatalf("search result = %#v, err = %v", searched, err)
	}
	var searchOutput builtin.SearchArchiveOutput
	if err := decodeReceiptResult(searched.Content, &searchOutput); err != nil {
		t.Fatal(err)
	}
	if len(searchOutput.Hits) != 1 || searchOutput.Hits[0].ID != "decision-1" {
		t.Fatalf("search output = %#v", searchOutput)
	}

	got, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-get", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "get_archive_record", Arguments: `{"id":"decision-1"}`},
	})
	if err != nil || got.IsError {
		t.Fatalf("get result = %#v, err = %v", got, err)
	}
	var record builtin.GetArchiveRecordOutput
	if err := decodeReceiptResult(got.Content, &record); err != nil {
		t.Fatal(err)
	}
	if record.ID != "decision-1" || record.Content != "Use the workspace archive for durable agent history." {
		t.Fatalf("record = %#v", record)
	}

	found, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-skill-search", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "search_skills", Arguments: `{"query":"unique archive skill fixtures"}`},
	})
	if err != nil || found.IsError {
		t.Fatalf("search skills = %#v, err = %v", found, err)
	}
	var skills builtin.SearchSkillsOutput
	if err := decodeReceiptResult(found.Content, &skills); err != nil {
		t.Fatal(err)
	}
	if len(skills.Hits) == 0 || skills.Hits[0].Title != "archive-test-skill" {
		t.Fatalf("skill hits = %#v", skills.Hits)
	}
	loaded, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-skill-get", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "get_skill", Arguments: `{"id":"` + skills.Hits[0].ID + `"}`},
	})
	if err != nil || loaded.IsError {
		t.Fatalf("get skill = %#v, err = %v", loaded, err)
	}
	var skill builtin.GetSkillOutput
	if err := json.Unmarshal([]byte(loaded.Content), &skill); err != nil {
		t.Fatal(err)
	}
	if !skill.Stored || skill.Artifact.Ref == "" || skill.Skill.Name != "archive-test-skill" {
		t.Fatalf("loaded skill = %#v", skill)
	}
}

func TestSkillToolsMergeGlobalLibraryAndProjectStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := t.TempDir()
	projectDirectory := filepath.Join(root, ".agents", "skills", "project-procedure")
	if err := os.MkdirAll(projectDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDirectory, "SKILL.md"), []byte("---\nname: project-procedure\ndescription: Project-local procedure.\n---\n\nProject body.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive, err := sessionstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	globalID := "skill-global-library"
	global := &fakeGlobalSkillLibrary{
		search: qlibrary.SkillSearchResponse{Total: 1, Hits: []qlibrary.SkillSearchHit{{
			ID: globalID, Title: "global-procedure", Description: "Global Library procedure.", Scope: "global", Score: 2,
		}}},
		items: map[string]qlibrary.SkillResource{globalID: {
			Skill: agentskills.Skill{ID: globalID, Name: "global-procedure", Description: "Global Library procedure.", Scope: "global"},
			Path:  "SKILL.md", Content: []byte("# Global body\n"),
		}},
	}
	runtime, err := NewRuntimeWithArchiveAndLoomOptionsAndLSPAndLibrary(
		context.Background(), root, archive, loom.StoreOptions{}, lsp.GlobalConfig{}, lsp.WorkspaceConfig{}, global,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	found, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-merged-skills", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "search_skills", Arguments: `{"query":"procedure"}`},
	})
	if err != nil || found.IsError {
		t.Fatalf("merged search = %#v, err = %v", found, err)
	}
	var output builtin.SearchSkillsOutput
	if err := decodeReceiptResult(found.Content, &output); err != nil {
		t.Fatal(err)
	}
	if output.Total != 2 || len(output.Hits) != 2 || output.Hits[0].Scope != "global" || output.Hits[1].Scope != "project" || global.searches != 1 {
		t.Fatalf("merged skill output = %#v, searches = %d", output, global.searches)
	}
	workspaceGlobals, err := archive.Search(context.Background(), sessionstore.SearchOptions{
		Filters: sessionstore.Filters{Kinds: []string{sessionstore.KindSkill}, Scopes: []string{"global"}}, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if workspaceGlobals.Total != 0 {
		t.Fatalf("global skills leaked into workspace Store: %#v", workspaceGlobals.Hits)
	}

	loaded, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-global-skill", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "get_skill", Arguments: `{"id":"` + globalID + `"}`},
	})
	if err != nil || loaded.IsError {
		t.Fatalf("global get_skill = %#v, err = %v", loaded, err)
	}
	var skill builtin.GetSkillOutput
	if err := json.Unmarshal([]byte(loaded.Content), &skill); err != nil {
		t.Fatal(err)
	}
	if !skill.Stored || skill.Skill.Scope != "global" || skill.Artifact.Ref == "" || global.gets != 1 {
		t.Fatalf("global skill output = %#v, gets = %d", skill, global.gets)
	}
}

func TestSkillToolsUseAuthenticatedGlobalLibraryAPIEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	configDirectory := filepath.Join(home, ".q")
	skillDirectory := filepath.Join(configDirectory, "skills", "global-e2e-skill")
	if err := os.MkdirAll(skillDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte("---\nname: global-e2e-skill\ndescription: Authenticated Library MCP integration token.\n---\n\n# End-to-end body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	propositionPayload, err := json.Marshal(qlibrary.PropositionPayload{
		Queries: []string{"which MCP server contains durable facts"}, ExtractorModel: "test-model", ExtractorVersion: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	seedGlobalLibraryRecords(t, configDirectory, sessionstore.Record{
		ID: "prop-mcp-e2e", Kind: sessionstore.KindProposition, Scope: "global",
		Content:    "Durable propositions are read through the existing q-tools MCP server.",
		SearchText: "which MCP server contains durable facts", Refs: []string{"thread://mcp-e2e"},
		Tags: []string{"mcp"}, Payload: propositionPayload,
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	libraryConfig := qlibrary.Config{
		Version: qlibrary.ConfigVersion, Host: "127.0.0.1", Port: port, APIKeyEnv: qlibrary.DefaultAPIKeyEnv,
	}
	libraryConfig, generated, err := (qlibrary.ConfigStore{Dir: configDirectory}).CreateAPIKey(libraryConfig, "MCP test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(qlibrary.DefaultAPIKeyEnv, generated.Secret)
	libraryRuntime, err := qlibrary.EnsureWithOptions(context.Background(), qlibrary.EnsureOptions{
		Dir: configDirectory, Config: libraryConfig,
		ProbeTimeout: 300 * time.Millisecond, StartupTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer libraryRuntime.Close()

	root := t.TempDir()
	archive, err := sessionstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	libraryClient := qlibrary.NewClient(libraryConfig.Endpoint(), generated.Secret, 5*time.Second)
	runtime, err := NewRuntimeWithArchiveAndLoomOptionsAndLSPAndLibrary(
		context.Background(), root, archive, loom.StoreOptions{}, lsp.GlobalConfig{}, lsp.WorkspaceConfig{}, libraryClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if !runtimeHasTool(runtime, "search_propositions") || !runtimeHasTool(runtime, "get_proposition") {
		t.Fatalf("proposition tools are missing: %#v", runtime.Tools())
	}

	found, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-e2e-library-search", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "search_skills", Arguments: `{"query":"Authenticated Library MCP integration token","scopes":["global"]}`},
	})
	if err != nil || found.IsError {
		t.Fatalf("end-to-end Library search = %#v, err = %v", found, err)
	}
	var output builtin.SearchSkillsOutput
	if err := decodeReceiptResult(found.Content, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Hits) != 1 || output.Hits[0].Title != "global-e2e-skill" || len(output.Warnings) != 0 {
		t.Fatalf("end-to-end Library hits = %#v", output)
	}
	loaded, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-e2e-library-get", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "get_skill", Arguments: `{"id":"` + output.Hits[0].ID + `"}`},
	})
	if err != nil || loaded.IsError {
		t.Fatalf("end-to-end Library get = %#v, err = %v", loaded, err)
	}
	var skill builtin.GetSkillOutput
	if err := json.Unmarshal([]byte(loaded.Content), &skill); err != nil {
		t.Fatal(err)
	}
	if !skill.Stored || skill.Skill.Name != "global-e2e-skill" || skill.Artifact.Ref == "" {
		t.Fatalf("end-to-end Library skill = %#v", skill)
	}

	propositions, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-e2e-proposition-search", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "search_propositions", Arguments: `{"query":"which MCP server contains durable facts"}`},
	})
	if err != nil || propositions.IsError {
		t.Fatalf("end-to-end proposition search = %#v, err = %v", propositions, err)
	}
	var propositionSearch builtin.SearchPropositionsOutput
	if err := decodeReceiptResult(propositions.Content, &propositionSearch); err != nil {
		t.Fatal(err)
	}
	if len(propositionSearch.Hits) != 1 || propositionSearch.Hits[0].ID != "prop-mcp-e2e" {
		t.Fatalf("end-to-end proposition hits = %#v", propositionSearch)
	}
	loadedProposition, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-e2e-proposition-get", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "get_proposition", Arguments: `{"id":"prop-mcp-e2e"}`},
	})
	if err != nil || loadedProposition.IsError {
		t.Fatalf("end-to-end proposition get = %#v, err = %v", loadedProposition, err)
	}
	var proposition builtin.GetPropositionOutput
	if err := decodeReceiptResult(loadedProposition.Content, &proposition); err != nil {
		t.Fatal(err)
	}
	if proposition.ID != "prop-mcp-e2e" || len(proposition.Payload.Queries) != 1 || proposition.Refs[0] != "thread://mcp-e2e" {
		t.Fatalf("end-to-end proposition = %#v", proposition)
	}
}

func TestSearchSkillsUsesSessionStoreSnapshotUntilReload(t *testing.T) {
	root := t.TempDir()
	initialDirectory := filepath.Join(root, ".agents", "skills", "initial-skill")
	if err := os.MkdirAll(initialDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(initialDirectory, "SKILL.md"), []byte("---\nname: initial-skill\ndescription: Initial indexed procedure.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive, err := sessionstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	runtime, err := NewRuntimeWithArchive(context.Background(), root, archive)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	listed, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-list-skills", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "search_skills", Arguments: `{"query":""}`},
	})
	if err != nil || listed.IsError {
		t.Fatalf("list skills = %#v, err = %v", listed, err)
	}
	var initial builtin.SearchSkillsOutput
	if err := decodeReceiptResult(listed.Content, &initial); err != nil {
		t.Fatal(err)
	}
	foundInitial := false
	for _, hit := range initial.Hits {
		foundInitial = foundInitial || hit.Title == "initial-skill"
	}
	if !foundInitial {
		t.Fatalf("initial skill hits = %#v", initial.Hits)
	}

	lateDirectory := filepath.Join(root, ".agents", "skills", "late-skill")
	if err := os.MkdirAll(lateDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lateDirectory, "SKILL.md"), []byte("---\nname: late-skill\ndescription: uniquelateskilltoken\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	search := func() builtin.SearchSkillsOutput {
		t.Helper()
		result, callErr := runtime.Call(context.Background(), client.ToolCall{
			ID: "call-search-late-skill", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: "search_skills", Arguments: `{"query":"uniquelateskilltoken"}`},
		})
		if callErr != nil || result.IsError {
			t.Fatalf("search late skill = %#v, err = %v", result, callErr)
		}
		var output builtin.SearchSkillsOutput
		if err := decodeReceiptResult(result.Content, &output); err != nil {
			t.Fatal(err)
		}
		return output
	}
	if before := search(); len(before.Hits) != 0 {
		t.Fatalf("search unexpectedly refreshed skill index: %#v", before.Hits)
	}
	if err := runtime.ReloadSkills(); err != nil {
		t.Fatal(err)
	}
	if after := search(); len(after.Hits) != 1 || after.Hits[0].Title != "late-skill" {
		t.Fatalf("reloaded skill hits = %#v", after.Hits)
	}
}

func TestRuntimeRootsComeFromCurrentSessionNotArchive(t *testing.T) {
	root := t.TempDir()
	sessionRef := loom.Ref("loom://0123456789abcdef0123456789abcdef")
	archiveRef := "loom://fedcba9876543210fedcba9876543210"
	if err := (workspace.Store{Root: root}).Save(workspace.Session{
		Transcript: []client.Message{{Role: client.RoleTool, Content: sessionRef.String()}},
	}); err != nil {
		t.Fatal(err)
	}
	archive, err := sessionstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if _, err := archive.Save(sessionstore.Record{
		Kind: sessionstore.KindResult, Refs: []string{archiveRef}, Content: archiveRef,
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := newRuntime(context.Background(), root, archive, loom.InProcessEvaluator{}, loom.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	refs, err := runtime.loom.Store.Options().Roots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != sessionRef {
		t.Fatalf("Loom roots = %#v", refs)
	}
}
func TestRuntimeEvaluatesCapturedMCPResult(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	var evaluator loom.Evaluator = loom.InProcessEvaluator{}
	if executable := os.Getenv("Q_LOOM_WORKER_EXECUTABLE"); executable != "" {
		evaluator = loom.ProcessEvaluator{Executable: executable}
	}
	runtime, err := newRuntime(context.Background(), root, nil, evaluator, loom.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listed, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-list", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "list_directory", Arguments: `{}`},
	})
	if err != nil || listed.IsError {
		t.Fatalf("list result = %#v, err = %v", listed, err)
	}
	var captured loomReceipt
	if err := json.Unmarshal([]byte(listed.Content), &captured); err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(map[string]any{
		"inputs": map[string]string{"source": captured.LoomRef.String()},
		"code":   `const entries = loom.json(inputs.source, "/structured/entries"); return {count: entries.filter(entry => entry.name === "one.txt").length};`,
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluated, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-eval", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "loom_eval", Arguments: string(arguments)},
	})
	if err != nil || evaluated.IsError {
		t.Fatalf("eval result = %#v, err = %v", evaluated, err)
	}
	var output builtin.LoomEvalOutput
	if err := json.Unmarshal([]byte(evaluated.Content), &output); err != nil {
		t.Fatal(err)
	}
	value, ok := output.Value.(map[string]any)
	if !ok || value["count"] != float64(1) || len(output.Artifact.Parents) != 1 || output.Artifact.Parents[0] != captured.LoomRef {
		t.Fatalf("eval output = %#v", output)
	}
}

func TestTargetResolverUsesRealLoomRuntime(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"one.go": "package sample", "two.txt": "two"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := newRuntime(context.Background(), root, nil, loom.InProcessEvaluator{}, loom.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listed, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "target-list", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "list_directory", Arguments: `{}`},
	})
	if err != nil || listed.IsError {
		t.Fatalf("list result = %#v, err = %v", listed, err)
	}
	var captured loomReceipt
	if err := json.Unmarshal([]byte(listed.Content), &captured); err != nil {
		t.Fatal(err)
	}
	target := subagent.TargetCondition{Any: []subagent.TargetProduct{
		{All: []subagent.TargetSelector{
			{Kind: subagent.TargetSelectorPaths, Paths: []string{"one.go", "two.txt"}},
			{Kind: subagent.TargetSelectorLoom,
				Code:   `const entries = loom.json(inputs.tree, "/structured/entries"); return entries.filter(entry => entry.name.endsWith(".go")).map(entry => entry.name);`,
				Inputs: map[string]string{"tree": captured.LoomRef.String()}},
		}},
		{All: []subagent.TargetSelector{{Kind: subagent.TargetSelectorPaths, Paths: []string{"README.md"}}}},
	}}
	resolved, err := (subagent.TargetResolver{Tools: runtime}).Resolve(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"README.md", "one.go"}
	if !reflect.DeepEqual(resolved, want) {
		t.Fatalf("resolved targets = %#v, want %#v", resolved, want)
	}
}
func TestLargeToolResultReceiptKeepsOnlyBoundedPreview(t *testing.T) {
	artifact := loom.Artifact{
		Ref: "loom://0123456789abcdef0123456789abcdef", Kind: "mcp-result",
		Bytes: 1 << 20, Digest: strings.Repeat("a", 64),
	}
	encoded, err := encodeLoomReceipt(artifact, strings.Repeat("x", maximumInlineToolResult+1))
	if err != nil {
		t.Fatal(err)
	}
	var receipt loomReceipt
	if err := json.Unmarshal([]byte(encoded), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Result != nil || len([]rune(receipt.Preview)) != 4096 || receipt.LoomRef != artifact.Ref {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestCaptureMCPToolResultSupportsCustomServer(t *testing.T) {
	store, err := loom.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	call := client.ToolCall{
		ID: "custom-call-1", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "search", Arguments: `{"query":"loom"}`},
	}
	raw := &mcp.CallToolResult{StructuredContent: map[string]any{
		"matches": []any{"first", "second"},
	}}
	result, err := CaptureMCPToolResult(context.Background(), store, "custom-search", call, raw)
	if err != nil || result.IsError {
		t.Fatalf("captured custom MCP result = %#v, err = %v", result, err)
	}
	var receipt loomReceipt
	if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.LoomRef == "" || !receipt.Stored {
		t.Fatalf("Loom receipt = %#v", receipt)
	}
	artifact, err := store.Inspect(context.Background(), receipt.LoomRef)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Source["server"] != "custom-search" || artifact.Source["tool"] != "search" || artifact.Source["call_id"] != "custom-call-1" {
		t.Fatalf("captured artifact source = %#v", artifact.Source)
	}
}

func TestCaptureResultStoresCompleteACPAgentOutput(t *testing.T) {
	store, err := loom.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	call := client.ToolCall{
		ID: "agent-call-1", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "external_search", Arguments: `{"query":"loom"}`},
	}
	summary := strings.Repeat("evidence-", 12000) + "final-source-marker"
	body, err := json.Marshal(map[string]any{"agent": "codex", "summary": summary})
	if err != nil {
		t.Fatal(err)
	}
	result, err := CaptureResult(context.Background(), store, CaptureSource{
		Protocol: "acp", Name: "codex", Kind: "agent-result",
		MediaType: "application/vnd.q.agent-result+json",
	}, call, client.ToolResult{Content: string(body)})
	if err != nil {
		t.Fatal(err)
	}
	var receipt loomReceipt
	if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Result != nil || receipt.Preview == "" || receipt.Kind != "agent-result" {
		t.Fatalf("receipt = %#v", receipt)
	}
	artifact, err := store.Inspect(context.Background(), receipt.LoomRef)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Source["protocol"] != "acp" || artifact.Source["source"] != "codex" {
		t.Fatalf("artifact source = %#v", artifact.Source)
	}
	stored, err := store.ReadAll(context.Background(), receipt.LoomRef, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), "final-source-marker") {
		t.Fatal("captured ACP artifact was truncated")
	}
}

func decodeReceiptResult(content string, output any) error {
	var receipt struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(content), &receipt); err != nil {
		return err
	}
	return json.Unmarshal(receipt.Result, output)
}

func runtimeHasTool(runtime *Runtime, name string) bool {
	for _, tool := range runtime.Tools() {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}

func TestSchemaObjectAddsPropertiesToEmptyObjectSchema(t *testing.T) {
	schema, err := schemaObject(map[string]any{"type": "object"})
	if err != nil {
		t.Fatal(err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) != 0 {
		t.Fatalf("properties = %#v", schema["properties"])
	}

	existing := map[string]any{"value": map[string]any{"type": "string"}}
	schema, err = schemaObject(map[string]any{"type": "object", "properties": existing})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schema["properties"], existing) {
		t.Fatalf("existing properties changed: %#v", schema["properties"])
	}
}

func seedGlobalLibraryRecords(t *testing.T, dir string, records ...sessionstore.Record) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := worklock.AcquireFile(dir, qlibrary.LockFileName, "seed Library MCP test records")
	if err != nil {
		t.Fatal(err)
	}
	store, err := sessionstore.OpenWithOptions(dir, sessionstore.OpenOptions{
		WorkspaceLock: lock, Directory: "library",
	})
	if err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	for _, record := range records {
		if _, err := store.Save(record); err != nil {
			_ = store.Close()
			_ = lock.Close()
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}
