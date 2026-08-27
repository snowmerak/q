package builtin

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const maxReadLines = 2000

// ReadFileInput describes a hashline read. Offset is 1-based and Limit is
// capped to keep tool responses bounded.
type ReadFileInput struct {
	Path   string `json:"path" jsonschema:"Workspace-relative or in-workspace absolute file path."`
	Offset int    `json:"offset,omitempty" jsonschema:"First 1-based line to return. Defaults to 1."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum lines to return. Defaults to 200 and is capped at 2000."`
}

type ReadFileOutput struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Offset     int    `json:"offset"`
	LineCount  int    `json:"line_count"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated"`
}

// ReadFile reads a text file and prefixes every returned line with a hashline
// anchor that can be passed to EditFile.
func (fs *FS) ReadFile(input ReadFileInput) (ReadFileOutput, error) {
	path, err := fs.resolveExisting(input.Path)
	if err != nil {
		return ReadFileOutput{}, err
	}
	lines, _, err := readFileLines(path)
	if err != nil {
		return ReadFileOutput{}, err
	}
	offset := input.Offset
	if offset == 0 {
		offset = 1
	}
	if offset < 1 || offset > len(lines) {
		return ReadFileOutput{}, fmt.Errorf("[E_INVALID_RANGE] offset %d is outside 1..%d", offset, len(lines))
	}
	limit := input.Limit
	if limit == 0 {
		limit = 200
	}
	if limit < 1 {
		return ReadFileOutput{}, fmt.Errorf("[E_INVALID_RANGE] limit must be positive")
	}
	if limit > maxReadLines {
		limit = maxReadLines
	}
	end := min(offset-1+limit, len(lines))
	selected := lines[offset-1 : end]
	return ReadFileOutput{
		Path:       input.Path,
		Content:    FormatReadOutput(selected, offset),
		Offset:     offset,
		LineCount:  len(selected),
		TotalLines: len(lines),
		Truncated:  end < len(lines),
	}, nil
}

type EditOperation struct {
	Op    string   `json:"op" jsonschema:"Operation: replace, append, or prepend."`
	Pos   string   `json:"pos,omitempty" jsonschema:"LINE#HASH anchor (e.g. 4#BP). Copy only the part before the first ':' in read_file's LINE#HASH:CONTENT display; never include ':' or the following file content. Required for replace; optional for append/prepend."`
	End   string   `json:"end,omitempty" jsonschema:"Inclusive ending LINE#HASH anchor for replace (e.g. 4#BP). Copy only the part before the first ':' in read_file's LINE#HASH:CONTENT display; never include ':' or the following file content."`
	Lines []string `json:"lines" jsonschema:"Literal replacement or inserted lines without hashline prefixes."`
}

type EditFileInput struct {
	Path  string          `json:"path" jsonschema:"Workspace-relative or in-workspace absolute file path."`
	Edits []EditOperation `json:"edits" jsonschema:"One or more non-overlapping edits, all anchored to the same pre-edit snapshot."`
}

type EditFileOutput struct {
	Path       string `json:"path"`
	Applied    int    `json:"applied"`
	Diff       string `json:"diff"`
	Anchors    string `json:"anchors,omitempty"`
	TotalLines int    `json:"total_lines"`
}

type resolvedEdit struct {
	index   int
	start   int // zero-based splice point in the original snapshot
	end     int // exclusive; start == end is an insertion
	lines   []string
	preview string
}

var displayedLineRE = regexp.MustCompile(`^\s*[>+\-]?\s*\d+#[ZPMQVRWSNKTXJBYH]{2}:`)

// EditFile validates every anchor against one snapshot, rejects overlapping
// edits, applies edits bottom-up, and atomically replaces the file.
func (fs *FS) EditFile(input EditFileInput) (EditFileOutput, error) {
	if len(input.Edits) == 0 {
		return EditFileOutput{}, fmt.Errorf("[E_INVALID_PATCH] edits must not be empty")
	}
	path, err := fs.resolveExisting(input.Path)
	if err != nil {
		return EditFileOutput{}, err
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()
	lines, perm, err := readFileLines(path)
	if err != nil {
		return EditFileOutput{}, err
	}
	resolved := make([]resolvedEdit, 0, len(input.Edits))
	for i, edit := range input.Edits {
		for _, line := range edit.Lines {
			if strings.ContainsAny(line, "\r\n") {
				return EditFileOutput{}, fmt.Errorf("[E_INVALID_PATCH] edit %d contains an embedded newline; pass one string per line", i)
			}
			if displayedLineRE.MatchString(line) {
				return EditFileOutput{}, fmt.Errorf("[E_INVALID_PATCH] edit %d contains a hashline display prefix", i)
			}
		}
		r, err := resolveEdit(lines, edit, i)
		if err != nil {
			return EditFileOutput{}, err
		}
		resolved = append(resolved, r)
	}
	if err := rejectOverlaps(resolved); err != nil {
		return EditFileOutput{}, err
	}

	sort.SliceStable(resolved, func(i, j int) bool { return resolved[i].start > resolved[j].start })
	result := append([]string(nil), lines...)
	for _, edit := range resolved {
		next := make([]string, 0, len(result)-(edit.end-edit.start)+len(edit.lines))
		next = append(next, result[:edit.start]...)
		next = append(next, edit.lines...)
		next = append(next, result[edit.end:]...)
		result = next
	}
	if err := atomicWriteFile(path, []byte(joinLines(result)), perm); err != nil {
		return EditFileOutput{}, err
	}

	previews := make([]string, len(resolved))
	for _, edit := range resolved {
		previews[edit.index] = edit.preview
	}
	first, last := changedRange(resolved, len(result))
	anchors := ""
	if first < last {
		anchors = FormatReadOutput(result[first:last], first+1)
	}
	return EditFileOutput{
		Path:       input.Path,
		Applied:    len(input.Edits),
		Diff:       strings.Join(previews, "\n"),
		Anchors:    anchors,
		TotalLines: len(result),
	}, nil
}

func resolveEdit(lines []string, edit EditOperation, index int) (resolvedEdit, error) {
	for _, field := range []struct{ name, value string }{{"pos", edit.Pos}, {"end", edit.End}} {
		if strings.Contains(field.value, ":") {
			return resolvedEdit{}, fmt.Errorf("[E_INVALID_PATCH] edit %d has invalid %s anchor %q: anchor contains ':'; ':' separates LINE#HASH from file content in read_file output. Use only LINE#HASH before the first ':'; remove ':' and all following file content", index, field.name, field.value)
		}
	}
	validate := func(raw, field string) (Anchor, error) {
		anchor, ok := ParseAnchor(raw)
		if !ok {
			return Anchor{}, fmt.Errorf("[E_INVALID_PATCH] edit %d has invalid %s anchor %q", index, field, raw)
		}
		if anchor.Line > len(lines) {
			return Anchor{}, fmt.Errorf("[E_INVALID_PATCH] edit %d %s line %d is outside the file", index, field, anchor.Line)
		}
		actual := HashLine(lines[anchor.Line-1], anchor.Line)
		if actual != anchor.Hash {
			return Anchor{}, fmt.Errorf("[E_STALE_CONTEXT] edit %d %s line %d changed; current anchor is %d#%s", index, field, anchor.Line, anchor.Line, actual)
		}
		return anchor, nil
	}

	switch edit.Op {
	case "replace":
		if edit.Pos == "" {
			return resolvedEdit{}, fmt.Errorf("[E_INVALID_PATCH] edit %d replace requires pos", index)
		}
		pos, err := validate(edit.Pos, "pos")
		if err != nil {
			return resolvedEdit{}, err
		}
		end := pos
		if edit.End != "" {
			end, err = validate(edit.End, "end")
			if err != nil {
				return resolvedEdit{}, err
			}
		}
		if end.Line < pos.Line {
			return resolvedEdit{}, fmt.Errorf("[E_INVALID_PATCH] edit %d end precedes pos", index)
		}
		old := lines[pos.Line-1 : end.Line]
		return resolvedEdit{index: index, start: pos.Line - 1, end: end.Line, lines: edit.Lines, preview: generateDiffPreview(old, edit.Lines, pos.Line)}, nil
	case "append", "prepend":
		if edit.End != "" {
			return resolvedEdit{}, fmt.Errorf("[E_INVALID_PATCH] edit %d %s does not accept end", index, edit.Op)
		}
		point := 0
		if edit.Op == "append" {
			point = len(lines)
		}
		if edit.Pos != "" {
			pos, err := validate(edit.Pos, "pos")
			if err != nil {
				return resolvedEdit{}, err
			}
			if edit.Op == "append" {
				point = pos.Line
			} else {
				point = pos.Line - 1
			}
		}
		startLine := point + 1
		return resolvedEdit{index: index, start: point, end: point, lines: edit.Lines, preview: generateDiffPreview(nil, edit.Lines, startLine)}, nil
	default:
		return resolvedEdit{}, fmt.Errorf("[E_INVALID_PATCH] edit %d has unsupported op %q", index, edit.Op)
	}
}

func rejectOverlaps(edits []resolvedEdit) error {
	for i := range edits {
		for j := i + 1; j < len(edits); j++ {
			a, b := edits[i], edits[j]
			if a.start == b.start || (a.start < b.end && b.start < a.end) ||
				(a.start == a.end && a.start >= b.start && a.start <= b.end) ||
				(b.start == b.end && b.start >= a.start && b.start <= a.end) {
				return fmt.Errorf("[E_INVALID_PATCH] edits %d and %d overlap or share an insertion point", a.index, b.index)
			}
		}
	}
	return nil
}

func changedRange(edits []resolvedEdit, total int) (int, int) {
	if len(edits) == 0 || total == 0 {
		return 0, 0
	}
	ordered := append([]resolvedEdit(nil), edits...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].start < ordered[j].start })
	first, last := total, 0
	delta := 0
	for _, edit := range ordered {
		start := edit.start + delta
		end := start + len(edit.lines)
		if start < first {
			first = start
		}
		if end > last {
			last = end
		}
		delta += len(edit.lines) - (edit.end - edit.start)
	}
	first = max(0, min(first, total))
	last = max(first, min(last, total))
	return first, last
}
