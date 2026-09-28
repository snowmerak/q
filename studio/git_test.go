package studio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/config"
)

func TestChangesAndCommitReviewAPI(t *testing.T) {
	root := t.TempDir()
	studioGit(t, root, "init")
	studioGit(t, root, "config", "user.name", "Q Studio Test")
	studioGit(t, root, "config", "user.email", "studio@example.test")
	writeStudioGitFile(t, root, "main.go", "package main\n\nvar answer = 42\n")
	studioGit(t, root, "add", "main.go")
	studioGit(t, root, "commit", "-m", "chore: initial")
	writeStudioGitFile(t, root, "main.go", "package main\n\nvar answer  = 42\n")

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/models" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"object":"list","data":[{"id":"test-model","object":"model"}]}`)
	}))
	defer upstream.Close()
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Provider.BaseURL = upstream.URL
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sessions.Close(); err != nil {
			t.Error(err)
		}
		if err := commits.Close(); err != nil {
			t.Error(err)
		}
	})

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/changes?workspace_root="+url.QueryEscape(root), nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"path":"main.go"`) || !strings.Contains(list.Body.String(), `"status":" M"`) {
		t.Fatalf("GET changes = %d %s", list.Code, list.Body.String())
	}
	detail := httptest.NewRecorder()
	handler.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/changes/file?workspace_root="+url.QueryEscape(root)+"&path=main.go", nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "UNSTAGED") || !strings.Contains(detail.Body.String(), "+var answer  = 42") {
		t.Fatalf("GET change detail = %d %s", detail.Code, detail.Body.String())
	}

	prepared := serveJSON(t, handler, http.MethodPost, "/api/v1/workspaces/commits", commitPrepareRequest{WorkspaceRoot: root})
	if prepared.Code != http.StatusCreated {
		t.Fatalf("POST commit review = %d %s", prepared.Code, prepared.Body.String())
	}
	var review commitSessionResponse
	if err := json.Unmarshal(prepared.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}
	if review.ID == "" || !review.AutoStaged || len(review.Proposals) != 1 {
		t.Fatalf("commit review = %#v", review)
	}
	updated := serveJSON(t, handler, http.MethodPut, "/api/v1/workspaces/commits/"+review.ID+"/proposals/0", commitProposalUpdate{Message: "style: align answer declaration"})
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), "align answer declaration") {
		t.Fatalf("PUT proposal = %d %s", updated.Code, updated.Body.String())
	}
	executed := serveJSON(t, handler, http.MethodPost, "/api/v1/workspaces/commits/"+review.ID+"/execute", commitExecuteRequest{})
	if executed.Code != http.StatusOK || !strings.Contains(executed.Body.String(), `"status":"committed"`) {
		t.Fatalf("POST execute = %d %s", executed.Code, executed.Body.String())
	}
	if subject := studioGit(t, root, "log", "-1", "--pretty=%s"); subject != "style: align answer declaration" {
		t.Fatalf("commit subject = %q", subject)
	}
}

func studioGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(context.Background(), "git", append([]string{"-C", root}, arguments...)...)
	body, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, body)
	}
	return strings.TrimSpace(string(body))
}

func writeStudioGitFile(t *testing.T, root, path, content string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
