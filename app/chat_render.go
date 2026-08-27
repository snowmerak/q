package app

import (
	"encoding/json"
	"fmt"
	"image/color"
	"regexp"
	"strings"

	"charm.land/glamour/v2"
	glamourstyles "charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/client"
)

func renderTranscript(messages []client.Message, width int) string {
	return renderTranscriptWithStyle(messages, width, true)
}

func renderTranscriptWithStyle(messages []client.Message, width int, dark bool) string {
	return renderStreamingTranscriptWithStyle(messages, nil, "", width, dark)
}

type transcriptThought struct {
	Before  int
	Content string
}

func renderStreamingTranscriptWithStyle(
	messages []client.Message,
	thoughts []transcriptThought,
	streamResponse string,
	width int,
	dark bool,
) string {
	return strings.Join(renderTranscriptBlocks(messages, thoughts, streamResponse, width, dark, false), "\n\n")
}

func renderTranscriptBlocks(
	messages []client.Message,
	thoughts []transcriptThought,
	streamResponse string,
	width int,
	dark, toolResultsCollapsed bool,
) []string {
	bodyWidth := max(10, width-2)
	markdownRenderer, _ := newMarkdownRenderer(bodyWidth, dark)
	var blocks []string
	thoughtIndex := 0
	appendThoughts := func(before int) {
		for thoughtIndex < len(thoughts) && thoughts[thoughtIndex].Before <= before {
			content := thoughts[thoughtIndex].Content
			if content != "" {
				content = renderMarkdown(markdownRenderer, normalizeMarkdown(content))
				labelStyle := lipgloss.NewStyle().Bold(true).Foreground(themedColor(dark, "243", "245"))
				// Markdown already carries its own emphasis, headings, list indentation,
				// and code colors. Applying an italic/foreground style to the rendered
				// ANSI output flattens those distinctions and can make code unreadable.
				bodyStyle := lipgloss.NewStyle()
				blocks = append(blocks, labelStyle.Render("THINKING")+"\n"+bodyStyle.Width(bodyWidth).Render(content))
			}
			thoughtIndex++
		}
	}
	for messageIndex, message := range messages {
		appendThoughts(messageIndex)
		if message.Role == client.RoleSystem || message.Role == client.RoleDeveloper {
			continue
		}
		label := "YOU"
		labelStyle := userLabelStyle
		bodyStyle := userBodyStyle
		if message.Role == client.RoleAssistant {
			label = "ASSISTANT"
			labelStyle = assistantLabelStyle
			bodyStyle = assistantBodyStyle
		}
		if message.Role == client.RoleTool {
			label = "TOOL"
			if message.Name != "" {
				label += " · " + message.Name
			}
		}
		body := message.TextContent()
		if message.Role == client.RoleAssistant && body != "" {
			body = renderMarkdown(markdownRenderer, normalizeMarkdown(body))
		}
		if len(message.ToolCalls) > 0 {
			calls := renderToolCalls(message.ToolCalls)
			if body == "" {
				body = calls
			} else {
				body += "\n\n" + calls
			}
		}
		if message.Role == client.RoleTool {
			body = renderToolResult(message, dark, bodyWidth, toolResultsCollapsed)
			if toolResultsCollapsed {
				label = "▸ " + label
			}
			blocks = append(blocks, renderToolPanel(label, body, bodyWidth, dark))
			continue
		}
		blocks = append(blocks, labelStyle.Render(label)+"\n"+bodyStyle.Width(bodyWidth).Render(body))
	}
	appendThoughts(len(messages))
	if streamResponse != "" {
		body := renderMarkdown(markdownRenderer, normalizeMarkdown(streamResponse))
		blocks = append(blocks, assistantLabelStyle.Render("ASSISTANT")+"\n"+assistantBodyStyle.Width(bodyWidth).Render(body))
	}
	if len(blocks) == 0 {
		return []string{emptyStyle.Render("Start a conversation. Your messages stay in memory for this run.")}
	}
	return blocks
}

var attachedMarkdownFence = regexp.MustCompile("(?m)([^\\r\\n])(?:[ \\t]+)(```|~~~)([A-Za-z0-9_.+#-]*)[ \\t]*(\\r?\\n)")

// normalizeMarkdown makes common streamed-model Markdown a little more
// forgiving. Models occasionally attach a fenced code block to the preceding
// sentence ("example: ```go\n"), which CommonMark interprets as inline text and
// then collapses all code lines into one paragraph. Moving only fence-shaped
// sequences that end the line preserves ordinary inline backticks.
func normalizeMarkdown(source string) string {
	source = attachedMarkdownFence.ReplaceAllString(source, "$1\n\n$2$3$4")
	return normalizeDisplaySymbols(source)
}

var displayMathSymbol = regexp.MustCompile(`\$\s*\\(rightarrow|to|leftarrow|leftrightarrow|Rightarrow|Leftarrow|Leftrightarrow|leq|le|geq|ge|neq|times|cdot|pm|infty)\s*\$`)

var displaySymbolReplacer = strings.NewReplacer(
	`\rightarrow`, "→", `\to`, "→", `\leftarrow`, "←", `\leftrightarrow`, "↔",
	`\Rightarrow`, "⇒", `\Leftarrow`, "⇐", `\Leftrightarrow`, "⇔",
	`\leq`, "≤", `\le`, "≤", `\geq`, "≥", `\ge`, "≥", `\neq`, "≠",
	`\times`, "×", `\cdot`, "·", `\pm`, "±", `\infty`, "∞",
)

func normalizeDisplaySymbols(source string) string {
	var result strings.Builder
	lines := strings.SplitAfter(source, "\n")
	inFence := false
	fenceMarker := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if !inFence {
				inFence, fenceMarker = true, marker
			} else if marker == fenceMarker {
				inFence, fenceMarker = false, ""
			}
			result.WriteString(line)
			continue
		}
		if inFence {
			result.WriteString(line)
			continue
		}
		result.WriteString(normalizeInlineDisplaySymbols(line))
	}
	return result.String()
}

func normalizeInlineDisplaySymbols(line string) string {
	var result strings.Builder
	for len(line) > 0 {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			result.WriteString(replaceDisplaySymbols(line))
			break
		}
		result.WriteString(replaceDisplaySymbols(line[:start]))
		run := 1
		for start+run < len(line) && line[start+run] == '`' {
			run++
		}
		delimiter := strings.Repeat("`", run)
		end := strings.Index(line[start+run:], delimiter)
		if end < 0 {
			result.WriteString(line[start:])
			break
		}
		end += start + run + run
		result.WriteString(line[start:end])
		line = line[end:]
	}
	return result.String()
}

func replaceDisplaySymbols(value string) string {
	value = displayMathSymbol.ReplaceAllStringFunc(value, func(match string) string {
		parts := displayMathSymbol.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		return displaySymbolReplacer.Replace(`\` + parts[1])
	})
	return displaySymbolReplacer.Replace(value)
}

func newMarkdownRenderer(width int, dark bool) (*glamour.TermRenderer, error) {
	style := glamourstyles.LightStyleConfig
	if dark {
		style = glamourstyles.DarkStyleConfig
	}
	// Keep fenced code readable even when a terminal does not report its
	// background color. Dracula's bright syntax palette works in either theme.
	style.CodeBlock = glamourstyles.DraculaStyleConfig.CodeBlock
	return glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(max(10, width)),
		glamour.WithPreservedNewLines(),
	)
}

func renderMarkdown(renderer *glamour.TermRenderer, source string) string {
	if renderer == nil {
		return source
	}
	rendered, err := renderer.Render(source)
	if err != nil {
		return source
	}
	return strings.Trim(rendered, "\n")
}
func renderToolPanel(label, body string, width int, dark bool) string {
	return toolPanelStyle(width, dark).Render(toolPanelContent(label, body, dark))
}

func toolPanelContent(label, body string, dark bool) string {
	background := themedColor(dark, "235", "255")
	border := themedColor(dark, "81", "25")
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(border).Background(background)
	return labelStyle.Render(label) + "\n" + body
}

func toolPanelStyle(width int, dark bool) lipgloss.Style {
	background := themedColor(dark, "235", "255")
	border := themedColor(dark, "81", "25")
	return lipgloss.NewStyle().
		Background(background).
		BorderStyle(lipgloss.Border{Left: "▌"}).
		BorderLeft(true).
		BorderForeground(border).
		Padding(0, 1).
		Width(max(10, width-3))
}

func agentTraceTitleStyle(dark bool) lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(themedColor(dark, "99", "61"))
}

func agentTraceActiveStyle(dark bool) lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(themedColor(dark, "75", "61"))
}

func agentTracePanelStyle(width int, dark bool) lipgloss.Style {
	return lipgloss.NewStyle().
		Background(themedColor(dark, "233", "255")).
		BorderStyle(lipgloss.Border{Left: "│"}).
		BorderLeft(true).
		BorderForeground(themedColor(dark, "99", "61")).
		Padding(0, 1).
		Width(max(10, width-3))
}

func isPlanApprovalQuestion(input askToUserInput) bool {
	if strings.EqualFold(strings.TrimSpace(input.Question), "Approve this plan?") {
		return true
	}
	choices := make(map[string]struct{}, len(input.Choices))
	for _, choice := range input.Choices {
		choices[strings.ToLower(strings.TrimSpace(choice.ID))] = struct{}{}
	}
	_, approve := choices["approve"]
	_, revise := choices["revise"]
	_, cancel := choices["cancel"]
	return approve && revise && cancel
}

func isPlanRecoveryQuestion(input askToUserInput) bool {
	return strings.EqualFold(strings.TrimSpace(input.Question), "Resume interrupted plan?")
}

func isPlanControlQuestion(input askToUserInput) bool {
	return isPlanApprovalQuestion(input) || isPlanRecoveryQuestion(input)
}

func questionPanelTitle(input askToUserInput) string {
	if isPlanApprovalQuestion(input) {
		return "PLAN APPROVAL"
	}
	if isPlanRecoveryQuestion(input) {
		return "EXECUTION RECOVERY"
	}
	return "QUESTION"
}

func questionPanelAccent(input askToUserInput, dark bool) color.Color {
	if isPlanApprovalQuestion(input) {
		return themedColor(dark, "214", "130")
	}
	if isPlanRecoveryQuestion(input) {
		return themedColor(dark, "141", "97")
	}
	return themedColor(dark, "81", "25")
}

func questionPanelStyle(width int, dark, planControl bool) lipgloss.Style {
	background := themedColor(dark, "235", "255")
	border := themedColor(dark, "81", "25")
	marker := "▌"
	if planControl {
		background = themedColor(dark, "236", "230")
		border = themedColor(dark, "214", "130")
		marker = "┃"
	}
	return lipgloss.NewStyle().
		Background(background).
		BorderStyle(lipgloss.Border{Left: marker}).
		BorderLeft(true).
		BorderForeground(border).
		Padding(0, 1).
		Width(max(10, width-3))
}

func renderQuestionPanel(input askToUserInput, selected, width int, dark bool) string {
	style := questionPanelStyle(width, dark, isPlanControlQuestion(input))
	title := lipgloss.NewStyle().Bold(true).Foreground(questionPanelAccent(input, dark))
	return style.Render(title.Render(questionPanelTitle(input)) + "\n" + renderPendingQuestion(input, selected))
}

func themedColor(dark bool, darkColor, lightColor string) color.Color {
	if dark {
		return lipgloss.Color(darkColor)
	}
	return lipgloss.Color(lightColor)
}

func renderToolCalls(calls []client.ToolCall) string {
	lines := make([]string, 0, len(calls))
	for _, call := range calls {
		lines = append(lines, "→ "+describeToolCall(call))
	}
	return strings.Join(lines, "\n")
}

func describeToolCall(call client.ToolCall) string {
	arguments := toolArguments(call.Function.Arguments)
	switch call.Function.Name {
	case "run_command":
		if command := stringArgument(arguments, "command"); command != "" {
			return "$ " + command
		}
	case "wait":
		return "wait for " + stringArgument(arguments, "command_id")
	case "cmd_status":
		return "check " + stringArgument(arguments, "command_id")
	case "read_file":
		return "read " + stringArgument(arguments, "path")
	case "write_file":
		content := stringArgument(arguments, "content")
		return fmt.Sprintf("write %s · %d bytes", stringArgument(arguments, "path"), len([]byte(content)))
	case "edit_file":
		return "edit " + stringArgument(arguments, "path")
	case "list_directory":
		path := stringArgument(arguments, "path")
		if path == "" {
			path = "."
		}
		return "list " + path
	case "create_directory":
		return "create directory " + stringArgument(arguments, "path")
	case "remove_path":
		return "remove " + stringArgument(arguments, "path")
	case "move_path", "copy_path":
		return call.Function.Name + " " + stringArgument(arguments, "source") + " → " + stringArgument(arguments, "destination")
	case taskStartToolName:
		return "start task · " + stringArgument(arguments, "objective")
	case askToUserToolName:
		return "ask user · " + stringArgument(arguments, "question")
	case taskCompleteToolName:
		return "complete task · " + stringArgument(arguments, "outcome")
	}
	return call.Function.Name
}

func renderToolResult(message client.Message, dark bool, width int, collapsed bool) string {
	content := message.Content
	isError := strings.HasPrefix(content, "Tool error: ")
	if isError {
		content = strings.TrimPrefix(content, "Tool error: ")
	}
	var value map[string]any
	if json.Unmarshal([]byte(content), &value) != nil {
		if collapsed {
			return collapsedToolSummary(message.Name, isError, content)
		}
		marker := "✓"
		if isError {
			marker = "✗"
		}
		return marker + " " + content
	}
	loomNote := ""
	if ref := stringValue(value["loom_ref"]); ref != "" {
		loomNote = "\n" + renderLoomNote(ref, value["bytes"], dark)
		if result, exists := value["result"]; exists {
			if resultObject, ok := result.(map[string]any); ok {
				value = resultObject
			} else {
				if collapsed {
					return collapsedToolSummary(message.Name, isError, result) + loomNote
				}
				body, _ := json.MarshalIndent(result, "", "  ")
				marker := "✓"
				if isError {
					marker = "✗"
				}
				return marker + " " + message.Name + "\n" + string(body) + loomNote
			}
		} else {
			marker := "•"
			if isError {
				marker = "✗"
			}
			rendered := marker + " " + message.Name + " · full result stored in Loom"
			if preview := strings.TrimSpace(stringValue(value["preview"])); preview != "" && !collapsed {
				rendered += "\n" + limitToolOutput(preview)
			}
			return rendered + loomNote
		}
	}
	if isError {
		if collapsed {
			return collapsedToolSummary(message.Name, true, value) + loomNote
		}
		body, _ := json.MarshalIndent(value, "", "  ")
		return "✗ " + message.Name + "\n" + string(body) + loomNote
	}
	var rendered string
	switch message.Name {
	case "run_command", "cmd_status", "wait":
		id := stringValue(value["command_id"])
		status := stringValue(value["status"])
		marker := "•"
		if status == "succeeded" {
			marker = "✓"
		} else if status == "failed" {
			marker = "✗"
		}
		rendered = strings.TrimSpace(strings.Join([]string{marker, id, "·", status}, " "))
		if exitCode, ok := value["exit_code"]; ok {
			rendered += " · exit " + stringValue(exitCode)
		}
		if output := strings.TrimRight(stringValue(value["output"]), "\r\n"); output != "" && !collapsed {
			rendered += "\n" + limitToolOutput(output)
		}
		if more, _ := value["more_output"].(bool); more && !collapsed {
			rendered += "\n… more output available"
		}
	case "write_file":
		rendered = fmt.Sprintf("✓ wrote %s · %s", renderFilePath(stringValue(value["path"]), dark), formatByteValue(value["bytes"]))
	case "edit_file":
		rendered = fmt.Sprintf("✓ edited %s · %s change(s)", renderFilePath(stringValue(value["path"]), dark), stringValue(value["applied"]))
		if diff := strings.TrimSpace(stringValue(value["diff"])); diff != "" && !collapsed {
			diff = limitToolOutput(diff)
			formatted, added, removed := renderFileDiff(diff, dark, max(10, width-6))
			counts := diffCountStyle(dark, true).Render(fmt.Sprintf("+%d", added)) + " " +
				diffCountStyle(dark, false).Render(fmt.Sprintf("-%d", removed))
			rendered += " · " + counts + "\n" + formatted
		}
	case "read_file":
		rendered = fmt.Sprintf("✓ read %s · %s/%s lines", renderFilePath(stringValue(value["path"]), dark), stringValue(value["line_count"]), stringValue(value["total_lines"]))
	case "create_directory":
		rendered = "✓ created " + renderFilePath(stringValue(value["path"]), dark)
	case "remove_path":
		rendered = "✓ removed " + renderFilePath(stringValue(value["path"]), dark)
	case "move_path", "copy_path":
		rendered = "✓ " + renderFilePath(stringValue(value["source"]), dark) + " → " + renderFilePath(stringValue(value["destination"]), dark)
	case "list_directory":
		entries, _ := value["entries"].([]any)
		rendered = fmt.Sprintf("✓ listed %s · %d entries", stringValue(value["path"]), len(entries))
	default:
		if collapsed {
			return collapsedToolSummary(message.Name, false, nil) + loomNote
		}
		body, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			rendered = "✓ " + message.Name
		} else {
			rendered = "✓ " + message.Name + "\n" + string(body)
		}
	}
	return rendered + loomNote
}

// Keep failures identifiable without exposing the result body in collapsed mode.
// Success text and arbitrary JSON are details, not a trustworthy short summary.
func collapsedToolSummary(name string, isError bool, value any) string {
	if !isError {
		return "✓ " + name
	}
	summary := "✗ " + name
	if object, ok := value.(map[string]any); ok {
		value = object["error"]
		if value == nil {
			value = object["message"]
		}
		if object, ok := value.(map[string]any); ok {
			value = object["message"]
		}
	}
	if detail, ok := value.(string); ok {
		detail = strings.TrimSpace(ansi.Strip(detail))
		detail, _, _ = strings.Cut(detail, "\n")
		detail = strings.Join(strings.Fields(detail), " ")
		if detail != "" {
			summary += " · " + ansi.Truncate(detail, 160, "…")
		}
	}
	return summary
}
func renderFilePath(path string, dark bool) string {
	return lipgloss.NewStyle().Bold(true).
		Foreground(themedColor(dark, "153", "24")).
		Render(path)
}

func renderLoomNote(ref string, bytes any, dark bool) string {
	id := strings.TrimPrefix(ref, "loom://")
	if len(id) > 12 {
		id = id[:12] + "…"
	}
	note := "↳ Loom " + id
	if size := formatByteValue(bytes); size != "" {
		note += " · " + size
	}
	return lipgloss.NewStyle().Foreground(themedColor(dark, "243", "242")).Render(note)
}

func formatByteValue(value any) string {
	var size int64
	switch value := value.(type) {
	case float64:
		size = int64(value)
	case int:
		size = int64(value)
	case int64:
		size = value
	case json.Number:
		size, _ = value.Int64()
	default:
		if text := stringValue(value); text != "" {
			return text + " B"
		}
		return ""
	}
	switch {
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(size)/(1<<10))
	default:
		return fmt.Sprintf("%d B", size)
	}
}

func diffCountStyle(dark, added bool) lipgloss.Style {
	if added {
		return lipgloss.NewStyle().Bold(true).Foreground(themedColor(dark, "120", "22"))
	}
	return lipgloss.NewStyle().Bold(true).Foreground(themedColor(dark, "210", "88"))
}

func renderFileDiff(diff string, dark bool, width int) (string, int, int) {
	addedStyle := lipgloss.NewStyle().
		Foreground(themedColor(dark, "120", "22")).
		Background(themedColor(dark, "22", "194")).
		Width(width)
	removedStyle := lipgloss.NewStyle().
		Foreground(themedColor(dark, "210", "88")).
		Background(themedColor(dark, "52", "224")).
		Width(width)
	headerStyle := lipgloss.NewStyle().Bold(true).
		Foreground(themedColor(dark, "81", "25")).
		Width(width)
	subtle := lipgloss.NewStyle().Foreground(themedColor(dark, "245", "240")).Width(width)
	lines := strings.Split(diff, "\n")
	rendered := make([]string, 0, len(lines))
	added, removed, change := 0, 0, 0
	for _, line := range lines {
		switch {
		case line == "Diff preview:":
			change++
			rendered = append(rendered, headerStyle.Render(fmt.Sprintf("@@ change %d @@", change)))
		case strings.HasPrefix(line, "+ "):
			added++
			rendered = append(rendered, addedStyle.Render(formatDiffLine(line)))
		case strings.HasPrefix(line, "- "):
			removed++
			rendered = append(rendered, removedStyle.Render(formatDiffLine(line)))
		default:
			rendered = append(rendered, subtle.Render(line))
		}
	}
	return strings.Join(rendered, "\n"), added, removed
}

func formatDiffLine(line string) string {
	if len(line) < 3 {
		return line
	}
	marker := line[:1]
	anchored := line[2:]
	hash := strings.IndexByte(anchored, '#')
	if hash < 1 {
		return line
	}
	colon := strings.IndexByte(anchored[hash+1:], ':')
	if colon < 0 {
		return line
	}
	content := anchored[hash+1+colon+1:]
	return fmt.Sprintf("%s %s │ %s", marker, anchored[:hash], content)
}

func toolArguments(arguments string) map[string]any {
	var value map[string]any
	_ = json.Unmarshal([]byte(arguments), &value)
	return value
}

func stringArgument(arguments map[string]any, name string) string {
	return stringValue(arguments[name])
}

func stringValue(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func limitToolOutput(output string) string {
	const maximumRunes = 8000
	runes := []rune(output)
	if len(runes) <= maximumRunes {
		return output
	}
	return "… output truncated in UI\n" + string(runes[len(runes)-maximumRunes:])
}
