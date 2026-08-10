// Package agentskills discovers and activates Agent Skills using the
// agentskills.io SKILL.md convention.
package agentskills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	maximumSkillFileBytes = 1 << 20
	maximumResourceBytes  = 1 << 20
)

var validName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Source string

const (
	SourceUserPortable    Source = "user:.agents"
	SourceUserQ           Source = "user:.q"
	SourceProjectPortable Source = "project:.agents"
	SourceProjectQ        Source = "project:.q"
)

type Skill struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	AllowedTools  string            `json:"allowed_tools,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Directory     string            `json:"directory"`
	Source        Source            `json:"source"`
	Scope         string            `json:"scope"`
	Tags          []string          `json:"tags,omitempty"`
	Digest        string            `json:"digest"`
}

type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Registry struct {
	root    string
	home    string
	mu      sync.RWMutex
	skills  map[string]Skill
	byID    map[string]Skill
	entries []Skill
	issues  []Issue
}

func Discover(root string) (*Registry, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("agent skills: resolve workspace: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("agent skills: resolve user home: %w", err)
	}
	r := &Registry{root: root, home: home}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) Reload() error {
	if r == nil {
		return errors.New("agent skills: registry is nil")
	}
	locations := []struct {
		path   string
		source Source
	}{
		{filepath.Join(r.home, ".agents", "skills"), SourceUserPortable},
		{filepath.Join(r.home, ".q", "skills"), SourceUserQ},
		{filepath.Join(r.root, ".agents", "skills"), SourceProjectPortable},
		{filepath.Join(r.root, ".q", "skills"), SourceProjectQ},
	}
	skills := make(map[string]Skill)
	var discovered []Skill
	var issues []Issue
	for _, location := range locations {
		entries, err := os.ReadDir(location.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			issues = append(issues, Issue{Path: location.path, Message: err.Error()})
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			directory := filepath.Join(location.path, entry.Name())
			skill, err := loadSkill(directory, location.source)
			if err != nil {
				issues = append(issues, Issue{Path: directory, Message: err.Error()})
				continue
			}
			if previous, exists := skills[skill.Name]; exists {
				issues = append(issues, Issue{Path: previous.Directory, Message: fmt.Sprintf("shadowed by %s", directory)})
			}
			discovered = append(discovered, skill)
			skills[skill.Name] = skill
		}
	}
	r.mu.Lock()
	byID := make(map[string]Skill, len(discovered))
	for _, skill := range discovered {
		byID[skill.ID] = skill
	}
	sort.Slice(discovered, func(i, j int) bool {
		if discovered[i].Scope != discovered[j].Scope {
			return discovered[i].Scope < discovered[j].Scope
		}
		if discovered[i].Name != discovered[j].Name {
			return discovered[i].Name < discovered[j].Name
		}
		return discovered[i].Source < discovered[j].Source
	})
	r.skills, r.byID, r.entries, r.issues = skills, byID, discovered, issues
	r.mu.Unlock()
	return nil
}

// Entries returns every valid discovered skill, including lower-precedence
// entries shadowed by an active skill. Search indexing should continue to use
// Skills, while management UIs use Entries so hidden checkouts remain
// updateable and removable.
func (r *Registry) Entries() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Skill(nil), r.entries...)
}

func (r *Registry) Skills() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Skill, 0, len(r.skills))
	for _, skill := range r.skills {
		result = append(result, skill)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r *Registry) Issues() []Issue {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Issue(nil), r.issues...)
}

func (r *Registry) SkillByID(id string) (Skill, error) {
	r.mu.RLock()
	skill, ok := r.byID[strings.TrimSpace(id)]
	r.mu.RUnlock()
	if !ok {
		return Skill{}, fmt.Errorf("agent skills: unknown skill record %q", id)
	}
	return skill, nil
}

func (r *Registry) ReadSkillFile(id, path string) (Skill, string, []byte, error) {
	skill, err := r.SkillByID(id)
	if err != nil {
		return Skill{}, "", nil, err
	}
	if strings.TrimSpace(path) == "" {
		path = "SKILL.md"
	}
	resource, err := resolveResource(skill.Directory, path)
	if err != nil {
		return Skill{}, "", nil, err
	}
	data, err := os.ReadFile(resource)
	if err != nil {
		return Skill{}, "", nil, err
	}
	if len(data) > maximumResourceBytes {
		return Skill{}, "", nil, fmt.Errorf("agent skills: resource exceeds %d bytes", maximumResourceBytes)
	}
	relative, _ := filepath.Rel(skill.Directory, resource)
	return skill, filepath.ToSlash(relative), data, nil
}

func (r *Registry) lookup(name string) (Skill, error) {
	name = strings.TrimSpace(name)
	r.mu.RLock()
	skill, ok := r.skills[name]
	r.mu.RUnlock()
	if !ok {
		return Skill{}, fmt.Errorf("agent skills: unknown skill %q", name)
	}
	return skill, nil
}

type frontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	AllowedTools  string            `yaml:"allowed-tools"`
	Metadata      map[string]string `yaml:"metadata"`
	Tags          []string          `yaml:"tags"`
}

func loadSkill(directory string, source Source) (Skill, error) {
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return Skill{}, errors.New("SKILL.md parent is not a directory")
	}
	data, err := os.ReadFile(filepath.Join(directory, "SKILL.md"))
	if err != nil {
		return Skill{}, fmt.Errorf("read SKILL.md: %w", err)
	}
	if len(data) > maximumSkillFileBytes {
		return Skill{}, fmt.Errorf("SKILL.md exceeds %d bytes", maximumSkillFileBytes)
	}
	metadata, _, err := splitFrontmatter(data)
	if err != nil {
		return Skill{}, err
	}
	var header frontmatter
	if err := yaml.Unmarshal(metadata, &header); err != nil {
		return Skill{}, fmt.Errorf("parse frontmatter: %w", err)
	}
	header.Name = strings.TrimSpace(header.Name)
	header.Description = strings.TrimSpace(header.Description)
	if len(header.Name) < 1 || len(header.Name) > 64 || !validName.MatchString(header.Name) {
		return Skill{}, errors.New("name must be 1-64 lowercase letters, digits, or single hyphen-separated segments")
	}
	if header.Name != filepath.Base(directory) {
		return Skill{}, fmt.Errorf("name %q must match directory %q", header.Name, filepath.Base(directory))
	}
	descriptionLength := utf8.RuneCountInString(header.Description)
	if descriptionLength < 1 || descriptionLength > 1024 {
		return Skill{}, errors.New("description must be 1-1024 characters")
	}
	if utf8.RuneCountInString(header.Compatibility) > 500 {
		return Skill{}, errors.New("compatibility must be at most 500 characters")
	}
	digest := sha256.Sum256(data)
	return Skill{
		ID:   skillID(directory),
		Name: header.Name, Description: header.Description, License: strings.TrimSpace(header.License),
		Compatibility: strings.TrimSpace(header.Compatibility), AllowedTools: strings.TrimSpace(header.AllowedTools),
		Metadata: header.Metadata, Directory: directory, Source: source, Scope: sourceScope(source),
		Tags: skillTags(header), Digest: hex.EncodeToString(digest[:]),
	}, nil
}

func skillID(directory string) string {
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		resolved = directory
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(resolved))))
	return "skill-" + hex.EncodeToString(sum[:16])
}

func sourceScope(source Source) string {
	if source == SourceUserPortable || source == SourceUserQ {
		return "global"
	}
	return "project"
}

func skillTags(header frontmatter) []string {
	tags := append([]string(nil), header.Tags...)
	if value := strings.TrimSpace(header.Metadata["tags"]); value != "" {
		tags = append(tags, strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' })...)
	}
	seen := make(map[string]struct{})
	result := tags[:0]
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	sort.Strings(result)
	return result
}

func resolveResource(directory, path string) (string, error) {
	path = filepath.Clean(filepath.FromSlash(strings.TrimSpace(path)))
	if path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return "", errors.New("agent skills: resource path must stay inside the skill directory")
	}
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", fmt.Errorf("agent skills: resolve skill directory: %w", err)
	}
	target, err := filepath.EvalSymlinks(filepath.Join(directory, path))
	if err != nil {
		return "", fmt.Errorf("agent skills: resolve resource: %w", err)
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("agent skills: resource resolves outside the skill directory")
	}
	return target, nil
}

func splitFrontmatter(data []byte) ([]byte, []byte, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) < 3 || strings.TrimSpace(string(lines[0])) != "---" {
		return nil, nil, errors.New("SKILL.md must begin with YAML frontmatter")
	}
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(string(lines[index])) == "---" {
			return bytes.Join(lines[1:index], []byte("\n")), bytes.Join(lines[index+1:], []byte("\n")), nil
		}
	}
	return nil, nil, errors.New("SKILL.md frontmatter is not closed")
}
