package changes

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

// Source is a bounded UTF-8 preview of a Git index blob.
type Source struct {
	Content   string
	Binary    bool
	Missing   bool
	Truncated bool
}

// ReadIndex reads the selected file's staged version without staging anything.
// New unstaged and staged-deleted files have no index content.
func ReadIndex(ctx context.Context, root string, file File) (Source, error) {
	if !validPath(file.Path) || len(file.Status) != 2 {
		return Source{}, errors.New("changes: invalid file selection")
	}
	if file.Status == "??" || file.Status[0] == 'D' {
		return Source{Missing: true}, nil
	}
	body, truncated, err := git(ctx, root, maximumPatchBytes, "show", ":"+file.Path)
	if err != nil {
		if ctx.Err() != nil {
			return Source{}, ctx.Err()
		}
		// A clean selection can also be an ignored file, absent from the index.
		if file.Status == "  " {
			return Source{Missing: true}, nil
		}
		return Source{}, err
	}
	if truncated {
		for start := max(0, len(body)-utf8.UTFMax); start < len(body); start++ {
			if utf8.RuneStart(body[start]) && !utf8.FullRune(body[start:]) {
				body = body[:start]
				break
			}
		}
	}
	if bytes.ContainsRune(body, 0) || !utf8.Valid(body) {
		return Source{Binary: true, Truncated: truncated}, nil
	}
	content, truncated := boundPatch(body, truncated)
	return Source{Content: content, Truncated: truncated}, nil
}

// ReadComparison returns one explicitly chosen baseline, so the caller can
// annotate a full file without combining incompatible HEAD/index/worktree lines.
// allAdded is true when the baseline is empty (an unborn HEAD or untracked file).
func ReadComparison(ctx context.Context, root string, file File, comparison string) (section Section, allAdded bool, err error) {
	if !validPath(file.Path) || (file.OldPath != "" && !validPath(file.OldPath)) || len(file.Status) != 2 {
		return section, false, errors.New("changes: invalid file selection")
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-relative", "--find-renames", "--unified=3", "--src-prefix=a/", "--dst-prefix=b/"}
	switch comparison {
	case "working":
		section.Title = "HEAD → working tree"
		if _, _, headErr := git(ctx, root, 1024, "rev-parse", "--verify", "HEAD"); headErr != nil {
			if ctx.Err() != nil {
				return section, false, ctx.Err()
			}
			return section, true, nil
		}
		args = append(args, "HEAD")
	case "staged":
		section.Title = "HEAD → index"
		if strings.ContainsRune(file.Status, 'U') || file.Status == "AA" || file.Status == "DD" {
			return section, false, errors.New("changes: index has unresolved merge entries; select the working tree comparison")
		}
		args = append(args, "--cached")
	case "unstaged":
		section.Title = "Index → working tree"
	default:
		return section, false, errors.New("changes: invalid comparison")
	}
	if file.Status == "??" {
		return section, comparison != "staged", nil
	}
	args = append(args, "--", file.Path)
	if file.OldPath != "" {
		args = append(args, file.OldPath)
	}
	body, truncated, err := git(ctx, root, maximumPatchBytes, args...)
	if err != nil {
		return section, false, err
	}
	section.Patch, section.Truncated = boundPatch(body, truncated)
	return section, false, nil
}
