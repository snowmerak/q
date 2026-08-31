package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const GitIgnoreFileName = ".gitignore"

// EnsureQGitIgnored adds q's workspace metadata directory to the repository
// root .gitignore when the workspace belongs to a Git work tree. Existing
// ignore rules that already cover the directory are left unchanged.
func (s Store) EnsureQGitIgnored(ctx context.Context) error {
	repositoryRoot, found, err := gitRepositoryRoot(ctx, s.Root)
	if err != nil || !found {
		return err
	}

	relativeWorkspace, err := filepath.Rel(repositoryRoot, s.Root)
	if err != nil {
		return fmt.Errorf("workspace: resolve %s relative to Git root: %w", s.Root, err)
	}
	if relativeWorkspace == ".." || strings.HasPrefix(relativeWorkspace, ".."+string(filepath.Separator)) {
		return fmt.Errorf("workspace: %s is outside Git root %s", s.Root, repositoryRoot)
	}
	return ensureQGitIgnore(filepath.Join(repositoryRoot, GitIgnoreFileName))
}

func gitRepositoryRoot(ctx context.Context, root string) (string, bool, error) {
	command := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", false, ctxErr
		}
		var exitError *exec.ExitError
		if errors.As(err, &exitError) || errors.Is(err, exec.ErrNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("workspace: find Git root: %w", err)
	}
	repositoryRoot := strings.TrimSpace(string(output))
	if repositoryRoot == "" {
		return "", false, errors.New("workspace: Git returned an empty repository root")
	}
	return repositoryRoot, true, nil
}

func ensureQGitIgnore(path string) error {
	body, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("workspace: read %s: %w", path, err)
	}
	if hasQGitIgnoreRule(body) {
		return nil
	}
	permission := os.FileMode(0o644)
	if err == nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return fmt.Errorf("workspace: inspect %s: %w", path, statErr)
		}
		permission = info.Mode().Perm()
	}

	lineEnding := []byte("\n")
	if bytes.Contains(body, []byte("\r\n")) {
		lineEnding = []byte("\r\n")
	}
	updated := append([]byte(nil), body...)
	if len(updated) > 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, lineEnding...)
	}
	updated = append(updated, []byte(DirectoryName)...)
	updated = append(updated, lineEnding...)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, permission)
	if err != nil {
		return fmt.Errorf("workspace: open %s for append: %w", path, err)
	}
	if _, err := file.Write(updated[len(body):]); err != nil {
		_ = file.Close()
		return fmt.Errorf("workspace: append %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("workspace: sync %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("workspace: close %s: %w", path, err)
	}
	return nil
}

func hasQGitIgnoreRule(body []byte) bool {
	for _, rawLine := range bytes.Split(body, []byte{'\n'}) {
		line := strings.TrimSuffix(string(rawLine), "\r")
		switch line {
		case DirectoryName, DirectoryName + "/", "**/" + DirectoryName, "**/" + DirectoryName + "/":
			return true
		}
	}
	return false
}
