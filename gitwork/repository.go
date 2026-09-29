package gitwork

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepositoryRoot reports the Git work tree containing directory. A directory
// outside Git is a valid result with found=false; Git failures inside a work
// tree remain errors so callers do not silently bypass repository isolation.
func RepositoryRoot(ctx context.Context, directory string) (root string, found bool, err error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", false, fmt.Errorf("gitwork: resolve directory: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", false, fmt.Errorf("gitwork: resolve directory links: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", false, fmt.Errorf("gitwork: inspect directory: %w", err)
	}
	if !info.IsDir() {
		return "", false, errors.New("gitwork: working directory is not a directory")
	}
	command := exec.CommandContext(ctx, "git", "-C", canonical, "rev-parse", "--show-toplevel")
	output, commandErr := command.CombinedOutput()
	if commandErr == nil {
		root = strings.TrimSpace(string(output))
		if root == "" {
			return "", false, errors.New("gitwork: Git returned an empty repository root")
		}
		return filepath.Clean(root), true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", false, ctxErr
	}
	if errors.Is(commandErr, exec.ErrNotFound) {
		return "", false, fmt.Errorf("gitwork: run Git: %w", commandErr)
	}
	if containsGitMarker(canonical) {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = commandErr.Error()
		}
		return "", false, fmt.Errorf("gitwork: inspect repository: %s", detail)
	}
	return "", false, nil
}

func containsGitMarker(directory string) bool {
	for current := filepath.Clean(directory); ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
	}
}
