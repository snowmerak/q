package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestFS(t *testing.T) *FS {
	t.Helper()
	fs, err := NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func writeTestFile(t *testing.T, fs *FS, name, content string) {
	t.Helper()
	path := filepath.Join(fs.Root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func anchor(line int, content string) string {
	return fmt.Sprintf("%d#%s", line, HashLine(content, line))
}

func TestReadAndBatchEdit(t *testing.T) {
	fs := newTestFS(t)
	writeTestFile(t, fs, "sample.txt", "one\ntwo\nthree\nfour")
	before, err := os.Stat(filepath.Join(fs.Root, "sample.txt"))
	if err != nil {
		t.Fatal(err)
	}

	read, err := fs.ReadFile(ReadFileInput{Path: "sample.txt", Offset: 2, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if want := "2#" + HashLine("two", 2) + ":two\n3#" + HashLine("three", 3) + ":three"; read.Content != want {
		t.Fatalf("read content = %q; want %q", read.Content, want)
	}

	result, err := fs.EditFile(EditFileInput{Path: "sample.txt", Edits: []EditOperation{
		{Op: "replace", Pos: anchor(2, "two"), End: anchor(3, "three"), Lines: []string{"TWO", "THREE"}},
		{Op: "append", Pos: anchor(4, "four"), Lines: []string{"five"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied != 2 || !strings.Contains(result.Diff, "- 2#") {
		t.Fatalf("unexpected edit result: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(fs.Root, "sample.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "one\nTWO\nTHREE\nfour\nfive"; got != want {
		t.Fatalf("edited content = %q; want %q", got, want)
	}
	if info, err := os.Stat(filepath.Join(fs.Root, "sample.txt")); err != nil || info.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("mode was not preserved: info=%v err=%v", info, err)
	}
}

func TestEditRejectsStaleAndOverlappingEdits(t *testing.T) {
	fs := newTestFS(t)
	writeTestFile(t, fs, "sample.txt", "one\ntwo\nthree")

	_, err := fs.EditFile(EditFileInput{Path: "sample.txt", Edits: []EditOperation{{
		Op: "replace", Pos: anchor(2, "old two"), Lines: []string{"TWO"},
	}}})
	if err == nil || !strings.Contains(err.Error(), "E_STALE_CONTEXT") {
		t.Fatalf("expected stale error, got %v", err)
	}

	_, err = fs.EditFile(EditFileInput{Path: "sample.txt", Edits: []EditOperation{
		{Op: "replace", Pos: anchor(1, "one"), End: anchor(2, "two"), Lines: []string{"x"}},
		{Op: "replace", Pos: anchor(2, "two"), Lines: []string{"y"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error, got %v", err)
	}
}

func TestEditRejectsColonInAnchorsWithGuidance(t *testing.T) {
	for _, test := range []struct {
		name, op, field, suffix string
	}{
		{"replace pos trailing colon", "replace", "pos", ":"},
		{"replace pos display line", "replace", "pos", ":\t\"fmt\""},
		{"replace end trailing colon", "replace", "end", ":"},
		{"replace end display line", "replace", "end", ":key: value"},
		{"append pos display line", "append", "pos", ":two"},
		{"prepend pos display line", "prepend", "pos", ":two"},
		{"append invalid end", "append", "end", ":two"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fs := newTestFS(t)
			const original = "one\ntwo\nthree"
			writeTestFile(t, fs, "sample.txt", original)
			edit := EditOperation{Op: test.op, Pos: anchor(2, "two"), Lines: []string{"updated"}}
			if test.field == "pos" {
				edit.Pos += test.suffix
			} else {
				edit.End = anchor(2, "two") + test.suffix
			}
			_, err := fs.EditFile(EditFileInput{Path: "sample.txt", Edits: []EditOperation{
				{Op: "replace", Pos: anchor(1, "one"), Lines: []string{"ONE"}},
				edit,
			}})
			if err == nil {
				t.Fatal("accepted a colon in an anchor")
			}
			for _, want := range []string{"[E_INVALID_PATCH]", "edit 1", "invalid " + test.field + " anchor", "contains ':'", "before the first ':'", "remove ':' and all following file content"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, missing %q", err, want)
				}
			}
			data, err := os.ReadFile(filepath.Join(fs.Root, "sample.txt"))
			if err != nil || string(data) != original {
				t.Fatalf("invalid batch changed the file: data=%q, err=%v", data, err)
			}
		})
	}
}

func TestEditAllowsColonInReplacementContent(t *testing.T) {
	fs := newTestFS(t)
	writeTestFile(t, fs, "sample.txt", "one")
	_, err := fs.EditFile(EditFileInput{Path: "sample.txt", Edits: []EditOperation{{
		Op: "replace", Pos: anchor(1, "one"), Lines: []string{"url: https://example.test"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(fs.Root, "sample.txt"))
	if err != nil || string(data) != "url: https://example.test" {
		t.Fatalf("replacement content = %q, err=%v", data, err)
	}
}

func TestWorkspaceJailRejectsEscapes(t *testing.T) {
	fs := newTestFS(t)
	if _, err := fs.ReadFile(ReadFileInput{Path: "../outside.txt"}); err == nil {
		t.Fatal("lexical workspace escape succeeded")
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(fs.Root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := fs.ReadFile(ReadFileInput{Path: "outside-link/secret.txt"}); err == nil {
		t.Fatal("symlink workspace escape succeeded")
	}
}

func TestCreateDirectoryAlwaysCreatesParents(t *testing.T) {
	for _, input := range []CreateDirectoryInput{
		{Path: "default/a/b"},
		{Path: "parents/a/b", Parents: true},
	} {
		t.Run(input.Path, func(t *testing.T) {
			fs := newTestFS(t)
			for attempt := 0; attempt < 2; attempt++ {
				output, err := fs.CreateDirectory(input)
				if err != nil {
					t.Fatalf("attempt %d: %v", attempt, err)
				}
				if output.Path != input.Path {
					t.Fatalf("output path = %q; want %q", output.Path, input.Path)
				}
				info, err := os.Stat(filepath.Join(fs.Root, input.Path))
				if err != nil || !info.IsDir() {
					t.Fatalf("directory was not created: info=%v err=%v", info, err)
				}
			}
		})
	}
}

func TestCreateDirectoryRejectsInvalidPaths(t *testing.T) {
	for _, path := range []string{"file.txt", "file.txt/child", "../outside/child", ""} {
		t.Run(path, func(t *testing.T) {
			fs := newTestFS(t)
			writeTestFile(t, fs, "file.txt", "keep")
			if _, err := fs.CreateDirectory(CreateDirectoryInput{Path: path}); err == nil {
				t.Fatal("invalid directory path unexpectedly succeeded")
			}
			data, err := os.ReadFile(filepath.Join(fs.Root, "file.txt"))
			if err != nil || string(data) != "keep" {
				t.Fatalf("existing file changed: data=%q err=%v", data, err)
			}
		})
	}
}

func TestBasicFileAndDirectoryOperations(t *testing.T) {
	fs := newTestFS(t)
	if _, err := fs.CreateDirectory(CreateDirectoryInput{Path: "a/b", Parents: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteFile(WriteFileInput{Path: "a/b/file.txt", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteFile(WriteFileInput{Path: "a/b/file.txt", Content: "clobber"}); err != nil {
		t.Fatalf("write_file did not replace existing file: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(fs.Root, "a/b/file.txt")); err != nil || string(body) != "clobber" {
		t.Fatalf("replaced file = %q, err = %v", body, err)
	}
	if _, err := fs.CopyPath(CopyPathInput{Source: "a", Destination: "copy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.MovePath(MovePathInput{Source: "copy/b/file.txt", Destination: "moved.txt"}); err != nil {
		t.Fatal(err)
	}
	listing, err := fs.ListDirectory(ListDirectoryInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Entries) != 3 {
		t.Fatalf("root entries = %+v", listing.Entries)
	}
	if _, err := fs.RemovePath(RemovePathInput{Path: "copy", Recursive: true}); err != nil {
		t.Fatal(err)
	}
}

func TestCopyDirectoryIntoItselfIsRejected(t *testing.T) {
	fs := newTestFS(t)
	writeTestFile(t, fs, "source/file.txt", "x")
	if _, err := fs.CopyPath(CopyPathInput{Source: "source", Destination: "source/copy"}); err == nil {
		t.Fatal("copy into source unexpectedly succeeded")
	}
}

func TestListDirectoryOmitsQMetadataOnlyFromWorkspaceRoot(t *testing.T) {
	fs := newTestFS(t)
	writeTestFile(t, fs, ".q/data/records/record.json", "internal")
	writeTestFile(t, fs, "source.go", "package sample")

	root, err := fs.ListDirectory(ListDirectoryInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range root.Entries {
		if entry.Name == ".q" {
			t.Fatalf("workspace metadata leaked into root listing: %+v", root.Entries)
		}
	}
	if len(root.Entries) != 1 || root.Entries[0].Name != "source.go" {
		t.Fatalf("root entries = %+v", root.Entries)
	}
	if root.Ignored != 1 {
		t.Fatalf("ignored root entry count = %d; want 1", root.Ignored)
	}

	metadata, err := fs.ListDirectory(ListDirectoryInput{Path: ".q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.Entries) != 1 || metadata.Entries[0].Name != "data" {
		t.Fatalf("explicit .q entries = %+v", metadata.Entries)
	}
}
