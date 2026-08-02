package builtin

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const codePath = "E_PATH"

// FS is a filesystem view jailed to Root.
type FS struct {
	Root     string
	mu       sync.Mutex
	commands *commandRegistry
}

// NewFS constructs a root-jailed filesystem. root must exist and be a
// directory. Root is made absolute and symlinks are evaluated once here.
func NewFS(root string) (*FS, error) {
	if root == "" {
		return nil, fmt.Errorf("builtin: empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("builtin: absolute root: %w", err)
	}
	evaluated, err := filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		return nil, fmt.Errorf("builtin: evaluate root: %w", err)
	}
	info, err := os.Stat(evaluated)
	if err != nil {
		return nil, fmt.Errorf("builtin: stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("builtin: root is not a directory")
	}
	return &FS{Root: evaluated, commands: newCommandRegistry(evaluated)}, nil
}

func (fs *FS) Close() {
	fs.commands.Close()
}

func insideRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (fs *FS) cleanJoin(userPath string) (string, error) {
	if userPath == "" {
		return "", fmt.Errorf("[%s] empty path", codePath)
	}
	var candidate string
	if filepath.IsAbs(userPath) {
		candidate = filepath.Clean(userPath)
	} else {
		candidate = filepath.Clean(filepath.Join(fs.Root, userPath))
	}
	if !insideRoot(fs.Root, candidate) {
		return "", fmt.Errorf("[%s] path escapes workspace root", codePath)
	}
	return candidate, nil
}

// resolveExisting follows symlinks and requires the final target to remain in
// the workspace. It is used by reads and content edits.
func (fs *FS) resolveExisting(userPath string) (string, error) {
	candidate, err := fs.cleanJoin(userPath)
	if err != nil {
		return "", err
	}
	evaluated, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("[%s] %v", codePath, err)
	}
	if !insideRoot(fs.Root, evaluated) {
		return "", fmt.Errorf("[%s] path escapes workspace root via symlink", codePath)
	}
	return evaluated, nil
}

// resolveWritePath permits a missing leaf while requiring its nearest existing
// ancestor to resolve inside the workspace.
func (fs *FS) resolveWritePath(userPath string) (string, error) {
	candidate, err := fs.cleanJoin(userPath)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(candidate); err == nil {
		return fs.resolveExisting(userPath)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("[%s] %v", codePath, err)
	}

	ancestor := filepath.Dir(candidate)
	for {
		if info, statErr := os.Stat(ancestor); statErr == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("[%s] ancestor is not a directory", codePath)
			}
			evaluated, evalErr := filepath.EvalSymlinks(ancestor)
			if evalErr != nil {
				return "", fmt.Errorf("[%s] %v", codePath, evalErr)
			}
			if !insideRoot(fs.Root, evaluated) {
				return "", fmt.Errorf("[%s] path escapes workspace root via symlink", codePath)
			}
			rest, relErr := filepath.Rel(ancestor, candidate)
			if relErr != nil {
				return "", fmt.Errorf("[%s] %v", codePath, relErr)
			}
			resolved := filepath.Clean(filepath.Join(evaluated, rest))
			if !insideRoot(fs.Root, resolved) {
				return "", fmt.Errorf("[%s] path escapes workspace root", codePath)
			}
			return resolved, nil
		}
		if ancestor == fs.Root || !insideRoot(fs.Root, ancestor) {
			break
		}
		ancestor = filepath.Dir(ancestor)
	}
	return "", fmt.Errorf("[%s] no existing ancestor inside workspace root", codePath)
}

// resolveEntry evaluates the parent but not the leaf. This lets remove and
// rename operate on a symlink entry rather than on its target.
func (fs *FS) resolveEntry(userPath string) (string, error) {
	candidate, err := fs.cleanJoin(userPath)
	if err != nil {
		return "", err
	}
	if candidate == fs.Root {
		return fs.Root, nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil {
		return "", fmt.Errorf("[%s] %v", codePath, err)
	}
	if !insideRoot(fs.Root, parent) {
		return "", fmt.Errorf("[%s] path escapes workspace root via symlink", codePath)
	}
	entry := filepath.Join(parent, filepath.Base(candidate))
	if _, err := os.Lstat(entry); err != nil {
		return "", fmt.Errorf("[%s] %v", codePath, err)
	}
	return entry, nil
}

func splitLines(data string) []string { return strings.Split(data, "\n") }
func joinLines(lines []string) string { return strings.Join(lines, "\n") }

func readFileLines(path string) ([]string, os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("[%s] %v", codePath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("[%s] not a regular file", codePath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("[%s] %v", codePath, err)
	}
	if strings.IndexByte(string(data), 0) >= 0 {
		return nil, 0, fmt.Errorf("[E_BINARY_FILE] file contains NUL bytes")
	}
	return splitLines(string(data)), info.Mode().Perm(), nil
}

func atomicWriteFile(path string, body []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create parent directories: %w", err)
	}
	if perm == 0 {
		perm = 0o644
	}
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		perm = info.Mode().Perm()
	}

	temp, err := os.CreateTemp(dir, ".q-write-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tempPath := temp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Chmod(tempPath, perm); err != nil {
		return fmt.Errorf("set temporary file mode: %w", err)
	}
	if err := replaceFile(tempPath, path); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	keep = true
	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}

func copyRegularFile(source, destination string, perm os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}
