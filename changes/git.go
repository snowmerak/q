// Package changes reads repository changes without staging files or changing
// the working tree. Staged and unstaged patches remain separate, including when
// they cancel one another out relative to HEAD.
package changes

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
	"unicode/utf8"
)

const (
	maximumListBytes  = 4 << 20
	maximumPatchBytes = 256 << 10
	maximumPatchLines = 4000
)

type File struct {
	Path    string
	OldPath string
	// Status is Git's two-column index/worktree status, or ?? for untracked.
	Status string
}

type Snapshot struct {
	Root  string
	Files []File
}

type Section struct {
	Title     string
	Patch     string
	Truncated bool
}

type Detail struct {
	Sections []Section
}

// List returns all changes in the enclosing repository, including individual
// untracked files. q-owned metadata is excluded; lock files are not excluded.
func List(ctx context.Context, directory string) (Snapshot, error) {
	rootOutput, _, err := git(ctx, directory, maximumListBytes, "rev-parse", "--show-toplevel")
	if err != nil {
		return Snapshot{}, fmt.Errorf("changes: open Git repository: %w", err)
	}
	root := filepath.Clean(strings.TrimRight(string(rootOutput), "\r\n"))
	body, truncated, err := git(ctx, root, maximumListBytes,
		"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none", "--renames")
	if err != nil {
		return Snapshot{}, err
	}
	if truncated {
		return Snapshot{}, errors.New("changes: file list is too large to display safely")
	}
	files, err := parseStatus(body)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Root: root, Files: files}, nil
}

func parseStatus(body []byte) ([]File, error) {
	var files []File
	for len(body) > 0 {
		end := bytes.IndexByte(body, 0)
		if end < 3 || body[2] != ' ' {
			return nil, errors.New("changes: malformed Git status")
		}
		file := File{Status: string(body[:2]), Path: string(body[3:end])}
		body = body[end+1:]
		if strings.ContainsAny(file.Status, "RC") {
			end = bytes.IndexByte(body, 0)
			if end < 1 {
				return nil, errors.New("changes: missing rename source in Git status")
			}
			// With -z, Git reports destination first and source second.
			file.OldPath, body = string(body[:end]), body[end+1:]
		}
		if !validPath(file.Path) || (file.OldPath != "" && !validPath(file.OldPath)) {
			return nil, errors.New("changes: invalid repository-relative path")
		}
		if metadataPath(file.Path) || (file.OldPath != "" && metadataPath(file.OldPath)) {
			continue
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func validPath(path string) bool {
	if path == "" || strings.ContainsRune(path, 0) || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func metadataPath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".q" || part == ".git" {
			return true
		}
	}
	return false
}

// Read lazily reads only the selected file. Its result is bounded even for
// binary files, very long lines, and generated files with millions of lines.
func Read(ctx context.Context, root string, file File) (Detail, error) {
	if !validPath(file.Path) || (file.OldPath != "" && !validPath(file.OldPath)) || len(file.Status) != 2 {
		return Detail{}, errors.New("changes: invalid file selection")
	}
	if err := ctx.Err(); err != nil {
		return Detail{}, err
	}
	if file.Status == "??" {
		section, err := readUntracked(root, file.Path)
		return Detail{Sections: []Section{section}}, err
	}
	var detail Detail
	appendDiff := func(title string, staged bool) error {
		args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-relative",
			"--find-renames", "--unified=3", "--ignore-submodules=none", "--src-prefix=a/", "--dst-prefix=b/"}
		if staged {
			args = append(args, "--cached")
		}
		args = append(args, "--", file.Path)
		if file.OldPath != "" {
			args = append(args, file.OldPath)
		}
		body, truncated, err := git(ctx, root, maximumPatchBytes, args...)
		if err != nil {
			return err
		}
		patch, truncated := boundPatch(body, truncated)
		if patch != "" {
			detail.Sections = append(detail.Sections, Section{Title: title, Patch: patch, Truncated: truncated})
		}
		return nil
	}
	if strings.ContainsRune(file.Status, 'U') || file.Status == "AA" || file.Status == "DD" {
		// Preserve Git's combined diff for conflicts instead of claiming a
		// two-way comparison against an arbitrary side of the merge.
		err := appendDiff("UNMERGED · combined diff", false)
		return detail, err
	}
	if file.Status[0] != ' ' {
		if err := appendDiff("STAGED · HEAD → index", true); err != nil {
			return Detail{}, err
		}
	}
	if file.Status[1] != ' ' {
		if err := appendDiff("UNSTAGED · index → working tree", false); err != nil {
			return Detail{}, err
		}
	}
	return detail, nil
}

func readUntracked(root, path string) (Section, error) {
	section := Section{Title: "UNTRACKED · new file"}
	// Root keeps even concurrently replaced symlinks inside the repository.
	fs, err := os.OpenRoot(root)
	if err != nil {
		return section, err
	}
	defer fs.Close()
	path = filepath.FromSlash(path)
	info, err := fs.Lstat(path)
	if err != nil {
		return section, fmt.Errorf("changes: read file (refresh if it changed): %w", err)
	}
	var body []byte
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := fs.Readlink(path)
		if err != nil {
			return section, err
		}
		body = []byte(target)
		section.Title += " · symlink target"
	} else if info.Mode().IsRegular() {
		file, err := fs.Open(path)
		if err != nil {
			return section, err
		}
		defer file.Close()
		body, err = io.ReadAll(io.LimitReader(file, maximumPatchBytes+1))
		if err != nil {
			return section, err
		}
	} else {
		section.Patch = "Non-regular file; content is not displayed."
		return section, nil
	}
	truncated := len(body) > maximumPatchBytes
	if truncated {
		body = body[:maximumPatchBytes]
		// The read limit may split a UTF-8 rune. Keep that from turning a
		// large text file into a misleading binary-file notice.
		for n := 0; n < utf8.UTFMax-1 && len(body) > 0 && !utf8.Valid(body); n++ {
			body = body[:len(body)-1]
		}
	}
	if bytes.ContainsRune(body, 0) || !utf8.Valid(body) {
		section.Patch = fmt.Sprintf("Binary file (%d bytes); content is not displayed.", info.Size())
		return section, nil
	}
	content, truncated := boundPatch(body, truncated)
	section.Truncated = truncated
	if content == "" {
		section.Patch = "Empty file."
		return section, nil
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	var patch strings.Builder
	fmt.Fprintf(&patch, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, line := range lines {
		patch.WriteByte('+')
		patch.WriteString(strings.TrimSuffix(line, "\r"))
		patch.WriteByte('\n')
	}
	if !strings.HasSuffix(content, "\n") && !truncated {
		patch.WriteString("\\ No newline at end of file\n")
	}
	patchBody := []byte(patch.String())
	if len(patchBody) > maximumPatchBytes {
		patchBody, section.Truncated = patchBody[:maximumPatchBytes], true
	}
	section.Patch, section.Truncated = boundPatch(patchBody, section.Truncated)
	return section, nil
}

func boundPatch(body []byte, truncated bool) (string, bool) {
	// Keep a partial final line when the byte limit lands inside a very long
	// line. Dropping it could discard the entire preview after the Git header.
	lines := 0
	for index, value := range body {
		if value == '\n' {
			lines++
			if lines == maximumPatchLines && index+1 < len(body) {
				body, truncated = body[:index+1], true
				break
			}
		}
	}
	return string(body), truncated
}

// A capped writer drains stdout even after reaching its limit, so Git cannot
// block on a full pipe and the child can still be cancelled by its context.
type cappedBuffer struct {
	body      bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.body.Len()
	if n > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	_, _ = b.body.Write(p)
	return n, nil
}

func git(ctx context.Context, root string, limit int, args ...string) ([]byte, bool, error) {
	arguments := []string{"--no-pager", "--no-optional-locks", "--literal-pathspecs", "-C", root,
		"-c", "core.quotepath=false", "-c", "core.fsmonitor=false"}
	command := exec.CommandContext(ctx, "git", append(arguments, args...)...)
	stdout, stderr := &cappedBuffer{limit: limit}, &cappedBuffer{limit: 8192}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, false, fmt.Errorf("changes: git: %w · %s", err, strings.TrimSpace(stderr.body.String()))
	}
	return stdout.body.Bytes(), stdout.truncated, nil
}
