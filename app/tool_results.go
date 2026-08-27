package app

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
)

func (m model) transcriptBlocks() []string {
	return renderTranscriptBlocks(
		m.messages, m.transcriptThoughts, m.streamResponse,
		m.viewport.Width(), m.dark, m.toolResultsCollapsed,
	)
}

func (m model) agentTraceBlocks() []string {
	return renderAgentTraceBlocks(m.agentTraces, m.dark, m.agentTraceViewport.Width(), m.toolResultsCollapsed)
}

func (m *model) toggleToolResults() {
	transcript := m.transcriptBlocks()
	traces := m.agentTraceBlocks()
	// This is an application-view preference, not conversation data. Keep it
	// across turns and session switches without changing messages or archives.
	m.toolResultsCollapsed = !m.toolResultsCollapsed
	replaceToolResultBlocks(&m.viewport, transcript, m.transcriptBlocks())
	replaceToolResultBlocks(&m.agentTraceViewport, traces, m.agentTraceBlocks())
}

// A toggle changes block heights but not their order. Anchor the viewport to
// the same message/trace instead of the old absolute line or the bottom. This
// also avoids relying on ToolCallID, which need not be unique across turns.
func replaceToolResultBlocks(vp *viewport.Model, before, after []string) {
	wasAtBottom := vp.AtBottom()
	blockIndex, offset := 0, vp.YOffset()
	measure := viewport.New(viewport.WithWidth(vp.Width()))
	measure.SoftWrap = vp.SoftWrap
	blockHeight := func(block string) int {
		measure.SetContent(block)
		return measure.TotalLineCount()
	}
	if !wasAtBottom {
		for index, block := range before {
			blockIndex = index
			height := blockHeight(block)
			if offset < height+1 || index == len(before)-1 {
				break
			}
			offset -= height + 1 // one empty line between blocks
		}
	}
	vp.SetContent(strings.Join(after, "\n\n"))
	if wasAtBottom {
		vp.GotoBottom()
		return
	}
	y := 0
	for index, block := range after {
		height := blockHeight(block)
		if index == blockIndex {
			if offset >= height {
				offset = 0 // the previously visible detail is now hidden
			}
			vp.SetYOffset(y + offset)
			return
		}
		y += height + 1
	}
	vp.SetYOffset(0)
}
