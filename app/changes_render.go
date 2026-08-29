package app

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/changes"
)

type changeDiffRow struct {
	kind             byte
	oldLine, newLine int
	text             string
	tokens           []chroma.Token
}

type changePreview struct {
	rows           []changeDiffRow
	added, removed int
	truncated      bool
	hasLineDiff    bool
}

var changeHunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

func buildChangePreview(path string, detail changes.Detail) changePreview {
	var preview changePreview
	for _, section := range detail.Sections {
		preview.rows = append(preview.rows, changeDiffRow{kind: '#', text: section.Title})
		oldLine, newLine, hunkStart := 0, 0, -1
		flushHunk := func() {
			if hunkStart >= 0 {
				highlightChangeHunk(path, preview.rows[hunkStart:])
			}
			hunkStart = -1
		}
		for _, line := range strings.Split(strings.TrimSuffix(section.Patch, "\n"), "\n") {
			line = strings.TrimSuffix(line, "\r")
			if match := changeHunkHeader.FindStringSubmatch(line); match != nil {
				preview.hasLineDiff = true
				flushHunk()
				oldLine, _ = strconv.Atoi(match[1])
				newLine, _ = strconv.Atoi(match[2])
				preview.rows = append(preview.rows, changeDiffRow{kind: '@', text: line})
				hunkStart = len(preview.rows)
				continue
			}
			if strings.HasPrefix(line, "diff --") {
				flushHunk()
				continue
			}
			// The pane title identifies the file; retain useful metadata such
			// as modes, rename sources, binary notices, and conflict headers.
			if hunkStart < 0 && (strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")) {
				continue
			}
			row := changeDiffRow{kind: '.', text: line}
			if hunkStart >= 0 && len(line) > 0 {
				switch line[0] {
				case '+':
					row.kind, row.text, row.newLine = '+', line[1:], newLine
					newLine++
					preview.added++
				case '-':
					row.kind, row.text, row.oldLine = '-', line[1:], oldLine
					oldLine++
					preview.removed++
				case ' ':
					row.kind, row.text, row.oldLine, row.newLine = ' ', line[1:], oldLine, newLine
					oldLine++
					newLine++
				}
			}
			preview.rows = append(preview.rows, row)
		}
		flushHunk()
		if section.Truncated {
			preview.truncated = true
			preview.rows = append(preview.rows, changeDiffRow{kind: '#', text: "… partial preview; counts cover displayed lines only"})
		}
	}
	return preview
}

func highlightChangeHunk(path string, rows []changeDiffRow) {
	var before, after strings.Builder
	for _, row := range rows {
		if row.kind == '-' || row.kind == ' ' {
			before.WriteString(row.text)
			before.WriteByte('\n')
		}
		if row.kind == '+' || row.kind == ' ' {
			after.WriteString(row.text)
			after.WriteByte('\n')
		}
	}
	// Lex complete old/new hunk snippets separately, preserving multiline
	// strings/comments without feeding diff markers to a language lexer.
	oldTokens := changeSyntaxTokens(path, before.String())
	newTokens := changeSyntaxTokens(path, after.String())
	oldIndex, newIndex := 0, 0
	for index := range rows {
		row := &rows[index]
		if row.kind == '-' || row.kind == ' ' {
			if oldIndex < len(oldTokens) {
				row.tokens = oldTokens[oldIndex]
			}
			oldIndex++
		}
		if row.kind == '+' || row.kind == ' ' {
			if newIndex < len(newTokens) {
				row.tokens = newTokens[newIndex]
			}
			newIndex++
		}
	}
}

func changeSyntaxTokens(path, content string) (lines [][]chroma.Token) {
	if content == "" || len(content) > 128<<10 {
		return nil
	}
	// Chroma iterators can panic on malformed source. A preview must still
	// work as plain text if a lexer cannot handle a particular snippet.
	defer func() {
		if recover() != nil {
			lines = nil
		}
	}()
	lexer := lexers.Match(path)
	if lexer == nil {
		return nil
	}
	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, content)
	if err != nil {
		return nil
	}
	return chroma.SplitTokensIntoLines(iterator.Tokens())
}

func renderChangePreview(preview changePreview, dark bool, width int) string {
	palette := styles.Get("github")
	background, foreground := "#fafafa", "#24292e"
	addedBackground, removedBackground := "#e6ffed", "#ffeef0"
	if dark {
		palette = styles.Get("dracula")
		background, foreground = "#171a21", "#f8f8f2"
		addedBackground, removedBackground = "#16362a", "#3b1e28"
	}
	lines := make([]string, 0, len(preview.rows))
	for _, row := range preview.rows {
		bg := background
		if row.kind == '+' {
			bg = addedBackground
		} else if row.kind == '-' {
			bg = removedBackground
		}
		base := lipgloss.NewStyle().Foreground(lipgloss.Color(foreground)).Background(lipgloss.Color(bg))
		var line strings.Builder
		if row.kind == '#' || row.kind == '@' || row.kind == '.' {
			style := base.Foreground(themedColor(dark, "245", "242"))
			if row.kind != '.' {
				style = base.Bold(true).Foreground(themedColor(dark, "81", "25"))
			}
			line.WriteString(style.Render(safeChangeText(row.text)))
		} else {
			oldNumber, newNumber := "", ""
			if row.oldLine > 0 {
				oldNumber = strconv.Itoa(row.oldLine)
			}
			if row.newLine > 0 {
				newNumber = strconv.Itoa(row.newLine)
			}
			gutter := base.Foreground(themedColor(dark, "245", "242"))
			if row.kind == '+' || row.kind == '-' {
				gutter = diffCountStyle(dark, row.kind == '+').Background(lipgloss.Color(bg))
			}
			line.WriteString(gutter.Render(fmt.Sprintf("%5s %5s %c │ ", oldNumber, newNumber, row.kind)))
			if len(row.tokens) == 0 {
				line.WriteString(base.Render(safeChangeText(row.text)))
			} else {
				for _, token := range row.tokens {
					entry := palette.Get(token.Type)
					style := base
					if entry.Colour.IsSet() {
						style = style.Foreground(lipgloss.Color(entry.Colour.String()))
					}
					style = style.Bold(entry.Bold == chroma.Yes).Italic(entry.Italic == chroma.Yes)
					line.WriteString(style.Render(safeChangeText(strings.TrimSuffix(token.Value, "\n"))))
				}
			}
		}
		// Paint to the edge without wrapping or recolouring syntax tokens.
		line.WriteString(base.Render(strings.Repeat(" ", max(0, width-ansi.StringWidth(line.String())))))
		lines = append(lines, line.String())
	}
	return strings.Join(lines, "\n")
}
