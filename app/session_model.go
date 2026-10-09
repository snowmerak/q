package app

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gatewayconfig"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/thinker"
)

// newRuntimeModel constructs execution state without terminal widgets. The TUI
// constructor adds its own controls; Studio and ACP use this state directly.
func newRuntimeModel(ctx context.Context, store config.Store, factory clientFactory, runtime providerRuntime) model {
	m := model{
		hostState:      hostState{ctx: ctx, store: store, factory: factory, runtime: runtime, headless: true},
		lifecycleState: lifecycleState{thinkerSerial: &thinker.Serial{}},
	}
	m.resetSessionLearning()
	if runtime != nil {
		m.gatewayConfig = runtime.Config()
	}
	m.gatewaySettingsStore = gatewayconfig.Store{Dir: store.Dir}
	m.librarySettingsStore = qlibrary.ConfigStore{Dir: store.Dir}
	m.mcpSettingsStore = mcpconfig.Store{Dir: store.Dir}
	return m
}

// updateExecution dispatches only session messages, never terminal input,
// window changes, or settings-screen messages. Bubble Tea remains the shared
// serializer for foreground execution and background learning.
func (m model) updateExecution(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case agentEventMsg:
		return m.updateAgentEvent(message)
	case chatResultMsg:
		return m.updateChatResult(message)
	case compactionResultMsg:
		return m.updateCompactionResult(message)
	case thinkerResultMsg:
		return m.updateThinkerResult(message)
	case delegationRecoveryMsg:
		command := m.acceptDelegationRecovery(message)
		return m, command
	default:
		return m, nil
	}
}

func (m *model) focusChatInput() tea.Cmd {
	if m.headless {
		return nil
	}
	return m.input.Focus()
}

func (m *model) resetChatInput() {
	if !m.headless {
		m.input.Reset()
	}
}

func (m *model) blurChatInput() {
	if !m.headless {
		m.input.Blur()
	}
}

func (m *model) setChatPlaceholder(value string) {
	if !m.headless {
		m.input.Placeholder = value
	}
}

func (m model) chatTick() tea.Cmd {
	if m.headless {
		return nil
	}
	return m.spinner.Tick
}
