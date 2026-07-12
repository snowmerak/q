package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const validWorkflowYAML = `version: 1
name: stored
loop:
  max_iterations: 1
  review: { provider: grok, prompt: review }
  improve: { provider: codex, prompt: improve }
`

func setupWorkflowStoreTest(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("APPDATA", filepath.Join(root, "appdata"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	return root
}

func TestResolveWorkflowPrefersProjectDefinition(t *testing.T) {
	root := setupWorkflowStoreTest(t)
	project := filepath.Join(root, ".q", "workflows")
	global, err := globalWorkflowDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(project, "review.yaml")
	if err := os.WriteFile(projectPath, []byte(validWorkflowYAML), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, "review.yaml"), []byte(validWorkflowYAML), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveWorkflow("review")
	if err != nil {
		t.Fatal(err)
	}
	if got != projectPath {
		t.Fatalf("resolved %q, want %q", got, projectPath)
	}
}

func TestResolveWorkflowFindsBareYMLName(t *testing.T) {
	root := setupWorkflowStoreTest(t)
	dir := filepath.Join(root, ".q", "workflows")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "review.yml")
	if err := os.WriteFile(want, []byte(validWorkflowYAML), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveWorkflow("review")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved %q, want %q", got, want)
	}
}

func TestSaveWorkflowRunReplacesCheckpoint(t *testing.T) {
	setupWorkflowStoreTest(t)
	run := &WorkflowRun{ID: "run-one", WorkflowName: "test", Status: "running", CreatedAt: time.Now()}
	if err := SaveWorkflowRun(run); err != nil {
		t.Fatal(err)
	}
	run.Status = "completed"
	if err := SaveWorkflowRun(run); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadWorkflowRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "completed" {
		t.Fatalf("status = %q", loaded.Status)
	}
}

func TestInitAndListWorkflowDefinitions(t *testing.T) {
	setupWorkflowStoreTest(t)
	path, err := InitWorkflowDefinition("local", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkflow(path); err != nil {
		t.Fatal(err)
	}
	if _, err := InitWorkflowDefinition("shared", true); err != nil {
		t.Fatal(err)
	}
	definitions, err := ListWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 {
		t.Fatalf("definitions = %#v", definitions)
	}
}

func TestDownloadWorkflowDefinitionValidatesAndPersists(t *testing.T) {
	setupWorkflowStoreTest(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(validWorkflowYAML)) }))
	defer server.Close()
	path, err := downloadWorkflowDefinitionWithClient("remote.yaml", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkflow(path); err != nil {
		t.Fatal(err)
	}
	if _, err := downloadWorkflowDefinitionWithClient("remote.yaml", server.URL, server.Client()); err == nil {
		t.Fatal("expected overwrite refusal")
	}
}

func TestDownloadWorkflowDefinitionRejectsInvalidWorkflow(t *testing.T) {
	setupWorkflowStoreTest(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("version: 1\nname: broken\n")) }))
	defer server.Close()
	if _, err := downloadWorkflowDefinitionWithClient("broken.yaml", server.URL, server.Client()); err == nil {
		t.Fatal("expected validation error")
	}
}
