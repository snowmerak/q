package app

import (
	"context"
	"time"

	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

const acpUsageUpdateTimeout = time.Second

// acpUsagePublisher owns the disposable usage projection for one serialized
// prompt. Its single pending slot keeps model and tool progress independent of
// a slow client while preserving the newest snapshot for the final flush.
type acpUsagePublisher struct {
	updates chan acp.SessionUpdate
	finish  chan acpUsageFinal
	done    chan struct{}
	cancel  context.CancelFunc
}

type acpUsageFinal struct {
	update    acp.SessionUpdate
	available bool
}

func acpUsageUpdate(stats memory.Stats) (acp.SessionUpdate, bool) {
	if stats.ContextWindow <= 0 {
		return acp.SessionUpdate{}, false
	}
	return acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{
		Used: max(0, stats.PredictedTokens),
		Size: stats.ContextWindow,
	}}, true
}

func (a *acpAgent) currentUsageUpdate() (acp.SessionUpdate, bool) {
	if a == nil || a.state == nil || a.state.memory == nil {
		return acp.SessionUpdate{}, false
	}
	return acpUsageUpdate(a.state.memory.Stats())
}

func (a *acpAgent) startUsageUpdates() {
	if _, ok := a.currentUsageUpdate(); !ok {
		return
	}
	parent := a.state.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	publisher := &acpUsagePublisher{
		updates: make(chan acp.SessionUpdate, 1),
		finish:  make(chan acpUsageFinal, 1),
		done:    make(chan struct{}),
		cancel:  cancel,
	}
	a.usagePublisher = publisher
	go publisher.run(ctx, a)
}

func (a *acpAgent) publishUsageUpdate() {
	publisher := a.usagePublisher
	if publisher == nil {
		return
	}
	update, ok := a.currentUsageUpdate()
	if !ok {
		return
	}
	publisher.publish(update)
}

func (a *acpAgent) finishUsageUpdates() {
	publisher := a.usagePublisher
	a.usagePublisher = nil
	if publisher == nil {
		return
	}
	update, available := a.currentUsageUpdate()
	publisher.finish <- acpUsageFinal{update: update, available: available}
	cancelTimer := time.AfterFunc(acpUsageUpdateTimeout, publisher.cancel)
	<-publisher.done
	cancelTimer.Stop()
	publisher.cancel()
}

func (p *acpUsagePublisher) publish(update acp.SessionUpdate) {
	// Replace a pending snapshot instead of building an accounting backlog. A
	// snapshot already being written is allowed to finish in the sole worker.
	select {
	case p.updates <- update:
		return
	default:
	}
	select {
	case <-p.updates:
	default:
	}
	select {
	case p.updates <- update:
	default:
	}
}

func (p *acpUsagePublisher) run(ctx context.Context, agent *acpAgent) {
	defer close(p.done)
	lastUsed, lastSize, hasLast := 0, 0, false
	failureLogged := false
	send := func(update acp.SessionUpdate) {
		usage := update.UsageUpdate
		if usage == nil || hasLast && usage.Used == lastUsed && usage.Size == lastSize {
			return
		}
		sendContext, cancel := context.WithTimeout(ctx, acpUsageUpdateTimeout)
		err := agent.updateContext(sendContext, update)
		cancel()
		if err != nil {
			if !failureLogged && agent.logger != nil {
				agent.logger.Warn("emit ACP session usage", "session_id", agent.sessionID, "error", err)
				failureLogged = true
			}
			return
		}
		lastUsed, lastSize, hasLast = usage.Used, usage.Size, true
	}

	for {
		select {
		case update := <-p.updates:
			send(update)
		case final := <-p.finish:
			if final.available {
				send(final.update)
			}
			return
		case <-ctx.Done():
			return
		}
	}
}
