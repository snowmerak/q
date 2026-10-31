package agentskills

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/snowmerak/q/sessionstore"
)

// Exercise junctions on Windows without requiring symlink privileges.
func linkSkillDirectory(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Fatalf("create junction: %v: %s", err, output)
		}
		return
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestLinkedSkillDiscoveryIndexAndResources(t *testing.T) {
	home := t.TempDir()
	source := writeSkill(t, t.TempDir(), ".agents", "linked-browser", "Browser automation guidance.")
	catalog := t.TempDir()
	intermediate := filepath.Join(t.TempDir(), "intermediate")
	linkSkillDirectory(t, source, intermediate)
	linkSkillDirectory(t, intermediate, filepath.Join(catalog, "browser-alias"))
	linkSkillDirectory(t, catalog, filepath.Join(home, ".agents", "skills"))

	registry, err := DiscoverGlobal(home, filepath.Join(home, ".q"))
	if err != nil {
		t.Fatal(err)
	}
	skills := registry.Skills()
	if len(skills) != 1 || len(registry.Issues()) != 0 {
		t.Fatalf("skills=%#v issues=%#v", skills, registry.Issues())
	}
	if skills[0].ID != skillID(source) {
		t.Fatalf("linked ID %q differs from source ID %q", skills[0].ID, skillID(source))
	}
	for _, resource := range []string{"SKILL.md", "references/guide.md"} {
		_, relative, content, err := registry.ReadSkillFile(skills[0].ID, resource)
		if err != nil || relative != resource || len(content) == 0 {
			t.Fatalf("resource=%q relative=%q content=%q err=%v", resource, relative, content, err)
		}
	}
	store, err := sessionstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := registry.SyncRecords(t.Context(), store); err != nil {
		t.Fatal(err)
	}
	result, err := store.Search(t.Context(), SearchOptions("linked-browser", 10, []string{"global"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].Record.ID != skills[0].ID {
		t.Fatalf("linked skill missing from search: %#v", result)
	}
}

func TestDiscoveryReportsLinkCyclesAndContinues(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".agents", "healthy-skill", "Still discoverable.")
	root := filepath.Join(home, ".agents", "skills")
	self := filepath.Join(root, "self")
	left, right := filepath.Join(root, "left"), filepath.Join(root, "right")
	expanding := filepath.Join(root, "expanding")
	broken := filepath.Join(root, "broken")
	ancestor := filepath.Join(root, "ancestor")
	linkSkillDirectory(t, self, self)
	linkSkillDirectory(t, right, left)
	linkSkillDirectory(t, left, right)
	linkSkillDirectory(t, filepath.Join(expanding, "child"), expanding)
	linkSkillDirectory(t, filepath.Join(home, "missing"), broken)
	linkSkillDirectory(t, root, ancestor)

	registry, err := DiscoverGlobal(home, filepath.Join(home, ".q"))
	if err != nil {
		t.Fatal(err)
	}
	if skills := registry.Skills(); len(skills) != 1 || skills[0].Name != "healthy-skill" {
		t.Fatalf("healthy skill lost: %#v", skills)
	}
	issues := make(map[string]string)
	for _, issue := range registry.Issues() {
		issues[issue.Path] = issue.Message
	}
	for _, path := range []string{self, left, right} {
		if !strings.Contains(issues[path], "link cycle") {
			t.Errorf("missing cycle diagnostic for %s: %#v", path, issues)
		}
	}
	if !strings.Contains(issues[expanding], "exceeds") {
		t.Errorf("missing link limit diagnostic: %#v", issues)
	}
	if issues[broken] == "" {
		t.Errorf("missing broken link diagnostic: %#v", issues)
	}
	if issues[ancestor] == "" {
		t.Errorf("expected an invalid skill diagnostic instead of descending into an ancestor: %#v", issues)
	}
}

func TestLinkedSkillUsesTargetGitCommit(t *testing.T) {
	home := t.TempDir()
	repository := t.TempDir()
	runGit(t, repository, "init", "-b", "main")
	runGit(t, repository, "config", "user.email", "skills@example.test")
	runGit(t, repository, "config", "user.name", "Skills Test")
	writeGitSkill(t, repository, "Linked Git skill.")
	runGit(t, repository, "add", "SKILL.md")
	runGit(t, repository, "commit", "-m", "initial")
	linkSkillDirectory(t, repository, filepath.Join(home, ".agents", "skills", "git-alias"))
	registry, err := DiscoverGlobal(home, filepath.Join(home, ".q"))
	if err != nil {
		t.Fatal(err)
	}
	if skills := registry.Skills(); len(skills) != 1 || skills[0].GitCommit == "" || skills[0].GitCommit != readGitCommit(repository) {
		t.Fatalf("skills=%#v issues=%#v", skills, registry.Issues())
	}
}

func TestDiscoveryReportsCyclicSkillRoot(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, ".q", "healthy-skill", "Still discoverable.")
	root := filepath.Join(home, ".agents", "skills")
	linkSkillDirectory(t, root, root)
	registry, err := DiscoverGlobal(home, filepath.Join(home, ".q"))
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Skills()) != 1 || len(registry.Issues()) != 1 || !strings.Contains(registry.Issues()[0].Message, "link cycle") {
		t.Fatalf("skills=%#v issues=%#v", registry.Skills(), registry.Issues())
	}
}

func TestLinkedSkillResourceBoundaries(t *testing.T) {
	home := t.TempDir()
	source := writeSkill(t, t.TempDir(), ".agents", "linked-browser", "Browser automation guidance.")
	linkSkillDirectory(t, source, filepath.Join(home, ".agents", "skills", "browser-alias"))
	linkSkillDirectory(t, filepath.Join(source, "references"), filepath.Join(source, "internal"))
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkSkillDirectory(t, outside, filepath.Join(source, "external"))
	cycle := filepath.Join(source, "cycle")
	linkSkillDirectory(t, cycle, cycle)
	registry, err := DiscoverGlobal(home, filepath.Join(home, ".q"))
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Skills()) != 1 {
		t.Fatalf("skills=%#v issues=%#v", registry.Skills(), registry.Issues())
	}
	id := registry.Skills()[0].ID
	_, path, content, err := registry.ReadSkillFile(id, "internal/guide.md")
	if err != nil || path != "references/guide.md" || string(content) != "reference body" {
		t.Fatalf("path=%q content=%q err=%v", path, content, err)
	}
	for _, path := range []string{"external/secret.md", "cycle/guide.md", "../outside.md"} {
		if _, _, _, err := registry.ReadSkillFile(id, path); err == nil {
			t.Errorf("accepted invalid resource %q", path)
		}
	}
}

func TestResolveSkillPathPreservesRelativeLinkParents(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "target")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	linkSkillDirectory(t, child, filepath.Join(root, "first"))
	alias := filepath.Join(root, "alias")
	if err := os.Symlink("first"+string(filepath.Separator)+"..", alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("relative symlink creation requires Windows symlink privileges: %v", err)
		}
		t.Fatal(err)
	}
	resolved, err := resolveSkillPath(alias)
	if err != nil {
		t.Fatal(err)
	}
	if !sameDirectory(resolved, parent) {
		t.Fatalf("resolved %q instead of target parent %q", resolved, parent)
	}
}
