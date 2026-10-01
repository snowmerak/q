package changes

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFileComparisonBaselinesAndRenames(t *testing.T) {
	root := testRepository(t)
	common := "package main\n\n" + strings.Repeat("// unchanged context\n", 20)
	writeFile(t, root, "before.go", common+"var value = 1\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "baseline")
	testGit(t, root, "mv", "before.go", "after.go")
	writeFile(t, root, "after.go", common+"var value = 2\n")
	testGit(t, root, "add", "after.go")
	writeFile(t, root, "after.go", common+"var value = 1\n")
	snapshot, err := List(t.Context(), root)
	if err != nil || len(snapshot.Files) != 1 {
		t.Fatalf("snapshot = %#v %v", snapshot, err)
	}
	file := snapshot.Files[0]
	if file.OldPath != "before.go" {
		t.Fatalf("rename = %#v", file)
	}
	for _, item := range []struct{ mode, want, reject string }{{"working", "rename from before.go", "+var value = 2"}, {"staged", "+var value = 2", ""}, {"unstaged", "+var value = 1", ""}} {
		section, added, err := ReadComparison(t.Context(), root, file, item.mode)
		if err != nil || added || !strings.Contains(section.Patch, item.want) || (item.reject != "" && strings.Contains(section.Patch, item.reject)) {
			t.Fatalf("%s = %#v %v %v", item.mode, section, added, err)
		}
	}
	index, err := ReadIndex(t.Context(), root, file)
	if err != nil || !strings.Contains(index.Content, "value = 2") {
		t.Fatalf("index = %#v %v", index, err)
	}
	if current := testGit(t, root, "show", ":after.go"); !strings.Contains(current, "value = 2") {
		t.Fatal("comparison changed the index")
	}
}

func TestFileComparisonUnbornAndBoundedIndex(t *testing.T) {
	root := testRepository(t)
	writeFile(t, root, "new.go", "package main\n")
	section, added, err := ReadComparison(t.Context(), root, File{Path: "new.go", Status: "??"}, "working")
	if err != nil || !added || section.Patch != "" {
		t.Fatalf("unborn = %#v %v %v", section, added, err)
	}
	writeFile(t, root, "large.txt", strings.Repeat("한글", maximumPatchBytes))
	writeFile(t, root, "binary.bin", "\x00\xff")
	testGit(t, root, "add", ".")
	large, err := ReadIndex(t.Context(), root, File{Path: "large.txt", Status: "A "})
	if err != nil || !large.Truncated || large.Binary || !utf8.ValidString(large.Content) || len(large.Content) > maximumPatchBytes {
		t.Fatalf("large preview: length=%d binary=%v truncated=%v err=%v", len(large.Content), large.Binary, large.Truncated, err)
	}
	binary, err := ReadIndex(t.Context(), root, File{Path: "binary.bin", Status: "A "})
	if err != nil || !binary.Binary || binary.Content != "" {
		t.Fatalf("binary = %#v %v", binary, err)
	}
}
