package agentskills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func (r *Registry) InstallGit(ctx context.Context, scope, repository string) (Skill, error) {
	root, source, err := r.managedRoot(scope)
	if err != nil {
		return Skill{}, err
	}
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return Skill{}, errors.New("agent skills: Git repository is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Skill{}, err
	}
	temporary, err := os.MkdirTemp(root, ".clone-*")
	if err != nil {
		return Skill{}, err
	}
	defer os.RemoveAll(temporary)
	checkout := filepath.Join(temporary, "checkout")
	if output, err := exec.CommandContext(ctx, "git", "clone", "--", repository, checkout).CombinedOutput(); err != nil {
		return Skill{}, fmt.Errorf("agent skills: git clone: %w: %s", err, strings.TrimSpace(string(output)))
	}
	name, err := skillName(filepath.Join(checkout, "SKILL.md"))
	if err != nil {
		return Skill{}, err
	}
	destination := filepath.Join(root, name)
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return Skill{}, fmt.Errorf("agent skills: %q already exists in %s", name, scope)
		}
		return Skill{}, err
	}
	if err := os.Rename(checkout, destination); err != nil {
		return Skill{}, err
	}
	skill, err := loadSkill(destination, source)
	if err != nil {
		return Skill{}, err
	}
	if err := r.Reload(); err != nil {
		return Skill{}, err
	}
	return skill, nil
}

func (r *Registry) UpdateGit(ctx context.Context, idOrName string) (Skill, error) {
	skill, err := r.resolve(idOrName)
	if err != nil {
		return Skill{}, err
	}
	if _, err := os.Stat(filepath.Join(skill.Directory, ".git")); err != nil {
		return Skill{}, fmt.Errorf("agent skills: %q is not a Git checkout", skill.Name)
	}
	if output, err := exec.CommandContext(ctx, "git", "-C", skill.Directory, "pull", "--ff-only").CombinedOutput(); err != nil {
		return Skill{}, fmt.Errorf("agent skills: git pull: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := r.Reload(); err != nil {
		return Skill{}, err
	}
	return r.resolve(skill.ID)
}

func (r *Registry) RemoveGit(idOrName string) (Skill, error) {
	skill, err := r.resolve(idOrName)
	if err != nil {
		return Skill{}, err
	}
	managed := false
	for _, scope := range []string{"global", "project"} {
		root, _, _ := r.managedRoot(scope)
		relative, relErr := filepath.Rel(root, skill.Directory)
		if relErr == nil && relative == filepath.Base(skill.Directory) && filepath.Dir(skill.Directory) == root {
			managed = true
			break
		}
	}
	if !managed {
		return Skill{}, fmt.Errorf("agent skills: %q is not inside a q-managed skill root", skill.Name)
	}
	if _, err := os.Stat(filepath.Join(skill.Directory, ".git")); err != nil {
		return Skill{}, fmt.Errorf("agent skills: %q is not a Git checkout", skill.Name)
	}
	if err := os.RemoveAll(skill.Directory); err != nil {
		return Skill{}, err
	}
	if err := r.Reload(); err != nil {
		return Skill{}, err
	}
	return skill, nil
}

func (r *Registry) resolve(idOrName string) (Skill, error) {
	value := strings.TrimSpace(idOrName)
	if strings.HasPrefix(value, "skill-") {
		return r.SkillByID(value)
	}
	return r.lookup(value)
}

func (r *Registry) managedRoot(scope string) (string, Source, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "global":
		return filepath.Join(r.home, ".q", "skills"), SourceUserQ, nil
	case "project", "session":
		return filepath.Join(r.root, ".q", "skills"), SourceProjectQ, nil
	default:
		return "", "", errors.New("agent skills: scope must be global or project")
	}
}

func skillName(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("agent skills: read cloned SKILL.md: %w", err)
	}
	metadata, _, err := splitFrontmatter(data)
	if err != nil {
		return "", err
	}
	var header frontmatter
	if err := yaml.Unmarshal(metadata, &header); err != nil {
		return "", err
	}
	name := strings.TrimSpace(header.Name)
	if len(name) < 1 || len(name) > 64 || !validName.MatchString(name) {
		return "", errors.New("agent skills: cloned skill has an invalid name")
	}
	return name, nil
}
