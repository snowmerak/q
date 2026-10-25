package composer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const MaximumArtifactSize = 256 << 10
const maximumArtifactFiles = 128

var ErrArtifactPath = errors.New("artifact path must be a .d2 file below docs/diagram/ or an .html file below docs/webcomponent/")
var ErrArtifactConflict = errors.New("artifact changed; reload its source before saving")

type Artifact struct {
	Path      string    `json:"path"`
	Kind      string    `json:"kind"`
	Hash      string    `json:"hash"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
	Content   string    `json:"content,omitempty"`
}

// Artifacts owns only the two source directories below one Composer root.
// All operations stay beneath directory handles; symlinks are not artifacts.
type Artifacts struct {
	Root string
	mu   sync.Mutex
}

func artifactPath(name string) (directory, relative, kind string, err error) {
	if strings.ContainsAny(name, "\\:\x00") || path.Clean(name) != name {
		return "", "", "", ErrArtifactPath
	}
	for _, candidate := range []struct{ dir, ext, kind string }{{"docs/diagram", ".d2", "d2"}, {"docs/webcomponent", ".html", "html"}} {
		if strings.HasPrefix(name, candidate.dir+"/") && strings.HasSuffix(name, candidate.ext) {
			rel := strings.TrimPrefix(name, candidate.dir+"/")
			if fs.ValidPath(rel) && strings.TrimSuffix(path.Base(rel), candidate.ext) != "" {
				return candidate.dir, rel, candidate.kind, nil
			}
		}
	}
	return "", "", "", ErrArtifactPath
}

func (a *Artifacts) open(name string, create bool) (*os.Root, string, string, error) {
	directory, relative, kind, err := artifactPath(name)
	if err != nil {
		return nil, "", "", err
	}
	root, err := os.OpenRoot(a.Root)
	if err != nil {
		return nil, "", "", err
	}
	defer func() { _ = root.Close() }()
	for _, part := range []string{"docs", directory} {
		info, err := root.Lstat(filepath.FromSlash(part))
		if errors.Is(err, os.ErrNotExist) && create {
			if err := root.Mkdir(filepath.FromSlash(part), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return nil, "", "", err
			}
			info, err = root.Lstat(filepath.FromSlash(part))
		}
		if err != nil {
			return nil, "", "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, "", "", ErrArtifactPath
		}
	}
	category, err := root.OpenRoot(filepath.FromSlash(directory))
	if err != nil {
		return nil, "", "", err
	}
	parts := strings.Split(relative, "/")
	for i := range parts {
		info, err := category.Lstat(filepath.FromSlash(strings.Join(parts[:i+1], "/")))
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			_ = category.Close()
			return nil, "", "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			_ = category.Close()
			return nil, "", "", ErrArtifactPath
		}
	}
	return category, filepath.FromSlash(relative), kind, nil
}

func hashContent(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

func (a *Artifacts) Read(name string) (Artifact, error) {
	root, relative, kind, err := a.open(name, false)
	if err != nil {
		return Artifact{}, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(relative)
	if err != nil {
		return Artifact{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return Artifact{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaximumArtifactSize {
		return Artifact{}, errors.New("artifact must be a regular file of at most 256 KiB")
	}
	body, err := io.ReadAll(io.LimitReader(file, MaximumArtifactSize+1))
	if err != nil {
		return Artifact{}, err
	}
	if len(body) > MaximumArtifactSize || !utf8.Valid(body) {
		return Artifact{}, errors.New("artifact must be UTF-8 text of at most 256 KiB")
	}
	return Artifact{Path: name, Kind: kind, Content: string(body), Hash: hashContent(body), Size: int64(len(body)), UpdatedAt: info.ModTime().UTC()}, nil
}

func (a *Artifacts) List() ([]Artifact, error) {
	result := []Artifact{}
	for _, category := range []struct{ directory, example string }{{"docs/diagram", "placeholder.d2"}, {"docs/webcomponent", "placeholder.html"}} {
		directory := category.directory
		root, _, _, err := a.open(directory+"/"+category.example, false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		walkErr := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return ErrArtifactPath
			}
			if entry.IsDir() {
				return nil
			}
			full := directory + "/" + name
			if _, _, _, err := artifactPath(full); err != nil {
				return nil
			}
			if len(result) >= maximumArtifactFiles {
				return errors.New("composer supports at most 128 artifacts")
			}
			value, err := a.Read(full)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			value.Content = ""
			result = append(result, value)
			return nil
		})
		_ = root.Close()
		if walkErr != nil {
			return nil, walkErr
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

// Write preserves valid source even when it does not yet render. expectedHash
// is optional for model calls and required by the editor to prevent lost edits.
func (a *Artifacts) Write(name, content string, expectedHash *string) (Artifact, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(content) > MaximumArtifactSize || !utf8.ValidString(content) {
		return Artifact{}, errors.New("artifact must be UTF-8 text of at most 256 KiB")
	}
	if _, _, _, err := artifactPath(name); err != nil {
		return Artifact{}, err
	}
	current, err := a.Read(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Artifact{}, err
	}
	if expectedHash != nil && *expectedHash != current.Hash {
		return Artifact{}, ErrArtifactConflict
	}
	if errors.Is(err, os.ErrNotExist) {
		listed, listErr := a.List()
		if listErr != nil {
			return Artifact{}, listErr
		}
		if len(listed) >= maximumArtifactFiles {
			return Artifact{}, errors.New("composer supports at most 128 artifacts")
		}
	}
	root, relative, _, err := a.open(name, true)
	if err != nil {
		return Artifact{}, err
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(filepath.Dir(relative), 0o700); err != nil {
		return Artifact{}, err
	}
	temporary := filepath.Join(filepath.Dir(relative), ".composer-"+uuid.NewString()+".tmp")
	defer func() { _ = root.Remove(temporary) }()
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Artifact{}, err
	}
	_, writeErr := file.WriteString(content)
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err != nil {
		return Artifact{}, err
	}
	for attempt := 0; ; attempt++ {
		err = root.Rename(temporary, relative)
		if err == nil || runtime.GOOS != "windows" || attempt >= 6 {
			break
		}
		time.Sleep(10 * time.Millisecond << attempt)
	}
	if err != nil {
		return Artifact{}, fmt.Errorf("save artifact: %w", err)
	}
	return a.Read(name)
}

func (a *Artifacts) Delete(name string, expectedHash *string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	current, err := a.Read(name)
	if err != nil {
		return err
	}
	if expectedHash != nil && current.Hash != *expectedHash {
		return ErrArtifactConflict
	}
	root, relative, _, err := a.open(name, false)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.Remove(relative)
}
