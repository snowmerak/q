package app

import tea "charm.land/bubbletea/v2"

func (m model) updateScreenMessage(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok {
		if key.String() == "ctrl+c" {
			if m.screen == screenChat && m.waiting {
				return m.interruptTurn()
			}
			return m, tea.Quit
		}
		if key.String() == "ctrl+h" {
			if m.screen == screenHelp {
				if m.isStandaloneScreen(screenHelp) {
					return m, tea.Quit
				}
				return m.leaveHelp()
			}
			return m.enterHelp()
		}
		if m.screen == screenSetup {
			return m.updateSetup(key)
		}
		if m.screen == screenProviders {
			return m.updateProviders(key)
		}
		if m.screen == screenGateway {
			return m.updateGateway(key)
		}
		if m.screen == screenGatewayNetwork {
			return m.updateGatewayNetwork(key)
		}
		if m.screen == screenGatewayKeys {
			return m.updateGatewayKeys(key)
		}
		if m.screen == screenLibrary {
			return m.updateLibrary(key)
		}
		if m.screen == screenModels {
			return m.updateModelPicker(key)
		}
		if m.screen == screenLoom {
			return m.updateLoom(key)
		}
		if m.screen == screenIgnore {
			return m.updateIgnore(key)
		}
		if m.screen == screenSkills {
			return m.updateSkills(key)
		}
		if m.screen == screenLSP {
			return m.updateLSP(key)
		}
		if m.screen == screenMCP {
			return m.updateMCP(key)
		}
		if m.screen == screenCustom {
			return m.updateCustom(key)
		}
		if m.screen == screenHelp {
			return m.updateHelp(key)
		}
		if m.screen == screenSessions {
			return m.updateSessions(key)
		}
		if m.screen == screenChanges {
			return m.updateChanges(key)
		}
		return m.updateChatKey(key)
	}

	if m.screen == screenChanges {
		if m.changes.diffFocused {
			var command tea.Cmd
			m.changes.viewport, command = m.changes.viewport.Update(message)
			return m, command
		}
		return m, nil
	}
	if m.screen == screenIgnore {
		before := m.ignoreEditor.Value()
		var command tea.Cmd
		m.ignoreEditor, command = m.ignoreEditor.Update(message)
		if m.ignoreEditor.Value() != before {
			m.ignoreDiscardArmed = false
			m.status = ""
		}
		return m, command
	}
	if m.screen == screenHelp {
		var command tea.Cmd
		m.helpViewport, command = m.helpViewport.Update(message)
		return m, command
	}
	if m.screen == screenSkills {
		if m.skillsMode == skillModeAdd {
			var command tea.Cmd
			m.skillsInput, command = m.skillsInput.Update(message)
			return m, command
		}
		return m, nil
	}
	if m.screen == screenLSP && m.lspMode != lspModeList {
		var command tea.Cmd
		m.lspInputs[m.lspFormFocus], command = m.lspInputs[m.lspFormFocus].Update(message)
		return m, command
	}
	if m.screen == screenMCP && m.mcpMode != mcpModeList {
		var command tea.Cmd
		m.mcpInputs[m.mcpFormFocus], command = m.mcpInputs[m.mcpFormFocus].Update(message)
		return m, command
	}
	if m.screen == screenCustom {
		if m.custom.connections && m.agentsMode != agentsModeList {
			var command tea.Cmd
			m.agentsInputs[m.agentsFormFocus], command = m.agentsInputs[m.agentsFormFocus].Update(message)
			return m, command
		}
		return m.updateCustomInput(message)
	}
	if m.screen == screenGatewayNetwork || (m.screen == screenGatewayKeys && m.gatewayKeyAdding) {
		return m.updateGatewayInput(message)
	}
	if m.screen == screenLibrary {
		return m.updateLibraryInput(message)
	}

	if m.screen == screenChat {
		var commands []tea.Cmd
		var command tea.Cmd
		if m.asking {
			m.questionViewport, command = m.questionViewport.Update(message)
		} else if m.waiting && m.agentTraceExpanded && len(m.agentTraces) > 0 {
			m.agentTraceViewport, command = m.agentTraceViewport.Update(message)
		} else {
			m.viewport, command = m.viewport.Update(message)
		}
		commands = append(commands, command)
		if (m.waiting || m.initializing) && !m.asking {
			m.spinner, command = m.spinner.Update(message)
			commands = append(commands, command)
		} else {
			m.input, command = m.input.Update(message)
			m.syncSlashCompletion()
			commands = append(commands, command)
		}
		return m, tea.Batch(commands...)
	}
	return m, nil
}
