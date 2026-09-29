package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdditionalRootsSupportFileAndCommandTools(t *testing.T) {
	primary := t.TempDir()
	additional := t.TempDir()
	outside := t.TempDir()
	for path, body := range map[string]string{
		filepath.Join(primary, "same.txt"):             "primary",
		filepath.Join(additional, "same.txt"):          "additional",
		filepath.Join(additional, "hidden.txt"):        "hidden",
		filepath.Join(additional, "visible.txt"):       "visible",
		filepath.Join(additional, workspaceIgnoreFile): "hidden.txt\n",
		filepath.Join(outside, "outside.txt"):          "outside",
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fs, err := NewFSWithRoots(primary, []string{additional})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	primaryRead, err := fs.ReadFile(ReadFileInput{Path: "same.txt"})
	if err != nil || !containsLine(primaryRead.Content, "primary") {
		t.Fatalf("primary read = %#v, err = %v", primaryRead, err)
	}
	additionalRead, err := fs.ReadFile(ReadFileInput{Path: filepath.Join(additional, "same.txt")})
	if err != nil || !containsLine(additionalRead.Content, "additional") {
		t.Fatalf("additional read = %#v, err = %v", additionalRead, err)
	}
	if _, err := fs.WriteFile(WriteFileInput{Path: filepath.Join(additional, "created.txt"), Content: "created"}); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(additional, "created.txt")); err != nil || string(body) != "created" {
		t.Fatalf("additional write = %q, err = %v", body, err)
	}
	if _, err := fs.ReadFile(ReadFileInput{Path: filepath.Join(outside, "outside.txt")}); err == nil {
		t.Fatal("read_file accepted a path outside configured roots")
	}

	listing, err := fs.ListDirectory(ListDirectoryInput{Path: additional})
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool)
	for _, entry := range listing.Entries {
		names[entry.Name] = true
	}
	if names["hidden.txt"] || !names["visible.txt"] {
		t.Fatalf("additional root listing = %#v", listing.Entries)
	}
	if _, err := fs.RemovePath(RemovePathInput{Path: additional, Recursive: true}); err == nil {
		t.Fatal("remove_path accepted an additional workspace root")
	}
	if _, err := fs.MovePath(MovePathInput{Source: additional, Destination: filepath.Join(primary, "moved")}); err == nil {
		t.Fatal("move_path accepted an additional workspace root")
	}

	started, err := fs.RunCommand(RunCommandInput{Command: "exit 0", Workdir: additional})
	if err != nil {
		t.Fatal(err)
	}
	canonicalAdditional, err := filepath.EvalSymlinks(additional)
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(started.Workdir, canonicalAdditional) {
		t.Fatalf("command workdir = %q, want %q", started.Workdir, canonicalAdditional)
	}
	finished, err := fs.WaitCommand(WaitInput{CommandID: started.CommandID, TimeoutMS: 5000})
	if err != nil || finished.Status != "succeeded" {
		t.Fatalf("command result = %#v, err = %v", finished, err)
	}
}

func containsLine(content, value string) bool {
	for _, line := range splitLines(content) {
		if len(line) >= len(value) && line[len(line)-len(value):] == value {
			return true
		}
	}
	return false
}
