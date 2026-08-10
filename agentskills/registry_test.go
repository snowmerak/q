package agentskills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, scope, name, description string) string {
	t.Helper()
	directory := filepath.Join(root, scope, "skills", name)
	if err := os.MkdirAll(filepath.Join(directory, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# Instructions\n\nDo the useful thing.\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "references", "guide.md"), []byte("reference body"), 0o644); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestDiscoverProjectSkillAndReadLazily(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", t.TempDir())
	writeSkill(t, root, ".agents", "review-go", "Review Go changes when correctness matters.")
	registry, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if skills := registry.Skills(); len(skills) != 1 || skills[0].Description != "Review Go changes when correctness matters." {
		t.Fatalf("unexpected skills: %#v", skills)
	}
	skill := registry.Skills()[0]
	loaded, path, content, err := registry.ReadSkillFile(skill.ID, "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "review-go" || path != "SKILL.md" || !strings.Contains(string(content), "Do the useful thing") {
		t.Fatalf("loaded=%#v path=%q content=%q", loaded, path, content)
	}
	_, path, content, err = registry.ReadSkillFile(skill.ID, "references/guide.md")
	if err != nil {
		t.Fatal(err)
	}
	if path != "references/guide.md" || string(content) != "reference body" {
		t.Fatalf("path=%q content=%q", path, content)
	}
}

func TestProjectSkillShadowsUserSkill(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	writeSkill(t, home, ".agents", "shared", "User description.")
	project := writeSkill(t, root, ".q", "shared", "Project description.")
	registry, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	skill := registry.Skills()[0]
	loaded, err := registry.SkillByID(skill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Directory != project || loaded.Description != "Project description." {
		t.Fatalf("unexpected winner: %#v", loaded)
	}
	if len(registry.Issues()) != 1 || !strings.Contains(registry.Issues()[0].Message, "shadowed") {
		t.Fatalf("missing shadow issue: %#v", registry.Issues())
	}
	entries := registry.Entries()
	if len(entries) != 2 {
		t.Fatalf("management entries = %#v", entries)
	}
	for _, entry := range entries {
		if _, err := registry.SkillByID(entry.ID); err != nil {
			t.Fatalf("shadowed entry %q is not addressable by ID: %v", entry.Directory, err)
		}
	}
}

func TestInvalidSkillBecomesIssue(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", t.TempDir())
	writeSkill(t, root, ".agents", "folder-name", "Valid description.")
	path := filepath.Join(root, ".agents", "skills", "folder-name", "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: other-name\ndescription: mismatch\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Skills()) != 0 || len(registry.Issues()) != 1 {
		t.Fatalf("skills=%#v issues=%#v", registry.Skills(), registry.Issues())
	}
}

func TestReadResourceRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", t.TempDir())
	writeSkill(t, root, ".agents", "safe-skill", "Safe resource reads.")
	registry, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := registry.ReadSkillFile(registry.Skills()[0].ID, "../outside"); err == nil {
		t.Fatal("expected traversal rejection")
	}
}
