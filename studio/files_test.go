package studio

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/snowmerak/q/config"
)

func fileViewerHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close(); _ = commits.Close() })
	return handler
}

func fileViewerGet(t *testing.T, handler http.Handler, root, endpoint, path string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/files"+endpoint+"?"+url.Values{"workspace_root": {root}, "path": {path}}.Encode(), nil))
	return response
}

func TestFileViewerRawAndDiff(t *testing.T) {
	root := t.TempDir()
	studioGit(t, root, "init")
	studioGit(t, root, "config", "user.name", "File Viewer Test")
	studioGit(t, root, "config", "user.email", "viewer@example.test")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	writeStudioGitFile(t, root, "src/main.go", "package main\nvar answer = 1\n")
	writeStudioGitFile(t, root, "src/deleted.go", "package main\n")
	writeStudioGitFile(t, root, "src/clean.go", "package main\n")
	writeStudioGitFile(t, root, "outside.go", "package main\n")
	studioGit(t, root, "add", ".")
	studioGit(t, root, "commit", "-m", "fixture")
	writeStudioGitFile(t, root, "src/main.go", "package main\nvar answer = 2\n")
	studioGit(t, root, "add", "src/main.go")
	writeStudioGitFile(t, root, "src/main.go", "package main\nvar answer = 3\n")
	writeStudioGitFile(t, root, "outside.go", "package main\nvar other = 4\n")
	writeStudioGitFile(t, root, "src/new.go", "package main\nvar newFile = true\n")
	if err := os.Remove(filepath.Join(root, "src/deleted.go")); err != nil {
		t.Fatal(err)
	}
	handler := fileViewerHandler(t)
	get := func(endpoint, path string) *httptest.ResponseRecorder {
		response := fileViewerGet(t, handler, filepath.Join(root, "src"), endpoint, path)
		if response.Code != http.StatusOK {
			t.Fatalf("%s %s = %d %s", endpoint, path, response.Code, response.Body.String())
		}
		return response
	}
	raw := get("/content", "main.go")
	if !strings.Contains(raw.Body.String(), "answer = 3") {
		t.Fatalf("raw = %s", raw.Body.String())
	}
	diff := get("/diff", "main.go")
	for _, want := range []string{"HEAD → working tree", "answer = 1", "answer = 3"} {
		if !strings.Contains(diff.Body.String(), want) {
			t.Fatalf("diff lacks %s: %s", want, diff.Body.String())
		}
	}
	if !strings.Contains(get("/diff", "new.go").Body.String(), `"all_added":true`) {
		t.Fatal("new file diff missing")
	}
	for _, comparison := range []struct{ mode, title, old, current string }{{"staged", "HEAD → index", "answer = 1", "answer = 2"}, {"unstaged", "Index → working tree", "answer = 2", "answer = 3"}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/files/diff?"+url.Values{"workspace_root": {filepath.Join(root, "src")}, "path": {"main.go"}, "comparison": {comparison.mode}}.Encode(), nil))
		var result workspaceFileDiff
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil {
			t.Fatalf("comparison = %d %s", response.Code, response.Body.String())
		}
		if !strings.Contains(result.Content.Content, comparison.current) || len(result.Sections) != 1 || result.Sections[0].Title != comparison.title || !strings.Contains(result.Sections[0].Patch, comparison.old) {
			t.Fatalf("incorrect %s comparison: %#v", comparison.mode, result)
		}
	}
	if !strings.Contains(get("/content", "deleted.go").Body.String(), `"missing":true`) {
		t.Fatal("deleted raw is not marked missing")
	}
	if !strings.Contains(get("/diff", "deleted.go").Body.String(), "-package main") {
		t.Fatal("deletion diff missing")
	}
	if !strings.Contains(get("/diff", "clean.go").Body.String(), `"sections":[]`) {
		t.Fatal("clean file should have no diff")
	}
	status := get("/changes", ".")
	if strings.Contains(status.Body.String(), "outside.go") || strings.Contains(status.Body.String(), "src/main.go") {
		t.Fatalf("subdirectory scope = %s", status.Body.String())
	}
	if !strings.Contains(status.Body.String(), `"path":"main.go"`) {
		t.Fatal("subdirectory status missing")
	}
	if !strings.Contains(get("", ".").Body.String(), "main.go") {
		t.Fatal("directory list missing")
	}
	// These APIs must not stage, write, or create workspace session metadata.
	if _, err := os.Stat(filepath.Join(root, "src", ".q")); !os.IsNotExist(err) {
		t.Fatalf("viewer created metadata: %v", err)
	}
	if got := studioGit(t, root, "show", ":src/main.go"); !strings.Contains(got, "answer = 2") {
		t.Fatalf("viewer changed index: %s", got)
	}
}

func TestFileViewerBoundsAndScope(t *testing.T) {
	root := t.TempDir()
	handler := fileViewerHandler(t)
	writeStudioGitFile(t, root, "binary.dat", "\x00\x01\xff")
	writeStudioGitFile(t, root, "large.txt", strings.Repeat("a", filePreviewBytes-1)+"한글")
	writeStudioGitFile(t, root, "lines.txt", strings.Repeat("line\n", filePreviewLines+2))
	for _, directory := range []string{".git", ".q", "nested"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeStudioGitFile(t, root, ".git/config", "hidden")
	for _, item := range []struct {
		path              string
		binary, truncated bool
	}{{"binary.dat", true, false}, {"large.txt", false, true}, {"lines.txt", false, true}} {
		response := fileViewerGet(t, handler, root, "/content", item.path)
		var content workspaceFileContent
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &content) != nil {
			t.Fatalf("content = %d %s", response.Code, response.Body.String())
		}
		if content.Binary != item.binary || content.Truncated != item.truncated || !utf8.ValidString(content.Content) || len(content.Content) > filePreviewBytes {
			t.Fatalf("invalid preview: %#v", content)
		}
	}
	for _, path := range []string{"../secret.txt", filepath.Join(t.TempDir(), "secret.txt"), ".git/config", "nested/../../secret.txt", ".q/session.json"} {
		if response := fileViewerGet(t, handler, root, "/content", path); response.Code != http.StatusBadRequest {
			t.Fatalf("accepted %s: %d", path, response.Code)
		}
	}
	if runtime.GOOS == "windows" {
		if response := fileViewerGet(t, handler, root, "/content", ".GIT/config"); response.Code != http.StatusBadRequest {
			t.Fatal("case variant bypassed metadata filter")
		}
	}
	list := fileViewerGet(t, handler, root, "", ".")
	if strings.Contains(list.Body.String(), `"name":".git"`) || strings.Contains(list.Body.String(), `"name":".q"`) {
		t.Fatal("metadata listed")
	}
	diff := fileViewerGet(t, handler, root, "/diff", "large.txt")
	if diff.Code != 200 || !strings.Contains(diff.Body.String(), `"available":false`) {
		t.Fatalf("non-Git diff = %d %s", diff.Code, diff.Body.String())
	}
}

func TestFileViewerDirectoryPagination(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < fileDirectoryPage+7; index++ {
		writeStudioGitFile(t, root, fmt.Sprintf("file-%04d.txt", index), "")
	}
	handler := fileViewerHandler(t)
	first := fileViewerGet(t, handler, root, "", ".")
	var page workspaceFileListing
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &page) != nil || len(page.Entries) != fileDirectoryPage || page.NextOffset != fileDirectoryPage {
		t.Fatalf("first page = %d %s", first.Code, first.Body.String())
	}
	seen := map[string]bool{}
	for _, entry := range page.Entries {
		seen[entry.Path] = true
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/files?"+url.Values{"workspace_root": {root}, "offset": {fmt.Sprint(page.NextOffset)}}.Encode(), nil))
	if second.Code != 200 || json.Unmarshal(second.Body.Bytes(), &page) != nil || len(page.Entries) != 7 || page.NextOffset != -1 {
		t.Fatalf("second page = %d %s", second.Code, second.Body.String())
	}
	for _, entry := range page.Entries {
		if seen[entry.Path] {
			t.Fatalf("duplicate entry %s", entry.Path)
		}
		seen[entry.Path] = true
	}
	if len(seen) != fileDirectoryPage+7 {
		t.Fatal("pagination lost entries")
	}
}

func TestFileViewerSymlinkScope(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeStudioGitFile(t, outside, "secret.txt", "outside secret")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeStudioGitFile(t, root, "inside.txt", "inside")
	if err := os.Symlink("inside.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	handler := fileViewerHandler(t)
	for _, endpoint := range []string{"", "/content"} {
		path := "escape"
		if endpoint != "" {
			path += "/secret.txt"
		}
		response := fileViewerGet(t, handler, root, endpoint, path)
		if response.Code == http.StatusOK || strings.Contains(response.Body.String(), "outside secret") {
			t.Fatalf("escaped root = %d %s", response.Code, response.Body.String())
		}
	}
	if response := fileViewerGet(t, handler, root, "/content", "link.txt"); response.Code != 200 || !strings.Contains(response.Body.String(), `"content":"inside"`) {
		t.Fatalf("inside link = %d %s", response.Code, response.Body.String())
	}
}
