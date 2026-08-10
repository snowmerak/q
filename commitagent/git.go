// Package commitagent implements q's isolated commit-message agent and Git
// execution pipeline.
package commitagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

var ErrNothingToCommit = errors.New("nothing to commit")

type repositoryState struct {
	root             string
	files            []string
	visibleFiles     []string
	lockFiles        []string
	stat             string
	numstat          string
	fullDiff         string
	fileDiffs        map[string]string
	contextFiles     []contextFile
	changelogTargets []string
	scopeCandidates  []string
	autoStaged       bool
}

type contextFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type trivialChange struct {
	Message string
	Found   bool
}

func prepareRepository(ctx context.Context, directory string) (repositoryState, error) {
	root, err := repositoryRoot(ctx, directory)
	if err != nil {
		return repositoryState{}, err
	}

	files, err := stagedFiles(ctx, root)
	if err != nil {
		return repositoryState{}, err
	}
	autoStaged := false
	if len(files) == 0 {
		autoStaged, err = stageWorkingTree(ctx, root)
		if err != nil {
			return repositoryState{}, err
		}
		files, err = stagedFiles(ctx, root)
		if err != nil {
			return repositoryState{}, err
		}
	}
	if len(files) == 0 {
		return repositoryState{}, ErrNothingToCommit
	}

	state := repositoryState{
		root: root, files: files, fileDiffs: make(map[string]string, len(files)), autoStaged: autoStaged,
	}
	for _, path := range files {
		if isLockFile(path) {
			state.lockFiles = append(state.lockFiles, path)
		} else {
			state.visibleFiles = append(state.visibleFiles, path)
		}
		diff, diffErr := gitOutput(ctx, root, "diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--", path)
		if diffErr != nil {
			return repositoryState{}, fmt.Errorf("q commit: read staged diff for %s: %w", path, diffErr)
		}
		state.fileDiffs[path] = diff
	}
	state.fullDiff, err = gitOutput(ctx, root, "diff", "--cached", "--binary", "--full-index", "--no-ext-diff")
	if err != nil {
		return repositoryState{}, fmt.Errorf("q commit: read staged diff: %w", err)
	}
	state.stat, err = gitDiffForPaths(ctx, root, state.visibleFiles, "--stat")
	if err != nil {
		return repositoryState{}, fmt.Errorf("q commit: read staged stat: %w", err)
	}
	state.numstat, err = gitDiffForPaths(ctx, root, state.visibleFiles, "--numstat")
	if err != nil {
		return repositoryState{}, fmt.Errorf("q commit: read staged numstat: %w", err)
	}
	state.contextFiles = collectContextFiles(root, files)
	state.changelogTargets = findChangelogTargets(root, files)
	state.scopeCandidates = inferScopes(state.visibleFiles)
	return state, nil
}

func stageWorkingTree(ctx context.Context, root string) (bool, error) {
	tracked, err := gitBytes(ctx, root, nil, "diff", "--name-only", "-z", "--diff-filter=ACDMRTUXB")
	if err != nil {
		return false, fmt.Errorf("q commit: list tracked working-tree changes: %w", err)
	}
	untracked, err := gitBytes(ctx, root, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return false, fmt.Errorf("q commit: list untracked working-tree changes: %w", err)
	}
	seen := make(map[string]struct{})
	paths := make([]string, 0)
	for _, body := range [][]byte{tracked, untracked} {
		for _, value := range bytes.Split(body, []byte{0}) {
			if len(value) == 0 {
				continue
			}
			path := filepath.ToSlash(string(value))
			if path == ".q" || strings.HasPrefix(path, ".q/") {
				continue
			}
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return false, nil
	}
	sort.Strings(paths)
	pathspecs := make([]byte, 0)
	for _, path := range paths {
		pathspecs = append(pathspecs, path...)
		pathspecs = append(pathspecs, 0)
	}
	if _, err := gitBytes(
		ctx, root, bytes.NewReader(pathspecs), "--literal-pathspecs", "add", "-A",
		"--pathspec-from-file=-", "--pathspec-file-nul",
	); err != nil {
		return false, fmt.Errorf("q commit: stage changes: %w", err)
	}
	return true, nil
}

func repositoryRoot(ctx context.Context, directory string) (string, error) {
	root, err := gitOutput(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("q commit: find repository: %w", err)
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("q commit: Git returned an empty repository root")
	}
	return filepath.Clean(root), nil
}

func stagedFiles(ctx context.Context, root string) ([]string, error) {
	body, err := gitBytes(ctx, root, nil, "diff", "--cached", "--name-only", "-z", "--diff-filter=ACDMRTUXB")
	if err != nil {
		return nil, fmt.Errorf("q commit: list staged files: %w", err)
	}
	parts := bytes.Split(body, []byte{0})
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			files = append(files, filepath.ToSlash(string(part)))
		}
	}
	sort.Strings(files)
	return files, nil
}

func gitDiffForPaths(ctx context.Context, root string, paths []string, mode string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	arguments := []string{"diff", "--cached", mode, "--no-ext-diff", "--"}
	arguments = append(arguments, paths...)
	return gitOutput(ctx, root, arguments...)
}

func detectTrivialChange(ctx context.Context, state repositoryState) (trivialChange, error) {
	command := exec.CommandContext(ctx, "git", "-C", state.root, "diff", "--cached", "--quiet", "-w", "--no-ext-diff")
	err := command.Run()
	if err == nil {
		return trivialChange{Message: "style: formatted code", Found: true}, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		return trivialChange{}, fmt.Errorf("q commit: inspect whitespace changes: %w", err)
	}
	if importsOnly(state.fullDiff) {
		return trivialChange{Message: "style: reorganized imports", Found: true}, nil
	}
	return trivialChange{}, nil
}

func importsOnly(diff string) bool {
	var removed, added []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") || len(line) == 0 {
			continue
		}
		var value string
		switch line[0] {
		case '-':
			value = strings.TrimSpace(line[1:])
			if value != "" {
				removed = append(removed, value)
			}
		case '+':
			value = strings.TrimSpace(line[1:])
			if value != "" {
				added = append(added, value)
			}
		default:
			continue
		}
		if value != "" && !looksLikeImport(value) {
			return false
		}
	}
	if len(removed) == 0 || len(removed) != len(added) {
		return false
	}
	sort.Strings(removed)
	sort.Strings(added)
	return strings.Join(removed, "\x00") == strings.Join(added, "\x00")
}

func looksLikeImport(line string) bool {
	if line == "(" || line == ")" || strings.HasPrefix(line, "//") {
		return true
	}
	prefixes := []string{"import ", "import(", "from ", "use ", "using ", "require(", "#include ", "@import ", "export {"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return strings.HasPrefix(line, "\"") || strings.HasPrefix(line, "`")
}

func isLockFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, "-lock.json") || strings.HasSuffix(name, "-lock.yaml") || strings.HasSuffix(name, "-lock.yml") {
		return true
	}
	switch name {
	case "go.sum", "cargo.lock", "composer.lock", "gemfile.lock", "podfile.lock", "packages.lock.json":
		return true
	default:
		return false
	}
}

func inferScopes(files []string) []string {
	counts := make(map[string]int)
	for _, path := range files {
		parts := strings.Split(filepath.ToSlash(path), "/")
		scope := strings.TrimSuffix(parts[0], filepath.Ext(parts[0]))
		if len(parts) == 1 {
			scope = "repo"
		}
		if scope != "" {
			counts[scope]++
		}
	}
	type scored struct {
		name  string
		count int
	}
	values := make([]scored, 0, len(counts))
	for name, count := range counts {
		values = append(values, scored{name, count})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].count == values[j].count {
			return values[i].name < values[j].name
		}
		return values[i].count > values[j].count
	})
	result := make([]string, 0, min(6, len(values)))
	for _, value := range values {
		result = append(result, value.name)
		if len(result) == 6 {
			break
		}
	}
	return result
}

func collectContextFiles(root string, changed []string) []contextFile {
	candidates := map[string]struct{}{
		"AGENTS.md": {}, "CONTRIBUTING.md": {}, ".github/CONTRIBUTING.md": {},
	}
	for _, path := range changed {
		directory := filepath.Dir(filepath.FromSlash(path))
		for directory != "." && directory != "" {
			candidates[filepath.ToSlash(filepath.Join(directory, "AGENTS.md"))] = struct{}{}
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
			directory = parent
		}
	}
	paths := make([]string, 0, len(candidates))
	for path := range candidates {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]contextFile, 0, len(paths))
	for _, path := range paths {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			continue
		}
		if len(body) > 64<<10 {
			body = body[:64<<10]
		}
		result = append(result, contextFile{Path: path, Content: string(body)})
	}
	return result
}

func findChangelogTargets(root string, changed []string) []string {
	seen := make(map[string]struct{})
	for _, path := range changed {
		name := strings.ToLower(filepath.Base(path))
		if strings.HasPrefix(name, "changelog") || strings.HasPrefix(name, "changes") {
			seen[path] = struct{}{}
		}
	}
	for _, path := range []string{"CHANGELOG.md", "CHANGES.md", "docs/CHANGELOG.md"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err == nil {
			seen[path] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for path := range seen {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func gitOutput(ctx context.Context, root string, arguments ...string) (string, error) {
	body, err := gitBytes(ctx, root, nil, arguments...)
	return string(body), err
}

func gitBytes(ctx context.Context, root string, input io.Reader, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...)
	command.Stdin = input
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message != "" {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, message)
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(arguments, " "), err)
	}
	return stdout.Bytes(), nil
}
