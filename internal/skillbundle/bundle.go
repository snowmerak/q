// Package skillbundle stores Library-owned, content-addressed skill resources.
package skillbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/snowmerak/q/internal/fsreplace"
)

const directory = "library/builtin-skills"

type Bundle struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
}

type Manifest struct {
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

var versionPattern = regexp.MustCompile(`^[0-9]{14}\.[0-9]{4}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (b Bundle) Manifest() (Manifest, error) {
	if !versionPattern.MatchString(b.Version) || len(b.Files) == 0 || len(b.Files) > 32 {
		return Manifest{}, errors.New("invalid bundled skill version or file count")
	}
	if !strings.HasPrefix(b.Files["SKILL.md"], "---\nname: q-cdp\n") {
		return Manifest{}, errors.New("bundled skill must be named q-cdp")
	}
	size := 0
	for path, content := range b.Files {
		if !fs.ValidPath(path) || strings.ContainsAny(path, "\\:") || strings.HasPrefix(path, ".") {
			return Manifest{}, fmt.Errorf("invalid bundled skill resource %q", path)
		}
		size += len(path) + len(content)
	}
	if size > 48<<10 {
		return Manifest{}, errors.New("bundled skill resources exceed size limit")
	}
	data, err := json.Marshal(b)
	if err != nil {
		return Manifest{}, err
	}
	digest := sha256.Sum256(data)
	return Manifest{Version: b.Version, Digest: hex.EncodeToString(digest[:])}, nil
}

func Current(configDir string) (Manifest, error) {
	root, err := os.OpenRoot(configDir)
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile(directory + "/current.json")
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	if !versionPattern.MatchString(manifest.Version) || !digestPattern.MatchString(manifest.Digest) {
		return Manifest{}, errors.New("invalid bundled skill manifest")
	}
	return manifest, nil
}

// Location resolves one immutable generation; callers can keep reading old
// resource paths while the current-generation pointer is atomically replaced.
func Location(configDir string) (string, error) {
	manifest, err := Current(configDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, filepath.FromSlash(directory), manifest.Digest), nil
}

// Install is serialized by Library's skill service. Older Q processes cannot
// downgrade resources installed by a newer binary. User skill roots are untouched.
func Install(configDir string, bundle Bundle) (Manifest, error) {
	manifest, err := bundle.Manifest()
	if err != nil {
		return Manifest{}, err
	}
	current, err := Current(configDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, err
	}
	if current.Version > manifest.Version {
		return current, nil
	}
	if current.Version == manifest.Version && current.Digest != manifest.Digest {
		return Manifest{}, errors.New("bundled skill content changed without a bundle version change")
	}
	root, err := os.OpenRoot(configDir)
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = root.Close() }()
	for path, content := range bundle.Files {
		name := directory + "/" + manifest.Digest + "/q-cdp/" + path
		if err := root.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			return Manifest{}, err
		}
		stored, readErr := root.ReadFile(name)
		if readErr == nil && string(stored) == content {
			continue
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return Manifest{}, readErr
		}
		if err := root.WriteFile(name, []byte(content), 0o600); err != nil {
			return Manifest{}, err
		}
	}
	if current == manifest {
		return manifest, nil
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, err
	}
	// Resolve the parent through Root before using the platform's atomic replace.
	parent, err := root.OpenRoot(directory)
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = parent.Close() }()
	if err := fsreplace.WriteFile(filepath.Join(parent.Name(), "current.json"), data, 0o600); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}
