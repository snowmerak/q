package app

import (
	"context"
	"time"

	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

const acpUsageUpdateTimeout = time.Second

func acpUsageUpdate(stats memory.Stats) (acp.SessionUpdate, bool) {
	if stats.ContextWindow <= 0 {
		return acp.SessionUpdate{}, false
	}
	return acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{
		Used: max(0, stats.PredictedTokens),
		Size: stats.ContextWindow,
	}}, true
}

func (a *acpAgent) emitUsageUpdate() error {
	if a == nil || a.state == nil || a.state.memory == nil {
		return nil
	}
	update, ok := acpUsageUpdate(a.state.memory.Stats())
	if !ok {
		return nil
	}

	parent := a.state.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, acpUsageUpdateTimeout)
	defer cancel()
	return a.updateContext(ctx, update)
}

func (a *acpAgent) emitUsageUpdateBestEffort() {
	if err := a.emitUsageUpdate(); err != nil && a.logger != nil {
		a.logger.Warn("emit ACP session usage", "session_id", a.sessionID, "error", err)
	}
}
