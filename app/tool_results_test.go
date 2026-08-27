package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
)

func TestToolResultsCollapseDetailsAndKeepSummaries(t *testing.T) {
	for _, test := range []struct {
		name, tool, content, summary, detail string
	}{
		{"command", "run_command", `{"command_id":"cmd-1","status":"succeeded","exit_code":0,"output":"command output","more_output":true}`, "✓ cmd-1 · succeeded · exit 0", "command output"},
		{"failed command", "wait", `{"command_id":"cmd-1","status":"failed","exit_code":2,"output":"failure output"}`, "✗ cmd-1 · failed · exit 2", "failure output"},
		{"running command", "cmd_status", `{"command_id":"cmd-1","status":"running","output":"running output"}`, "• cmd-1 · running", "running output"},
		{"diff", "edit_file", `{"path":"main.go","applied":1,"diff":"Diff preview:\n- 1#AB:old value\n+ 1#CD:new value"}`, "✓ edited main.go · 1 change(s)", "new value"},
		{"object", "search", `{"hits":["search result"]}`, "✓ search", "search result"},
		{"array", "search", `["array result"]`, "✓ search", "array result"},
		{"text", "external_tool", "plain output\nsecond line", "✓ external_tool", "plain output"},
		{"error text", "read_file", "Tool error: permission denied\nerror detail", "✗ read_file · permission denied", "error detail"},
		{"error object", "read_file", `Tool error: {"error":"permission denied","stack":"error detail"}`, "✗ read_file · permission denied", "error detail"},
		{"error nested", "read_file", `Tool error: {"error":{"message":"permission denied","stack":"error detail"}}`, "✗ read_file · permission denied", "error detail"},
		{"loom inline", "wait", `{"loom_ref":"loom://0123456789abcdef","bytes":100,"result":{"command_id":"cmd-2","status":"succeeded","exit_code":0,"output":"inline output"}}`, "✓ cmd-2 · succeeded · exit 0", "inline output"},
		{"loom preview", "search", `{"loom_ref":"loom://0123456789abcdef","bytes":40000,"preview":"preview output"}`, "• search · full result stored in Loom", "preview output"},
		{"loom text", "search", `{"loom_ref":"loom://0123456789abcdef","bytes":100,"result":"string output"}`, "✓ search", "string output"},
		{"loom array", "search", `{"loom_ref":"loom://0123456789abcdef","bytes":100,"result":["array output"]}`, "✓ search", "array output"},
		{"loom error", "search", `Tool error: {"loom_ref":"loom://0123456789abcdef","bytes":100,"result":{"error":"permission denied","stack":"error detail"}}`, "✗ search · permission denied", "error detail"},
	} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dark=%t", test.name, dark), func(t *testing.T) {
				message := client.Message{Role: client.RoleTool, Name: test.tool, Content: test.content}
				collapsed := ansi.Strip(strings.Join(renderTranscriptBlocks([]client.Message{message}, nil, "", 100, dark, true), "\n\n"))
				if !strings.Contains(collapsed, "▸ TOOL · "+test.tool) || !strings.Contains(collapsed, test.summary) {
					t.Fatalf("collapsed summary missing:\n%s", collapsed)
				}
				if strings.Contains(collapsed, test.detail) || strings.Contains(collapsed, "more output available") {
					t.Fatalf("collapsed result leaked details:\n%s", collapsed)
				}
				expanded := ansi.Strip(renderTranscriptWithStyle([]client.Message{message}, 100, dark))
				if !strings.Contains(expanded, test.detail) || strings.Contains(expanded, "▸ TOOL") {
					t.Fatalf("expanded result missing details:\n%s", expanded)
				}
				if strings.Contains(test.content, "loom_ref") && !strings.Contains(collapsed, "Loom 0123456789ab…") {
					t.Fatalf("Loom reference missing:\n%s", collapsed)
				}
			})
		}
	}
}

func TestCollapsedToolErrorSummaryIsBounded(t *testing.T) {
	result := collapsedToolSummary("search", true, "\x1b[31m"+strings.Repeat("오류", 200)+"\x1b[0m\nprivate detail")
	if strings.Contains(result, "\x1b") || strings.Contains(result, "private detail") || ansi.StringWidth(result) > 180 {
		t.Fatalf("error summary is not bounded plain text: %q", result)
	}
}

func TestCollapsedToolResultsKeepFileMetadata(t *testing.T) {
	for _, test := range []struct{ tool, content, summary string }{
		{"read_file", `{"path":"main.go","line_count":10,"total_lines":20,"content":"file contents"}`, "✓ read main.go · 10/20 lines"},
		{"write_file", `{"path":"main.go","bytes":128}`, "✓ wrote main.go · 128 B"},
		{"list_directory", `{"path":"src","entries":["main.go"]}`, "✓ listed src · 1 entries"},
	} {
		t.Run(test.tool, func(t *testing.T) {
			message := client.Message{Name: test.tool, Content: test.content}
			for _, collapsed := range []bool{false, true} {
				result := ansi.Strip(renderToolResult(message, true, 80, collapsed))
				if result != test.summary {
					t.Fatalf("collapsed=%t: result=%q want=%q", collapsed, result, test.summary)
				}
			}
		})
	}
}

func TestToolResultToggleWorksDuringChatAndQuestions(t *testing.T) {
	for _, state := range []string{"idle", "waiting", "asking", "initializing"} {
		t.Run(state, func(t *testing.T) {
			m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
			m.screen = screenChat
			m.waiting = state == "waiting" || state == "asking"
			m.asking = state == "asking"
			m.initializing = state == "initializing"
			m.pendingQuestion = askToUserInput{Question: "Continue?"}
			m.input.SetValue("draft answer")
			m.messages = []client.Message{
				{Role: client.RoleAssistant, Content: "assistant text", ToolCalls: []client.ToolCall{{Function: client.FunctionCall{Name: "read_file", Arguments: `{"path":"main.go"}`}}}},
				{Role: client.RoleTool, Name: "search", Content: `{"result":"chat detail"}`},
			}
			m.memory = memory.New(memoryPolicy(config.Default()), m.messages)
			m.agentTraces = []agentTrace{
				{Agent: "coder", Kind: "assistant", Content: "trace assistant text"},
				{Agent: "coder", Kind: "tool_call", Name: "read_file", Content: `{"path":"main.go"}`},
				{Agent: "coder", Kind: "tool_result", Name: "read_file", Content: `{"path":"main.go","line_count":1,"total_lines":1,"content":"trace detail"}`},
				{Agent: "coder", Kind: "tool_result", Name: "search", IsError: true, Content: `{"error":"permission denied","detail":"error body"}`},
			}
			m.agentTraceExpanded = true
			m.resize(120, 38)
			messagesBefore, _ := json.Marshal(m.messages)
			tracesBefore, _ := json.Marshal(m.agentTraces)
			contextBefore, _ := json.Marshal(m.memory.Messages())
			for _, collapsed := range []bool{true, false} {
				updated, command := m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
				m = updated.(model)
				if command != nil || m.toolResultsCollapsed != collapsed || m.input.Value() != "draft answer" || !m.agentTraceExpanded {
					t.Fatalf("toggle changed unrelated state: collapsed=%v input=%q trace=%v cmd=%v", m.toolResultsCollapsed, m.input.Value(), m.agentTraceExpanded, command != nil)
				}
				chat := ansi.Strip(m.viewport.GetContent())
				trace := ansi.Strip(m.agentTraceViewport.GetContent())
				if strings.Contains(chat, "chat detail") == collapsed || strings.Contains(trace, "trace detail") == collapsed || strings.Contains(trace, "error body") == collapsed {
					t.Fatalf("result visibility does not match collapsed=%t:\n%s\n%s", collapsed, chat, trace)
				}
				if !strings.Contains(chat, "assistant text") || !strings.Contains(chat, "→ read main.go") || !strings.Contains(trace, "trace assistant text") || !strings.Contains(trace, `"path": "main.go"`) {
					t.Fatal("toggle hid assistant text or tool calls")
				}
				if collapsed && (!strings.Contains(trace, "✓ read main.go · 1/1 lines") || !strings.Contains(trace, "✗ search · permission denied")) {
					t.Fatalf("trace lost result summaries:\n%s", trace)
				}
			}
			messagesAfter, _ := json.Marshal(m.messages)
			tracesAfter, _ := json.Marshal(m.agentTraces)
			contextAfter, _ := json.Marshal(m.memory.Messages())
			if string(messagesBefore) != string(messagesAfter) || string(tracesBefore) != string(tracesAfter) || string(contextBefore) != string(contextAfter) {
				t.Fatal("toggle mutated conversation, trace, or model context")
			}
		})
	}
}

func TestToolResultPreferenceAppliesToNewResultsAndSurvivesReset(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(config.Default(), &fakeClient{})
	if m.toolResultsCollapsed {
		t.Fatal("tool results must be expanded by default")
	}
	m.toggleToolResults() // works before any results exist
	m.messages = append(m.messages, client.Message{Role: client.RoleTool, Name: "search", Content: "new chat detail"})
	m.refreshTranscript()
	m.appendAgentTrace(agentTrace{Agent: "coder", Kind: "tool_result", Name: "search", Content: "new trace detail"})
	if strings.Contains(m.viewport.GetContent(), "new chat detail") || strings.Contains(m.agentTraceViewport.GetContent(), "new trace detail") {
		t.Fatal("new tool results ignored the collapsed preference")
	}
	m.clearAgentActivities()
	m.resetConversation()
	m.enterChat(config.Default(), &fakeClient{})
	if !m.toolResultsCollapsed {
		t.Fatal("conversation/trace initialization reset the display preference")
	}
	m.resize(96, 24)
	m.applyColorScheme(!m.dark)
	if !m.toolResultsCollapsed || !strings.Contains(ansi.Strip(m.viewChat()), "ctrl+o expand tools") {
		t.Fatal("resize/theme change lost the collapsed preference or hint")
	}
	m.toggleToolResults()
	if !strings.Contains(ansi.Strip(m.viewChat()), "ctrl+o collapse tools") || !strings.Contains(renderHelpContent(m.dark), "ctrl+o") {
		t.Fatal("tool result toggle is not discoverable")
	}
}

func TestToolResultToggleIsIndependentOfTracePanelToggle(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.screen = screenChat
	m.agentTraces = []agentTrace{{Agent: "coder", Kind: "tool_result", Name: "search", Content: "trace detail"}}
	m.agentTraceExpanded = false
	m.resize(100, 30)
	updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(model)
	if m.agentTraceExpanded || !m.toolResultsCollapsed {
		t.Fatal("ctrl+o changed the trace panel state")
	}
	updated, _ = m.updateChatKey(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = updated.(model)
	view := ansi.Strip(m.viewChat())
	if !m.agentTraceExpanded || !m.toolResultsCollapsed || strings.Contains(view, "trace detail") || !strings.Contains(view, "▸ coder · tool result · search") {
		t.Fatalf("ctrl+g ignored the result preference:\n%s", view)
	}
}

func TestToolResultToggleKeepsViewportBlockAnchor(t *testing.T) {
	for _, softWrap := range []bool{false, true} {
		t.Run(fmt.Sprintf("softWrap=%t", softWrap), func(t *testing.T) {
			vp := viewport.New(viewport.WithWidth(12), viewport.WithHeight(3))
			vp.SoftWrap = softWrap
			before := []string{strings.Repeat("tool output ", 8) + "\nline two\nline three", "reading\nthis message\nline three", strings.Repeat("tail\n", 12)}
			after := []string{"tool summary", before[1], before[2]}
			measure := viewport.New(viewport.WithWidth(vp.Width()))
			measure.SoftWrap = softWrap
			measure.SetContent(before[0])
			vp.SetContent(strings.Join(before, "\n\n"))
			vp.SetYOffset(measure.TotalLineCount() + 2) // second line of the next message
			original := vp.YOffset()
			replaceToolResultBlocks(&vp, before, after)
			if vp.YOffset() != 3 || !strings.Contains(vp.View(), "this message") {
				t.Fatalf("collapse lost reading anchor: offset=%d view=%q", vp.YOffset(), vp.View())
			}
			replaceToolResultBlocks(&vp, after, before)
			if vp.YOffset() != original || !strings.Contains(vp.View(), "this message") {
				t.Fatalf("expand lost reading anchor: offset=%d want=%d", vp.YOffset(), original)
			}
			vp.SetYOffset(2) // inside the detail being collapsed
			replaceToolResultBlocks(&vp, before, after)
			if vp.YOffset() != 0 {
				t.Fatalf("hidden detail should anchor to its summary, offset=%d", vp.YOffset())
			}
			vp.GotoBottom()
			replaceToolResultBlocks(&vp, after, before)
			if !vp.AtBottom() {
				t.Fatal("bottom-following viewport lost its position")
			}
		})
	}
}

func TestToolResultTogglePreservesBothViewportAnchors(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.screen = screenChat
	m.agentTraceExpanded = true
	for index := 0; index < 12; index++ {
		m.messages = append(m.messages, client.Message{Role: client.RoleTool, Name: "search", Content: strings.Repeat(fmt.Sprintf("chat %d detail\n", index), 4)})
		m.agentTraces = append(m.agentTraces, agentTrace{Agent: "coder", Kind: "tool_result", Name: fmt.Sprintf("tool_%d", index), Content: strings.Repeat("trace detail\n", 4)})
	}
	m.resize(100, 30)
	chatBlocks, traceBlocks := m.transcriptBlocks(), m.agentTraceBlocks()
	m.viewport.SetYOffset(lipgloss.Height(strings.Join(chatBlocks[:3], "\n\n")) + 1)
	m.agentTraceViewport.SetYOffset(lipgloss.Height(strings.Join(traceBlocks[:3], "\n\n")) + 1)
	m.toggleToolResults()
	if m.viewport.AtBottom() || m.agentTraceViewport.AtBottom() || !strings.Contains(m.agentTraceViewport.View(), "tool_3") {
		t.Fatalf("toggle jumped away from the current block: chat=%d trace=%d", m.viewport.YOffset(), m.agentTraceViewport.YOffset())
	}
}
