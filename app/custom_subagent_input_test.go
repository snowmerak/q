package app

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
	"strings"
	"testing"
)

func TestCustomSaveTerminalInputBytes(t *testing.T) {
	for _, input := range []string{"\x13", "\x1b[83;31;19;1;8;1_", "\x1b[115;5u", "\x1b[12~"} {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			m := customViewFixture(t, 100, 36, true)
			updated, _ := m.beginCustomEdit(true)
			m = updated.(model)
			m.custom.inputs[0].SetValue("save-from-terminal")
			m.custom.prompt.SetValue("Keep my prompt")
			m.custom.field = 5
			decoder := uv.EventDecoder{}
			n, event := decoder.Decode([]byte(input))
			key, ok := event.(uv.KeyPressEvent)
			if n != len(input) || !ok {
				t.Fatalf("decoder: %d %T %+v", n, event, event)
			}
			updated, _ = m.Update(tea.KeyPressMsg(key))
			m = updated.(model)
			if m.custom.editing || m.status != "Saved" {
				t.Fatalf("key=%+v: %s", key, m.status)
			}
		})
	}
}

func TestCustomSaveActionWithoutControlKey(t *testing.T) {
	m := customViewFixture(t, 100, 36, true)
	updated, _ := m.beginCustomEdit(true)
	m = updated.(model)
	m.custom.inputs[0].SetValue("save-action")
	m.custom.prompt.SetValue("Keep my prompt")
	m.custom.field = 5
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = updated.(model)
	if m.custom.field != len(m.custom.inputs)-1 {
		t.Fatal("Tools Tab did not select Save")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.custom.editing || m.status != "Saved" {
		t.Fatal(m.status)
	}
}

func TestCustomSaveFromEveryEditorState(t *testing.T) {
	for field := 0; field < 6; field++ {
		for _, picker := range []bool{false, true} {
			if picker && (field == 0 || field == 1 || field == 4) {
				continue
			}
			t.Run(fmt.Sprintf("field=%d/picker=%v", field, picker), func(t *testing.T) {
				m := customViewFixture(t, 100, 36, true)
				updated, _ := m.beginCustomEdit(true)
				m = updated.(model)
				m.custom.inputs[0].SetValue("save-test")
				m.custom.prompt.SetValue("Keep this prompt")
				m.custom.field = field
				if picker {
					updated, _ = m.openCustomPicker()
					m = updated.(model)
				}
				updated, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
				m = updated.(model)
				if m.custom.editing || m.custom.picker || m.status != "Saved" {
					t.Fatalf("save ignored: editing=%v picker=%v status=%s", m.custom.editing, m.custom.picker, m.status)
				}
				p, err := m.customStore().Get("save-test")
				if err != nil || p.Profile.SystemPrompt != "Keep this prompt" {
					t.Fatal("profile not persisted", err)
				}
			})
		}
	}
}

func TestCustomPickerSaveValidationPreservesDraft(t *testing.T) {
	m := customViewFixture(t, 100, 36, true)
	updated, _ := m.beginCustomEdit(true)
	m = updated.(model)
	m.custom.inputs[0].SetValue("draft")
	m.custom.field = 5
	updated, _ = m.openCustomPicker()
	m = updated.(model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(model)
	if !m.custom.editing || m.custom.inputs[0].Value() != "draft" || !strings.Contains(m.status, "system_prompt") {
		t.Fatalf("validation feedback missing: %s", m.status)
	}
}

func TestCustomScreenshotSaveShowsNameErrorAtField(t *testing.T) {
	m := customViewFixture(t, 180, 36, true)
	updated, _ := m.beginCustomEdit(true)
	m = updated.(model)
	m.custom.inputs[0].SetValue("Apple Developer")
	m.custom.inputs[1].SetValue("Apple Developer")
	m.custom.prompt.SetValue("You are a Apple Developer.")
	m.custom.tools["read_file"] = true
	m.custom.field = 5
	updated, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(model)
	if !m.custom.editing || m.custom.field != 0 || !strings.Contains(m.status, "apple-developer") {
		t.Fatalf("save failed without actionable field feedback: field=%d status=%s", m.custom.field, m.status)
	}
	if !strings.Contains(m.viewCustom(), "Try apple-developer") {
		t.Fatal("name guidance is not visible")
	}
	m.custom.inputs[0].SetValue("apple-developer")
	updated, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(model)
	profile, err := m.customStore().Get("apple-developer")
	if err != nil || m.custom.editing || profile.Profile.Description != "Apple Developer" || profile.Profile.SystemPrompt != "You are a Apple Developer." {
		t.Fatalf("corrected draft was not saved intact: %v %s", err, m.status)
	}
}

func TestCustomSaveKeyWithTextPayload(t *testing.T) {
	m := customViewFixture(t, 100, 36, true)
	updated, _ := m.beginCustomEdit(true)
	m = updated.(model)
	m.custom.inputs[0].SetValue("apple-developer")
	m.custom.prompt.SetValue("You are an Apple Developer.")
	m.custom.field = 5
	updated, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl, Text: "s"})
	m = updated.(model)
	if m.custom.editing || m.status != "Saved" {
		t.Fatalf("Ctrl+S with text payload ignored: %s", m.status)
	}
}

func TestCustomToolPickerSelectsBuiltinAndMCP(t *testing.T) {
	m := customViewFixture(t, 100, 36, true)
	tool := func(name string) client.Tool { return client.Tool{Function: client.FunctionDefinition{Name: name}} }
	m.toolRuntime = &roleCatalogTools{toolsByRole: map[string][]client.Tool{"default": {tool("read_file"), tool("mcp_docs__read")}}}
	updated, _ := m.beginCustomEdit(true)
	m = updated.(model)
	m.custom.field = 5
	updated, _ = m.openCustomPicker()
	m = updated.(model)
	for _, name := range []string{"read_file", "mcp_docs__read"} {
		for i, option := range m.custom.options {
			if option == name {
				m.custom.pickCursor = i
			}
		}
		updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
		m = updated.(model)
		if !m.custom.tools[name] {
			t.Fatalf("space did not select %s", name)
		}
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	m = updated.(model)
	if m.custom.picker || m.custom.field != 4 {
		t.Fatal("picker backward exit failed")
	}
}

func TestCustomSelectorsAndPromptExit(t *testing.T) {
	m := customViewFixture(t, 100, 36, true)
	m.workspaceStore = &workspace.Store{Root: t.TempDir()}
	send := func(k tea.KeyPressMsg) { t.Helper(); updated, _ := m.Update(k); m = updated.(model) }
	send(tea.KeyPressMsg{Code: 'a', Text: "a"})
	for i := 0; i < 2; i++ {
		send(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	before := m.custom.inputs[2].Value()
	send(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.custom.inputs[2].Value() == before {
		t.Fatal("scope did not cycle")
	}
	send(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.custom.inputs[2].Value() != before {
		t.Fatal("scope did not wrap")
	}
	send(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.custom.inputs[2].Value() != before {
		t.Fatal("selector accepted free text")
	}
	send(tea.KeyPressMsg{Code: tea.KeyTab})
	before = m.custom.inputs[3].Value()
	send(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.custom.inputs[3].Value() == before {
		t.Fatal("role did not cycle")
	}
	send(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.custom.inputs[3].Value() != before {
		t.Fatal("role reverse cycle failed")
	}
	send(tea.KeyPressMsg{Code: tea.KeyTab})
	m.custom.prompt.SetValue("keep this")
	send(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.custom.field != 3 {
		t.Fatal("shift-tab did not exit prompt backwards")
	}
	send(tea.KeyPressMsg{Code: tea.KeyTab})
	send(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.custom.editing || m.custom.field != 3 || m.custom.prompt.Value() != "keep this" {
		t.Fatal("escape discarded prompt")
	}
	send(tea.KeyPressMsg{Code: tea.KeyTab})
	send(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if m.custom.field != 3 {
		t.Fatal("ctrl-up did not exit prompt")
	}
}

func TestCustomEditorKeyboardInput(t *testing.T) {
	m := customViewFixture(t, 100, 36, true)
	send := func(key tea.KeyPressMsg) { t.Helper(); updated, _ := m.Update(key); m = updated.(model) }
	send(tea.KeyPressMsg{Code: 'a', Text: "a"})
	for _, r := range "novelwriter" {
		send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if m.custom.inputs[0].Value() != "novelwriter" {
		t.Fatal("name input ignored")
	}
	send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.custom.field != 1 {
		t.Fatal("enter did not advance from name")
	}
	send(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.custom.field != 2 {
		t.Fatal("down did not advance field")
	}
	send(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.custom.field != 1 {
		t.Fatal("up did not return to description")
	}
	for i := 0; i < 3; i++ {
		send(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	if m.custom.field != 4 {
		t.Fatal("tab did not navigate")
	}
	for _, r := range "Write a novel" {
		send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if m.custom.prompt.Value() != "Write a novel" {
		t.Fatalf("prompt input ignored: focused=%v value=%q", m.custom.prompt.Focused(), m.custom.prompt.Value())
	}
	send(tea.KeyPressMsg{Code: tea.KeyEnter})
	send(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.custom.prompt.Value() != "Write a novel\nx" || m.custom.field != 4 {
		t.Fatal("multiline editing broken")
	}
	send(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.custom.editing {
		t.Fatalf("keyboard save failed: %s", m.status)
	}
	e, err := m.customStore().Get("novelwriter")
	if err != nil || e.Profile.SystemPrompt != "Write a novel\nx" {
		t.Fatalf("saved profile = %+v, %v", e, err)
	}
	send(tea.KeyPressMsg{Code: 'a', Text: "a"})
	send(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.custom.editing {
		t.Fatal("escape ignored")
	}
}
