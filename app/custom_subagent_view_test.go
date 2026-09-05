package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
)

func customViewFixture(t *testing.T, width, height int, dark bool) model {
	t.Helper()
	store := config.Store{Dir: t.TempDir()}
	v := config.Default()
	v.Provider.Model = "analysis-model"
	v.Agents.Roles = map[string]config.AgentConfig{"analyst": {Model: "analysis-model"}}
	if err := store.Save(v); err != nil {
		t.Fatal(err)
	}
	m := newModel(t.Context(), store, nil)
	m.config = v
	m.resize(width, height)
	m.applyColorScheme(dark)
	m.screen = screenCustom
	m.custom.entries = []subagent.ProfileEntry{{Profile: subagent.Profile{Version: 1, Name: "security-reviewer", Role: "analyst", Description: "Investigate concrete security defects.", SystemPrompt: "Inspect the requested code.\nReport evidence and the smallest useful fix.", Tools: []string{"read_file", "run_command", "wait"}}, Path: "C:/workspace/.q/subagents/security-reviewer.yaml", Scope: "workspace"}}
	return m
}

func TestCustomViewsFollowLayoutAndExposeDetails(t *testing.T) {
	for _, size := range [][2]int{{100, 36}, {60, 28}, {40, 24}} {
		for _, dark := range []bool{true, false} {
			for _, mode := range []string{"list", "profile-edit", "tools"} {
				name := fmt.Sprintf("%dx%d-%t-%s", size[0], size[1], dark, mode)
				t.Run(name, func(t *testing.T) {
					m := customViewFixture(t, size[0], size[1], dark)
					switch mode {
					case "profile-edit", "tools":
						updated, _ := m.beginCustomEdit(false)
						m = updated.(model)
						m.custom.field = 4
						if mode == "tools" {
							m.custom.field = 5
							updated, _ = m.openCustomPicker()
							m = updated.(model)
						}
					}
					view := m.viewCustom()
					plain := ansi.Strip(view)
					if !strings.Contains(plain, "q · Subagents") {
						t.Fatal("missing shared header")
					}
					if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
						t.Fatalf("overflow %dx%d:\n%s", lipgloss.Width(view), lipgloss.Height(view), plain)
					}
					if mode == "list" {
						for _, s := range []string{"Subagents 1", "LIST", "DETAILS", "security-reviewer", "analyst · workspace"} {
							if !strings.Contains(plain, s) {
								t.Fatalf("missing %s:\n%s", s, plain)
							}
						}
					}
					if strings.Contains(plain, "Roles 1") {
						t.Fatalf("role management leaked into the subagent screen:\n%s", plain)
					}
					if mode == "list" && size[0] == 100 {
						for _, s := range []string{"CONFIGURATION", "SYSTEM PROMPT", "TOOLS", "3 selected", "run_command", "security-reviewer.yaml"} {
							if !strings.Contains(plain, s) {
								t.Fatalf("missing %s", s)
							}
						}
					}
					if dir := os.Getenv("Q_CUSTOM_UI_SNAPSHOTS"); dir != "" {
						if err := os.MkdirAll(dir, 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(view), 0600); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}

func TestCustomDeleteRequiresConfirmation(t *testing.T) {
	m := customViewFixture(t, 40, 24, true)
	p := subagent.Profile{Version: 1, Name: "delete-me", Role: "analyst", SystemPrompt: "Temporary", Tools: []string{}}
	if err := m.customStore().Save(p, "global", nil); err != nil {
		t.Fatal(err)
	}
	m.reloadCustom()
	for index, entry := range m.custom.entries {
		if entry.Profile.Name == p.Name {
			m.custom.cursor = index
		}
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = updated.(model)
	if !m.custom.confirmDelete || !strings.Contains(ansi.Strip(m.viewCustom()), "d/y/enter confirm") {
		t.Fatal("delete confirmation was not shown")
	}
	if _, err := m.customStore().Get(p.Name); err != nil {
		t.Fatal("first delete key removed the profile", err)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(model)
	if m.custom.confirmDelete || m.status != "Delete canceled" {
		t.Fatal("delete confirmation did not cancel")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = updated.(model)
	if _, err := m.customStore().Get(p.Name); err == nil {
		t.Fatal("confirmed delete kept the profile")
	}
}

func TestCustomReloadKeepsSelectedEntry(t *testing.T) {
	m := customViewFixture(t, 100, 36, true)
	for _, name := range []string{"alpha", "omega"} {
		if err := m.customStore().Save(subagent.Profile{Version: 1, Name: name, Role: "analyst", SystemPrompt: "Inspect", Tools: []string{}}, "global", nil); err != nil {
			t.Fatal(err)
		}
	}
	m.reloadCustom()
	for index, entry := range m.custom.entries {
		if entry.Profile.Name == "omega" {
			m.custom.cursor = index
		}
	}
	m.reloadCustom()
	if m.custom.entries[m.custom.cursor].Profile.Name != "omega" {
		t.Fatal("reload moved the profile selection")
	}
}

func TestCustomDetailsScrollWithoutMovingSelection(t *testing.T) {
	m := customViewFixture(t, 100, 24, true)
	m.custom.entries[0].Profile.SystemPrompt = strings.Repeat("More diagnostic context\n", 30) + "FINAL DETAIL"
	before := m.viewCustom()
	if strings.Contains(before, "FINAL DETAIL") {
		t.Fatal("fixture should require scrolling")
	}
	for i := 0; i < 8; i++ {
		updated, _ := m.updateCustom(tea.KeyPressMsg{Code: tea.KeyPgDown})
		m = updated.(model)
	}
	if m.custom.cursor != 0 || !strings.Contains(m.viewCustom(), "FINAL DETAIL") {
		t.Fatal("details did not scroll independently")
	}
	bottom := m.custom.panelOffset
	updated, _ := m.updateCustom(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = updated.(model)
	if m.custom.panelOffset >= bottom {
		t.Fatal("overscrolling prevented paging up")
	}
}

func TestCustomAddShortcutMatchesOtherManagementScreens(t *testing.T) {
	m := customViewFixture(t, 100, 36, true)
	if !strings.Contains(ansi.Strip(m.viewCustom()), "a add") {
		t.Fatal("add shortcut missing from help")
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = updated.(model)
	if !m.custom.editing || m.custom.original != nil {
		t.Fatal("a did not open the subagent form")
	}
}
