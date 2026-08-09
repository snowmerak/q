package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/loom"
)

type loomController interface {
	ConfigureLoom(loom.StoreOptions) error
	LoomStats(context.Context) (loom.Stats, error)
	CollectLoom(context.Context, bool) (loom.GCResult, error)
}

func (m model) enterLoom() (tea.Model, tea.Cmd) {
	m.screen = screenLoom
	m.input.Blur()
	m.loomDraft = m.config.EffectiveLoom()
	m.loomFocus = loomAutoGC
	m.loomBusy = true
	m.status = "Loading Loom storage…"
	m.loomInputs[0].SetValue(strconv.Itoa(m.loomDraft.MaximumArtifactMiB))
	m.loomInputs[1].SetValue(strconv.Itoa(m.loomDraft.MaximumStoreMiB))
	m.loomInputs[2].SetValue(formatPercentInput(m.loomDraft.GC.TriggerRatio))
	m.loomInputs[3].SetValue(formatPercentInput(m.loomDraft.GC.TargetRatio))
	m.loomInputs[4].SetValue(strconv.Itoa(m.loomDraft.GC.GraceHours))
	m.blurLoomInputs()
	controller, ok := m.toolRuntime.(loomController)
	if !ok {
		m.loomBusy = false
		m.status = "Loom runtime is unavailable"
		return m, nil
	}
	return m, func() tea.Msg {
		stats, err := controller.LoomStats(m.ctx)
		return loomStatsMsg{stats: stats, err: err}
	}
}

func (m model) updateLoom(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.loomBusy {
		if key.String() == "esc" {
			m.screen = screenChat
			m.status = ""
			return m, m.input.Focus()
		}
		return m, nil
	}
	switch key.String() {
	case "esc":
		m.screen = screenChat
		m.status = ""
		m.blurLoomInputs()
		return m, m.input.Focus()
	case "up", "shift+tab":
		m.loomFocus = (m.loomFocus - 1 + loomControlCount) % loomControlCount
		return m, m.loomFocusCommand()
	case "down", "tab":
		m.loomFocus = (m.loomFocus + 1) % loomControlCount
		return m, m.loomFocusCommand()
	case " ":
		if m.loomFocus == loomAutoGC {
			m.loomDraft.GC.Disabled = !m.loomDraft.GC.Disabled
		}
		return m, nil
	case "enter":
		switch m.loomFocus {
		case loomAutoGC:
			m.loomDraft.GC.Disabled = !m.loomDraft.GC.Disabled
			return m, nil
		case loomSave:
			return m.applyLoomAction("save")
		case loomDryRun:
			return m.applyLoomAction("dry-run")
		case loomCollect:
			return m.applyLoomAction("collect")
		default:
			m.loomFocus = (m.loomFocus + 1) % loomControlCount
			return m, m.loomFocusCommand()
		}
	}
	if input := m.focusedLoomInput(); input != nil {
		var command tea.Cmd
		*input, command = input.Update(key)
		return m, command
	}
	return m, nil
}

func (m model) applyLoomAction(action string) (tea.Model, tea.Cmd) {
	loomConfig, err := m.parseLoomConfig()
	if err != nil {
		m.status = err.Error()
		return m, nil
	}
	value := m.config
	value.Loom = loomConfig
	if err := value.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	controller, ok := m.toolRuntime.(loomController)
	if !ok && action != "save" {
		m.status = "Loom runtime is unavailable"
		return m, nil
	}
	m.loomBusy = true
	m.status = "Saving Loom settings…"
	if action == "dry-run" {
		m.status = "Previewing Loom GC…"
	} else if action == "collect" {
		m.status = "Running Loom GC…"
	}
	m.blurLoomInputs()
	store := m.store
	archive := m.archive
	ctx := m.ctx
	return m, func() tea.Msg {
		if err := store.Save(value); err != nil {
			return loomActionMsg{action: action, err: err}
		}
		if ok {
			if err := controller.ConfigureLoom(value.LoomStoreOptions(nil)); err != nil {
				return loomActionMsg{config: value, action: action, err: err}
			}
		}
		var result *loom.GCResult
		if action != "save" {
			if archive != nil {
				if err := archive.Flush(); err != nil {
					return loomActionMsg{config: value, action: action, err: fmt.Errorf("flush archive before Loom GC: %w", err)}
				}
			}
			collected, err := controller.CollectLoom(ctx, action == "dry-run")
			if err != nil {
				return loomActionMsg{config: value, action: action, err: err}
			}
			result = &collected
		}
		var stats loom.Stats
		if ok {
			var err error
			stats, err = controller.LoomStats(ctx)
			if err != nil {
				return loomActionMsg{config: value, action: action, err: err}
			}
		}
		return loomActionMsg{config: value, stats: stats, result: result, action: action}
	}
}

func (m model) parseLoomConfig() (config.LoomConfig, error) {
	parseInt := func(index int, label string) (int, error) {
		value, err := strconv.Atoi(strings.TrimSpace(m.loomInputs[index].Value()))
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", label)
		}
		return value, nil
	}
	artifact, err := parseInt(0, "Maximum artifact MiB")
	if err != nil {
		return config.LoomConfig{}, err
	}
	store, err := parseInt(1, "Maximum store MiB")
	if err != nil {
		return config.LoomConfig{}, err
	}
	trigger, err := strconv.ParseFloat(strings.TrimSpace(m.loomInputs[2].Value()), 64)
	if err != nil {
		return config.LoomConfig{}, errors.New("GC trigger must be a percentage")
	}
	target, err := strconv.ParseFloat(strings.TrimSpace(m.loomInputs[3].Value()), 64)
	if err != nil {
		return config.LoomConfig{}, errors.New("GC target must be a percentage")
	}
	grace, err := parseInt(4, "GC grace hours")
	if err != nil {
		return config.LoomConfig{}, err
	}
	return config.LoomConfig{
		MaximumArtifactMiB: artifact,
		MaximumStoreMiB:    store,
		GC: config.LoomGCConfig{
			Disabled: m.loomDraft.GC.Disabled, TriggerRatio: trigger / 100,
			TargetRatio: target / 100, GraceHours: grace,
		},
	}, nil
}

func (m *model) blurLoomInputs() {
	for index := range m.loomInputs {
		m.loomInputs[index].Blur()
	}
}

func (m *model) focusedLoomInput() *textinput.Model {
	if m.loomFocus < loomMaximumArtifact || m.loomFocus > loomGCGrace {
		return nil
	}
	return &m.loomInputs[m.loomFocus-loomMaximumArtifact]
}

func (m *model) loomFocusCommand() tea.Cmd {
	m.blurLoomInputs()
	if input := m.focusedLoomInput(); input != nil && !m.loomBusy {
		return input.Focus()
	}
	return nil
}

func (m model) viewLoom() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · Loom storage"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	maximum := int64(m.loomDraft.MaximumStoreMiB) << 20
	body.WriteString(subtleStyle.Render(fmt.Sprintf(
		"%s / %s · %d artifacts · %d blobs", formatBytes(m.loomStats.Bytes), formatBytes(maximum), m.loomStats.Artifacts, m.loomStats.Blobs,
	)))
	body.WriteString("\n\n")
	autoState := "enabled"
	if m.loomDraft.GC.Disabled {
		autoState = "disabled"
	}
	writeLoomControl(&body, m.loomFocus == loomAutoGC, "Automatic GC", autoState)
	for index := range m.loomInputs {
		prefix := "  "
		if m.loomFocus == index+loomMaximumArtifact {
			prefix = "› "
		}
		body.WriteString(prefix)
		body.WriteString(m.loomInputs[index].View())
		body.WriteString("\n")
	}
	body.WriteString("\n")
	writeLoomControl(&body, m.loomFocus == loomSave, "Save settings", "")
	writeLoomControl(&body, m.loomFocus == loomDryRun, "Preview GC", "keeps all files")
	writeLoomControl(&body, m.loomFocus == loomCollect, "Run GC", "removes unreachable artifacts")
	if m.status != "" {
		body.WriteString("\n")
		style := subtleStyle
		if !m.loomBusy && strings.Contains(strings.ToLower(m.status), "must") {
			style = errorStyle
		}
		body.WriteString(style.Render(m.status))
		body.WriteString("\n")
	}
	body.WriteString("\n")
	body.WriteString(helpStyle.Render("tab/↑/↓ navigate · space toggle · enter apply · esc chat"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func writeLoomControl(body *strings.Builder, active bool, label, detail string) {
	prefix := "  "
	style := subtleStyle
	if active {
		prefix = "› "
		style = activeLabelStyle
	}
	body.WriteString(prefix)
	body.WriteString(style.Render(label))
	if detail != "" {
		body.WriteString(subtleStyle.Render("  · " + detail))
	}
	body.WriteString("\n")
}

func formatPercentInput(value float64) string {
	return strconv.FormatFloat(value*100, 'f', -1, 64)
}

func formatBytes(bytes int64) string {
	const mib = 1 << 20
	if bytes >= mib {
		return fmt.Sprintf("%.1f MiB", float64(bytes)/mib)
	}
	if bytes >= 1<<10 {
		return fmt.Sprintf("%.1f KiB", float64(bytes)/(1<<10))
	}
	return fmt.Sprintf("%d B", bytes)
}
