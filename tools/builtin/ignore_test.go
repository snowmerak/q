package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoveryIgnorePatterns(t *testing.T) {
	ignore := parseDiscoveryIgnore(`
# generated trees
.gradle/
/build/
vendor/**
*.tmp
!important.tmp
generated/**/scratch/
`)
	tests := []struct {
		path      string
		directory bool
		ignored   bool
	}{
		{path: ".q", directory: true, ignored: true},
		{path: ".gradle", directory: true, ignored: true},
		{path: "module/.gradle", directory: true, ignored: true},
		{path: "build", directory: true, ignored: true},
		{path: "module/build", directory: true, ignored: false},
		{path: "vendor/library.go", ignored: true},
		{path: "cache.tmp", ignored: true},
		{path: "important.tmp", ignored: false},
		{path: "generated/scratch", directory: true, ignored: true},
		{path: "generated/deep/tree/scratch", directory: true, ignored: true},
		{path: "generated/deep/tree/source.go", ignored: false},
	}
	for _, test := range tests {
		if got := ignore.matches(test.path, test.directory); got != test.ignored {
			t.Errorf("matches(%q, directory=%v) = %v; want %v", test.path, test.directory, got, test.ignored)
		}
	}
}

func TestListDirectoryAppliesQIgnoreDuringDiscovery(t *testing.T) {
	fs := newTestFS(t)
	writeTestFile(t, fs, "source.go", "package sample")
	writeTestFile(t, fs, "build/output.bin", "generated")
	writeTestFile(t, fs, "module/build/keep.go", "package module")
	writeTestFile(t, fs, "vendor/library.go", "package vendor")
	if err := os.WriteFile(filepath.Join(fs.Root, workspaceIgnoreFile), []byte("/build/\nvendor/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := fs.ListDirectory(ListDirectoryInput{})
	if err != nil {
		t.Fatal(err)
	}
	if hasDirectoryEntry(root.Entries, "build") || hasDirectoryEntry(root.Entries, "vendor") {
		t.Fatalf("ignored roots leaked into discovery: %+v", root.Entries)
	}
	if !hasDirectoryEntry(root.Entries, "source.go") || !hasDirectoryEntry(root.Entries, "module") {
		t.Fatalf("source entries missing from discovery: %+v", root.Entries)
	}

	module, err := fs.ListDirectory(ListDirectoryInput{Path: "module"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasDirectoryEntry(module.Entries, "build") {
		t.Fatalf("root-anchored pattern excluded nested build: %+v", module.Entries)
	}

	build, err := fs.ListDirectory(ListDirectoryInput{Path: "build"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasDirectoryEntry(build.Entries, "output.bin") {
		t.Fatalf("explicit ignored-directory access was blocked: %+v", build.Entries)
	}
}

func hasDirectoryEntry(entries []DirectoryEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name == name {
			return true
		}
	}
	return false
}
