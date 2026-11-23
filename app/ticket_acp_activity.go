package app

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/snowmerak/q/diagnostics"
	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

const (
	workerActivityItems         = 32
	workerActivityResponseBytes = 8192
	workerActivityTitleBytes    = 512
)

type ticketACPActivity struct {
	mu       sync.Mutex
	dirty    bool
	snapshot library.WorkerACPActivity
}

func (a *ticketACPActivity) observe(update acp.SessionUpdate) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case update.AgentMessageChunk != nil:
		if a.snapshot.Truncated {
			return
		}
		content := acpContentText(update.AgentMessageChunk.Content)
		preview := activityExcerpt(content, workerActivityResponseBytes-len(a.snapshot.Response))
		a.snapshot.Response += preview
		a.snapshot.Truncated = len(preview) < len(content)
	case update.ToolCall != nil:
		call := update.ToolCall
		a.tool(string(call.ToolCallId), &call.Title, &call.Status)
	case update.ToolCallUpdate != nil:
		call := update.ToolCallUpdate
		a.tool(string(call.ToolCallId), call.Title, call.Status)
	case update.Plan != nil:
		a.snapshot.Plan = a.snapshot.Plan[:0]
		for i, entry := range update.Plan.Entries[:min(len(update.Plan.Entries), workerActivityItems)] {
			a.snapshot.Plan = append(a.snapshot.Plan, library.WorkerActivityItem{
				ID: strconv.Itoa(i), Title: activityExcerpt(entry.Content, workerActivityTitleBytes), Status: string(entry.Status),
			})
		}
	default:
		return
	}
	a.dirty = true
}

func (a *ticketACPActivity) tool(id string, title *string, status *acp.ToolCallStatus) {
	id = activityExcerpt(id, workerActivityTitleBytes)
	i := slices.IndexFunc(a.snapshot.Tools, func(tool library.WorkerActivityItem) bool { return tool.ID == id })
	if i < 0 {
		if len(a.snapshot.Tools) == workerActivityItems {
			a.snapshot.Tools = a.snapshot.Tools[1:]
		}
		i = len(a.snapshot.Tools)
		a.snapshot.Tools = append(a.snapshot.Tools, library.WorkerActivityItem{ID: id, Title: "Tool", Status: "running"})
	}
	if title != nil && strings.TrimSpace(*title) != "" {
		a.snapshot.Tools[i].Title = activityExcerpt(*title, workerActivityTitleBytes)
	}
	if status != nil {
		a.snapshot.Tools[i].Status = acpActivityAction(*status)
	}
}

func (a *ticketACPActivity) publish(ctx context.Context, path string) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				a.flush(ctx, path)
			case <-stop:
				a.flush(ctx, path)
				return
			}
		}
	}()
	return func() { close(stop); <-done }
}

func (a *ticketACPActivity) flush(ctx context.Context, path string) {
	a.mu.Lock()
	if !a.dirty {
		a.mu.Unlock()
		return
	}
	body, err := json.Marshal(a.snapshot)
	a.dirty = false
	a.mu.Unlock()
	if err == nil {
		err = fsreplace.WriteFile(path, body, 0o600)
	}
	if err != nil {
		diagnostics.Logger(ctx).WarnContext(ctx, "ticket.acp_activity.write_failed", "error", err)
	}
}

func activityExcerpt(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}
