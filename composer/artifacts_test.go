package composer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactsCRUDRevisionsAndPaths(t *testing.T) {
	files := &Artifacts{Root: t.TempDir()}
	empty := ""
	first, err := files.Write("docs/diagram/아이디어.d2", "유저 -> 아이디어", &empty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.Write(first.Path, "overwrite", &empty); !errors.Is(err, ErrArtifactConflict) {
		t.Fatalf("create conflict = %v", err)
	}
	second, err := files.Write(first.Path, "유저 -> 대화 -> 아이디어", &first.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash == second.Hash {
		t.Fatal("hash did not change")
	}
	if err := files.Delete(first.Path, &first.Hash); !errors.Is(err, ErrArtifactConflict) {
		t.Fatalf("delete conflict = %v", err)
	}
	for _, name := range []string{"../escape.d2", "docs/diagram/../outside.d2", "docs/diagram/a.html", "docs/webcomponent/a.d2", "docs/diagram/a.d2/", "docs/diagram/C:foo.d2", "docs/diagram/../../.q/config.d2", "docs\\diagram\\x.d2", "/docs/diagram/a.d2"} {
		if _, err := files.Write(name, "bad", nil); !errors.Is(err, ErrArtifactPath) {
			t.Errorf("path %q: %v", name, err)
		}
	}
	if _, err := files.Write("docs/webcomponent/sample.html", "<button>안녕</button>", &empty); err != nil {
		t.Fatal(err)
	}
	values, err := files.List()
	if err != nil || len(values) != 2 {
		t.Fatalf("list = %+v %v", values, err)
	}
	if err := files.Delete(second.Path, &second.Hash); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Read(second.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := files.Write("docs/diagram/huge.d2", strings.Repeat("a", MaximumArtifactSize+1), nil); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestArtifactsRejectSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "docs")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	files := &Artifacts{Root: root}
	if _, err := files.Write("docs/diagram/escape.d2", "x", nil); err == nil {
		t.Fatal("symlink accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("modified target: %+v %v", entries, err)
	}
}

func TestD2PreviewRendersAndConfinesImports(t *testing.T) {
	files := &Artifacts{Root: t.TempDir()}
	if _, err := files.Write("docs/diagram/common.d2", "user -> idea", nil); err != nil {
		t.Fatal(err)
	}
	value, err := files.Write("docs/diagram/main.d2", "...@common", nil)
	if err != nil {
		t.Fatal(err)
	}
	svg, err := files.RenderD2(t.Context(), value)
	if err != nil || !strings.Contains(string(svg), "<svg") {
		t.Fatalf("render = %s %v", svg, err)
	}
	value, err = files.Write(value.Path, "...@../../../secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.RenderD2(t.Context(), value); err == nil {
		t.Fatal("external import accepted")
	}
	saved, err := files.Read(value.Path)
	if err != nil || saved.Content != value.Content {
		t.Fatal("invalid source was lost")
	}
}
