package subagent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/snowmerak/q/config"
	"gopkg.in/yaml.v3"
)

type Profile struct {
	Version      int      `yaml:"version" json:"version"`
	Name         string   `yaml:"name" json:"name"`
	Description  string   `yaml:"description,omitempty" json:"description,omitempty"`
	Role         string   `yaml:"role" json:"role"`
	SystemPrompt string   `yaml:"system_prompt" json:"system_prompt"`
	Tools        []string `yaml:"tools" json:"tools"`
}

func (p Profile) Validate() error {
	if p.Version != 1 {
		return errors.New("profile: version must be 1")
	}
	if !config.ValidCustomName(p.Name) || !config.ValidCustomName(p.Role) {
		return errors.New("profile: invalid name or role")
	}
	if strings.TrimSpace(p.SystemPrompt) == "" {
		return errors.New("profile: system_prompt is required")
	}
	if p.Tools == nil {
		return errors.New("profile: tools is required (use [] for none)")
	}
	seen := map[string]bool{}
	for _, name := range p.Tools {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) || seen[name] {
			return fmt.Errorf("profile: invalid or duplicate tool %q", name)
		}
		seen[name] = true
	}
	return nil
}

type ProfileEntry struct {
	Profile     Profile
	Path, Scope string
	Raw         []byte
	Err         error
	Shadowed    bool
}
type ProfileStore struct{ Global, Workspace string }

func ParseProfile(raw []byte) (Profile, error) {
	var p Profile
	d := yaml.NewDecoder(bytes.NewReader(raw))
	d.KnownFields(true)
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return p, errors.New("profile: expected one YAML document")
	}
	return p, p.Validate()
}
func (s ProfileStore) List() []ProfileEntry {
	var result []ProfileEntry
	for _, scope := range []struct{ name, dir string }{{"global", s.Global}, {"workspace", s.Workspace}} {
		if scope.dir == "" {
			continue
		}
		files, err := os.ReadDir(scope.dir)
		if err != nil {
			if !os.IsNotExist(err) {
				result = append(result, ProfileEntry{Path: scope.dir, Scope: scope.name, Err: err})
			}
			continue
		}
		for _, f := range files {
			if f.IsDir() || !(strings.HasSuffix(f.Name(), ".yaml") || strings.HasSuffix(f.Name(), ".yml")) {
				continue
			}
			e := ProfileEntry{Path: filepath.Join(scope.dir, f.Name()), Scope: scope.name}
			e.Raw, e.Err = os.ReadFile(e.Path)
			if e.Err == nil {
				e.Profile, e.Err = ParseProfile(e.Raw)
			}
			result = append(result, e)
		}
	}
	for i := range result {
		for j := range result {
			if i == j || result[i].Profile.Name == "" || result[i].Profile.Name != result[j].Profile.Name {
				continue
			}
			if result[i].Scope == result[j].Scope {
				result[i].Err = errors.New("duplicate profile name in scope")
			} else if result[i].Scope == "global" {
				result[i].Shadowed = true
			}
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Profile.Name < result[j].Profile.Name })
	return result
}
func (s ProfileStore) Get(name string) (ProfileEntry, error) {
	entries := s.List()
	var global *ProfileEntry
	for _, e := range entries {
		if e.Profile.Name == name && !e.Shadowed {
			if e.Scope == "workspace" {
				return e, e.Err
			}
			copy := e
			global = &copy
		}
	}
	// An invalid workspace document whose name could not be decoded may be an
	// override for this profile. Fail closed instead of silently executing a
	// global definition that the workspace may have intended to replace.
	for _, e := range entries {
		if e.Scope == "workspace" && e.Err != nil && e.Profile.Name == "" {
			return ProfileEntry{}, fmt.Errorf(
				"cannot resolve subagent %q while workspace profile %s has an unreadable identity: %w",
				name, e.Path, e.Err,
			)
		}
	}
	if global != nil {
		return *global, global.Err
	}
	return ProfileEntry{}, fmt.Errorf("unknown subagent %q", name)
}
func (s ProfileStore) Save(p Profile, scope string, original *ProfileEntry) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if original != nil && original.Scope != scope {
		if original.Profile.Name != p.Name {
			return errors.New("profile identity cannot change")
		}
		if err := checkProfileOriginal(*original); err != nil {
			return err
		}
		if err := s.Save(p, scope, nil); err != nil {
			return err
		}
		if err := s.Delete(*original); err != nil {
			dir := s.Global
			if scope == "workspace" {
				dir = s.Workspace
			}
			raw, _ := yaml.Marshal(p)
			rollback := s.Delete(ProfileEntry{Path: filepath.Join(dir, p.Name+".yaml"), Raw: raw})
			return errors.Join(fmt.Errorf("move profile: %w", err), rollback)
		}
		return nil
	}
	dir := s.Global
	if scope == "workspace" {
		dir = s.Workspace
	} else if scope != "global" {
		return errors.New("invalid profile scope")
	}
	if dir == "" {
		return errors.New("profile directory unavailable")
	}
	path := filepath.Join(dir, p.Name+".yaml")
	if original != nil {
		if original.Profile.Name != p.Name || original.Scope != scope {
			return errors.New("profile identity cannot change")
		}
		path = original.Path
		if err := checkProfileOriginal(*original); err != nil {
			return err
		}
	} else {
		for _, e := range s.List() {
			if e.Scope == scope && e.Profile.Name == p.Name {
				return errors.New("profile already exists")
			}
		}
		if _, err := os.Stat(path); err == nil {
			return errors.New("profile file already exists")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	raw, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".profile-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func checkProfileOriginal(e ProfileEntry) error {
	raw, err := os.ReadFile(e.Path)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, e.Raw) {
		return errors.New("profile changed externally; reload before saving")
	}
	return nil
}
func (s ProfileStore) Delete(e ProfileEntry) error {
	if err := checkProfileOriginal(e); err != nil {
		return err
	}
	return os.Remove(e.Path)
}
