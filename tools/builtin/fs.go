package builtin

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/snowmerak/q/internal/fsreplace"
)

const codePath = "E_PATH"

// FS is a filesystem view jailed to its primary Root and any explicitly
// configured additional roots.
type FS struct {
	Root         string
	allowedRoots []string
	mu           sync.Mutex
	commands     *commandRegistry
}

// NewFS constructs a root-jailed filesystem. root must exist and be a
// directory. Root is made absolute and symlinks are evaluated once here.
func NewFS(root string) (*FS, error) {
	return NewFSWithRoots(root, nil)
}

// NewFSWithRoots extends the root jail with explicitly allowed additional
// roots. Relative paths continue to resolve from the primary root; callers
// select an additional root with an absolute path.
func NewFSWithRoots(root string, additional []string) (*FS, error) {
	primary, err := canonicalDirectory(root)
	if err != nil {
		return nil, err
	}
	roots := []string{primary}
	for _, candidate := range additional {
		candidate, err = canonicalDirectory(candidate)
		if err != nil {
			return nil, err
		}
		duplicate := false
		for _, existing := range roots {
			if samePath(existing, candidate) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			roots = append(roots, candidate)
		}
	}
	return &FS{Root: primary, allowedRoots: roots, commands: newCommandRegistry(primary)}, nil
}

func canonicalDirectory(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("builtin: empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("builtin: absolute root: %w", err)
	}
	evaluated, err := filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		return "", fmt.Errorf("builtin: evaluate root: %w", err)
	}
	info, err := os.Stat(evaluated)
	if err != nil {
		return "", fmt.Errorf("builtin: stat root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("builtin: root is not a directory")
	}
	return filepath.Clean(evaluated), nil
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

func samePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if filepath.Separator == '\\' {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (fs *FS) allowedRoot(candidate string) (string, bool) {
	matched := ""
	for _, root := range fs.allowedRoots {
		if insideRoot(root, candidate) && len(root) > len(matched) {
			matched = root
		}
	}
	return matched, matched != ""
}

func (fs *FS) isAllowedRoot(candidate string) bool {
	for _, root := range fs.allowedRoots {
		if samePath(root, candidate) {
			return true
		}
	}
	return false
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
	if _, ok := fs.allowedRoot(candidate); !ok {
		return "", fmt.Errorf("[%s] path escapes workspace root", codePath)
	}
	return candidate, nil
}

// resolveExisting follows symlinks and requires the final target to remain in
// the workspace. It is used by reads and content edits.
func (fs *FS) resolveExisting(userPath string) (string, error) {
	path, _, err := fs.resolveExistingRoot(userPath)
	return path, err
}

func (fs *FS) resolveExistingRoot(userPath string) (string, string, error) {
	candidate, err := fs.cleanJoin(userPath)
	if err != nil {
		return "", "", err
	}
	evaluated, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", "", fmt.Errorf("[%s] %v", codePath, err)
	}
	root, ok := fs.allowedRoot(evaluated)
	if !ok {
		return "", "", fmt.Errorf("[%s] path escapes workspace root via symlink", codePath)
	}
	return evaluated, root, nil
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
			if _, ok := fs.allowedRoot(evaluated); !ok {
				return "", fmt.Errorf("[%s] path escapes workspace root via symlink", codePath)
			}
			rest, relErr := filepath.Rel(ancestor, candidate)
			if relErr != nil {
				return "", fmt.Errorf("[%s] %v", codePath, relErr)
			}
			resolved := filepath.Clean(filepath.Join(evaluated, rest))
			if _, ok := fs.allowedRoot(resolved); !ok {
				return "", fmt.Errorf("[%s] path escapes workspace root", codePath)
			}
			return resolved, nil
		}
		if fs.isAllowedRoot(ancestor) {
			break
		}
		if _, ok := fs.allowedRoot(ancestor); !ok {
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
	if fs.isAllowedRoot(candidate) {
		return candidate, nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil {
		return "", fmt.Errorf("[%s] %v", codePath, err)
	}
	if _, ok := fs.allowedRoot(parent); !ok {
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
	if err := fsreplace.Replace(tempPath, path); err != nil {
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
	defer func() { _ = in.Close() }()
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
