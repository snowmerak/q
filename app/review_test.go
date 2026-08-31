package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectReviewRequestCapturesBoundedWorkingTreeEvidence(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", output, err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nvar value = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".q"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".q", "session.json"), []byte("metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := collectReviewRequest(t.Context(), root, "  focus on regressions  ", "run-review")
	if err != nil {
		t.Fatal(err)
	}
	if request.ID != "run-review" || request.Objective != "focus on regressions" || len(request.Files) != 1 {
		t.Fatalf("request = %#v", request)
	}
	file := request.Files[0]
	if file.Path != "main.go" || file.Status != "??" || len(file.Sections) != 1 ||
		!strings.Contains(file.Sections[0].Patch, "+var value = 1") {
		t.Fatalf("reviewed file = %#v", file)
	}
}

func TestCollectReviewRequestDefaultsRequestAndRejectsCleanTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", output, err)
	}
	if _, err := collectReviewRequest(t.Context(), root, "", ""); err == nil || !strings.Contains(err.Error(), "no changes") {
		t.Fatalf("clean review error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	request, err := collectReviewRequest(t.Context(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if request.Objective != defaultReviewRequest {
		t.Fatalf("default request = %q", request.Objective)
	}
}

func TestCollectReviewRequestBoundsAggregatePatchInput(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", output, err)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(strings.Repeat(name, 100<<10)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	request, err := collectReviewRequest(t.Context(), root, "review large changes", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Files) != 3 || len(request.Files[2].Sections) != 1 {
		t.Fatalf("files = %#v", request.Files)
	}
	last := request.Files[2].Sections[0]
	if !last.Truncated || !strings.Contains(last.Patch, "aggregate review input reached its safety limit") {
		t.Fatalf("last patch was not bounded: %#v", last)
	}
}

func TestReviewIsNotExposedAsALocalSlashCommand(t *testing.T) {
	for _, command := range localSlashCommands {
		if command.name == "/review" {
			t.Fatal("review must remain CLI-only")
		}
	}
}
