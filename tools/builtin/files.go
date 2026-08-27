package builtin

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type WriteFileInput struct {
	Path    string `json:"path" jsonschema:"Workspace-relative or in-workspace absolute destination path."`
	Content string `json:"content" jsonschema:"Complete literal file content."`
}

type WriteFileOutput struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

func (fs *FS) WriteFile(input WriteFileInput) (WriteFileOutput, error) {
	path, err := fs.resolveWritePath(input.Path)
	if err != nil {
		return WriteFileOutput{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	permission := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return WriteFileOutput{}, fmt.Errorf("[E_PATH] destination is not a regular file")
		}
		permission = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return WriteFileOutput{}, err
	}
	if err := atomicWriteFile(path, []byte(input.Content), permission); err != nil {
		return WriteFileOutput{}, err
	}
	return WriteFileOutput{Path: input.Path, Bytes: len([]byte(input.Content))}, nil
}

type ListDirectoryInput struct {
	Path string `json:"path,omitempty" jsonschema:"Directory path inside the workspace. Defaults to the workspace root."`
}

type DirectoryEntry struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Type     string    `json:"type"`
	Size     int64     `json:"size,omitempty"`
	Modified time.Time `json:"modified"`
}

type ListDirectoryOutput struct {
	Path    string           `json:"path"`
	Entries []DirectoryEntry `json:"entries"`
	Ignored int              `json:"ignored,omitempty"`
}

func (fsys *FS) ListDirectory(input ListDirectoryInput) (ListDirectoryOutput, error) {
	userPath := input.Path
	if userPath == "" {
		userPath = "."
	}
	path, err := fsys.resolveExisting(userPath)
	if err != nil {
		return ListDirectoryOutput{}, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return ListDirectoryOutput{}, err
	}
	ignore, err := loadDiscoveryIgnore(fsys.Root)
	if err != nil {
		return ListDirectoryOutput{}, fmt.Errorf("read %s: %w", workspaceIgnoreFile, err)
	}
	result := make([]DirectoryEntry, 0, len(entries))
	resultIgnored := 0
	for _, entry := range entries {
		relative, err := filepath.Rel(fsys.Root, filepath.Join(path, entry.Name()))
		if err != nil {
			return ListDirectoryOutput{}, err
		}
		if ignore.matches(relative, entry.IsDir()) {
			resultIgnored++
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return ListDirectoryOutput{}, err
		}
		kind := "file"
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			kind = "symlink"
		case entry.IsDir():
			kind = "directory"
		case !entry.Type().IsRegular():
			kind = "other"
		}
		result = append(result, DirectoryEntry{
			Name: entry.Name(), Path: filepath.ToSlash(filepath.Join(userPath, entry.Name())),
			Type: kind, Size: info.Size(), Modified: info.ModTime(),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return ListDirectoryOutput{Path: userPath, Entries: result, Ignored: resultIgnored}, nil
}

type CreateDirectoryInput struct {
	Path    string `json:"path" jsonschema:"Directory path to create inside the workspace."`
	Parents bool   `json:"parents,omitempty" jsonschema:"Deprecated compatibility option; missing parents are always created regardless of this value."`
}

type PathOutput struct {
	Path string `json:"path"`
}

func (fs *FS) CreateDirectory(input CreateDirectoryInput) (PathOutput, error) {
	path, err := fs.resolveWritePath(input.Path)
	if err != nil {
		return PathOutput{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := os.MkdirAll(path, 0o755); err != nil {
		return PathOutput{}, err
	}
	return PathOutput{Path: input.Path}, nil
}

type MovePathInput struct {
	Source      string `json:"source" jsonschema:"Existing source path inside the workspace."`
	Destination string `json:"destination" jsonschema:"New destination path inside the workspace."`
}

type MovePathOutput struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

func (fs *FS) MovePath(input MovePathInput) (MovePathOutput, error) {
	source, err := fs.resolveEntry(input.Source)
	if err != nil {
		return MovePathOutput{}, err
	}
	if source == fs.Root {
		return MovePathOutput{}, fmt.Errorf("[E_PATH] cannot move the workspace root")
	}
	destination, err := fs.resolveWritePath(input.Destination)
	if err != nil {
		return MovePathOutput{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, err := os.Lstat(destination); err == nil {
		return MovePathOutput{}, fmt.Errorf("[E_EXISTS] destination already exists")
	} else if !os.IsNotExist(err) {
		return MovePathOutput{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return MovePathOutput{}, err
	}
	if err := os.Rename(source, destination); err != nil {
		return MovePathOutput{}, err
	}
	return MovePathOutput{Source: input.Source, Destination: input.Destination}, nil
}

type CopyPathInput struct {
	Source      string `json:"source" jsonschema:"Existing regular file or directory inside the workspace."`
	Destination string `json:"destination" jsonschema:"New destination path inside the workspace."`
}

type CopyPathOutput struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Files       int    `json:"files"`
	Directories int    `json:"directories"`
}

func (fsys *FS) CopyPath(input CopyPathInput) (CopyPathOutput, error) {
	source, err := fsys.resolveExisting(input.Source)
	if err != nil {
		return CopyPathOutput{}, err
	}
	destination, err := fsys.resolveWritePath(input.Destination)
	if err != nil {
		return CopyPathOutput{}, err
	}
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	if _, err := os.Lstat(destination); err == nil {
		return CopyPathOutput{}, fmt.Errorf("[E_EXISTS] destination already exists")
	} else if !os.IsNotExist(err) {
		return CopyPathOutput{}, err
	}
	output := CopyPathOutput{Source: input.Source, Destination: input.Destination}
	info, err := os.Stat(source)
	if err != nil {
		return CopyPathOutput{}, err
	}
	if info.Mode().IsRegular() {
		if err := copyRegularFile(source, destination, info.Mode().Perm()); err != nil {
			return CopyPathOutput{}, err
		}
		output.Files = 1
		return output, nil
	}
	if !info.IsDir() {
		return CopyPathOutput{}, fmt.Errorf("[E_PATH] source is not a regular file or directory")
	}
	if insideRoot(source, destination) {
		return CopyPathOutput{}, fmt.Errorf("[E_PATH] cannot copy a directory into itself")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return CopyPathOutput{}, err
	}
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("[E_PATH] copying symlinks is not supported: %s", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if err := os.Mkdir(target, info.Mode().Perm()); err != nil {
				return err
			}
			output.Directories++
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("[E_PATH] copying special files is not supported: %s", rel)
		}
		if err := copyRegularFile(path, target, info.Mode().Perm()); err != nil {
			return err
		}
		output.Files++
		return nil
	})
	if err != nil {
		_ = os.RemoveAll(destination)
		return CopyPathOutput{}, err
	}
	return output, nil
}

type RemovePathInput struct {
	Path      string `json:"path" jsonschema:"Existing path inside the workspace."`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"Remove a non-empty directory tree. Defaults to false."`
}

func (fs *FS) RemovePath(input RemovePathInput) (PathOutput, error) {
	path, err := fs.resolveEntry(input.Path)
	if err != nil {
		return PathOutput{}, err
	}
	if path == fs.Root {
		return PathOutput{}, fmt.Errorf("[E_PATH] cannot remove the workspace root")
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if input.Recursive {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		return PathOutput{}, err
	}
	return PathOutput{Path: input.Path}, nil
}
