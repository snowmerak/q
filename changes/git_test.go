package changes

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is unavailable")
	}
	root := t.TempDir()
	testGit(t, root, "init", "-q")
	testGit(t, root, "config", "user.name", "Changes Test")
	testGit(t, root, "config", "user.email", "changes@example.test")
	testGit(t, root, "config", "core.autocrlf", "false")
	testGit(t, root, "config", "core.hooksPath", filepath.Join(root, "no-hooks"))
	return root
}

func testGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"--no-optional-locks", "-C", root, "-c", "commit.gpgsign=false"}, args...)...)
	body, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, body)
	}
	return string(body)
}

func writeFile(t *testing.T, root, path, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadPreservesIndexAndSeparatesStagedAndUnstagedChanges(t *testing.T) {
	root := testRepository(t)
	for _, path := range []string{"both.go", "delete.txt", "before.go", "literal[1].go", "literal1.go", "go.sum"} {
		writeFile(t, root, path, "old\n")
	}
	writeFile(t, root, ".gitattributes", "*.go diff=guard\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "baseline")
	writeFile(t, root, "both.go", "staged\n")
	testGit(t, root, "add", "both.go")
	// The net diff from HEAD is empty, but both individual patches matter.
	writeFile(t, root, "both.go", "old\n")
	if err := os.Remove(filepath.Join(root, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "before.go"), filepath.Join(root, "after.go")); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "--", "before.go", "after.go")
	writeFile(t, root, "literal[1].go", "literal selected\n")
	writeFile(t, root, "literal1.go", "must not leak into selected diff\n")
	writeFile(t, root, "go.sum", "updated checksum\n")
	writeFile(t, root, "새 폴더/new.go", "package main\n")
	writeFile(t, root, ".q/state.json", "private metadata\n")
	writeFile(t, root, "nested/.q/state.json", "private metadata\n")
	testGit(t, root, "config", "diff.external", "q-diff-must-not-execute")
	testGit(t, root, "config", "diff.guard.textconv", "q-textconv-must-not-execute")
	indexPath := filepath.Join(root, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := List(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]File)
	for _, file := range snapshot.Files {
		files[file.Path] = file
		if strings.Contains(file.Path, ".q/") {
			t.Fatalf("q metadata exposed: %#v", file)
		}
	}
	for path, status := range map[string]string{"both.go": "MM", "delete.txt": " D", "after.go": "R ", "go.sum": " M", "새 폴더/new.go": "??"} {
		if files[path].Status != status {
			t.Errorf("%s status = %q, want %q", path, files[path].Status, status)
		}
	}
	if files["after.go"].OldPath != "before.go" {
		t.Fatalf("rename source = %#v", files["after.go"])
	}
	for _, file := range snapshot.Files {
		detail, err := Read(t.Context(), snapshot.Root, file)
		if err != nil {
			t.Fatalf("read %s: %v", file.Path, err)
		}
		if len(detail.Sections) == 0 {
			t.Fatalf("no detail for %s", file.Path)
		}
		switch file.Path {
		case "both.go":
			if len(detail.Sections) != 2 || !strings.HasPrefix(detail.Sections[0].Title, "STAGED") || !strings.HasPrefix(detail.Sections[1].Title, "UNSTAGED") ||
				!strings.Contains(detail.Sections[0].Patch, "+staged") || !strings.Contains(detail.Sections[1].Patch, "-staged") {
				t.Fatalf("opposing patches not preserved: %#v", detail)
			}
		case "literal[1].go":
			if !strings.Contains(detail.Sections[0].Patch, "+literal selected") || strings.Contains(detail.Sections[0].Patch, "must not leak") {
				t.Fatalf("pathspec was not literal: %#v", detail)
			}
		}
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(indexBefore, indexAfter) {
		t.Fatalf("read-only browsing modified the index: %v", err)
	}
}

func TestUntrackedPreviewsWithoutHEAD(t *testing.T) {
	root := testRepository(t)
	for _, test := range []struct {
		path, body, want string
		truncated        bool
	}{
		{"empty.txt", "", "Empty file", false},
		{"binary.bin", "\x00\x01\x02", "Binary file", false},
		{"utf16.txt", "\xff\xfeh\x00i\x00", "Binary file", false},
		{"new.go", "package main\nvar x = 42", "+var x = 42\n\\ No newline", false},
		{"large.txt", strings.Repeat("line\n", maximumPatchLines+1), "+line", true},
		{"long.txt", strings.Repeat("x", maximumPatchBytes+1), "+xxx", true},
		{"unicode.txt", strings.Repeat("가", maximumPatchBytes/3+10), "+가", true},
	} {
		t.Run(test.path, func(t *testing.T) {
			writeFile(t, root, test.path, test.body)
			detail, err := Read(t.Context(), root, File{Path: test.path, Status: "??"})
			if err != nil || len(detail.Sections) != 1 {
				t.Fatalf("read: %#v, %v", detail, err)
			}
			section := detail.Sections[0]
			if !strings.Contains(section.Patch, test.want) || section.Truncated != test.truncated {
				t.Fatalf("preview mismatch: length=%d truncated=%v prefix=%q", len(section.Patch), section.Truncated, section.Patch[:min(100, len(section.Patch))])
			}
		})
	}
	// An initial repository may also have staged additions before its first commit.
	testGit(t, root, "add", "new.go")
	detail, err := Read(t.Context(), root, File{Path: "new.go", Status: "A "})
	if err != nil || len(detail.Sections) != 1 || !strings.Contains(detail.Sections[0].Patch, "+package main") {
		t.Fatalf("unborn HEAD staged diff: %#v %v", detail, err)
	}
}

func TestTrackedBinaryAndLargePatch(t *testing.T) {
	root := testRepository(t)
	writeFile(t, root, "binary.bin", "\x00old")
	writeFile(t, root, "large.txt", "old\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "baseline")
	writeFile(t, root, "binary.bin", "\x00new")
	writeFile(t, root, "large.txt", strings.Repeat("new line\n", 100000))
	for _, path := range []string{"binary.bin", "large.txt"} {
		detail, err := Read(t.Context(), root, File{Path: path, Status: " M"})
		if err != nil || len(detail.Sections) != 1 {
			t.Fatalf("%s: %#v %v", path, detail, err)
		}
		section := detail.Sections[0]
		if path == "binary.bin" && !strings.Contains(section.Patch, "Binary files") {
			t.Fatalf("binary diff = %q", section.Patch)
		}
		if path == "large.txt" && (!section.Truncated || len(section.Patch) > maximumPatchBytes || strings.Count(section.Patch, "\n") > maximumPatchLines) {
			t.Fatalf("unbounded large patch: bytes=%d lines=%d", len(section.Patch), strings.Count(section.Patch, "\n"))
		}
	}
}

func TestParseStatusPreservesLiteralNamesAndConsumesRenameSources(t *testing.T) {
	files, err := parseStatus([]byte("R  new name.go\x00old\nname.go\x00?? 한 글.go\x00 M literal[1].go\x00?? .q/state\x00"))
	if err != nil || len(files) != 3 {
		t.Fatalf("status: %#v %v", files, err)
	}
	if files[1].Path != "new name.go" || files[1].OldPath != "old\nname.go" {
		t.Fatalf("literal rename lost: %#v", files)
	}
	for _, body := range []string{" M missing-terminator", "R  missing-source\x00", "?? ../escape\x00"} {
		if _, err := parseStatus([]byte(body)); err == nil {
			t.Fatalf("accepted malformed status %q", body)
		}
	}
}

func TestCleanRepositoryAndReadErrors(t *testing.T) {
	root := testRepository(t)
	snapshot, err := List(t.Context(), root)
	if err != nil || len(snapshot.Files) != 0 {
		t.Fatalf("empty repository: %#v %v", snapshot, err)
	}
	if _, err := List(t.Context(), t.TempDir()); err == nil {
		t.Fatal("accepted a non-repository")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := List(ctx, root); err == nil {
		t.Fatal("ignored cancellation")
	}
	for _, path := range []string{"../escape", filepath.Join(root, "absolute"), "missing.txt"} {
		if _, err := Read(t.Context(), root, File{Path: path, Status: "??"}); err == nil {
			t.Fatalf("unexpectedly read %q", path)
		}
	}
}

func TestUnmergedFileRetainsCombinedDiff(t *testing.T) {
	root := testRepository(t)
	writeFile(t, root, "conflict.txt", "base\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "base")
	testGit(t, root, "checkout", "-qb", "other")
	writeFile(t, root, "conflict.txt", "theirs\n")
	testGit(t, root, "commit", "-qam", "theirs")
	testGit(t, root, "checkout", "-")
	writeFile(t, root, "conflict.txt", "ours\n")
	testGit(t, root, "commit", "-qam", "ours")
	if output, err := exec.Command("git", "-C", root, "merge", "--no-edit", "other").CombinedOutput(); err == nil {
		t.Fatalf("expected a merge conflict: %s", output)
	}
	snapshot, err := List(t.Context(), root)
	if err != nil || len(snapshot.Files) != 1 || snapshot.Files[0].Status != "UU" {
		t.Fatalf("conflict status: %#v %v", snapshot, err)
	}
	detail, err := Read(t.Context(), root, snapshot.Files[0])
	if err != nil || len(detail.Sections) != 1 || !strings.Contains(detail.Sections[0].Title, "UNMERGED") || !strings.Contains(detail.Sections[0].Patch, "@@@") {
		t.Fatalf("combined diff lost: %#v %v", detail, err)
	}
}

func TestUntrackedSymlinkDoesNotReadTarget(t *testing.T) {
	root := testRepository(t)
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("private contents must not be read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	detail, err := Read(t.Context(), root, File{Path: "link.txt", Status: "??"})
	if err != nil || len(detail.Sections) != 1 || strings.Contains(detail.Sections[0].Patch, "private contents") {
		t.Fatalf("symlink preview: %#v %v", detail, err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "external")); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(t.Context(), root, File{Path: "external/private.txt", Status: "??"}); err == nil {
		t.Fatal("read through an outside parent symlink")
	}
}
