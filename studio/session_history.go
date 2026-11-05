package studio

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

type sessionHistory struct {
	Start int `json:"start"`
	End   int `json:"end"`
	Total int `json:"total"`
}

// History pages run newest to oldest, while messages within a page stay in
// chronological order. The limit is soft: a user turn and its tool exchanges
// stay together, including parallel calls and their later results.
func requestedSessionHistory(request *http.Request, root string, store workspace.Store, value workspace.Session) (sessionDetail, error) {
	query := request.URL.Query()
	if !query.Has("limit") && !query.Has("before") {
		return detailFromSession(root, store, value), nil
	}
	limit := 80
	if query.Has("limit") {
		var err error
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > 200 {
			return sessionDetail{}, fmt.Errorf("limit must be between 1 and 200 messages")
		}
	}
	end := len(value.Transcript)
	if query.Has("before") {
		before, err := strconv.Atoi(query.Get("before"))
		if err != nil || before < 0 {
			return sessionDetail{}, fmt.Errorf("before must be a non-negative message cursor")
		}
		end = min(before, end)
	}
	boundaries := []int{0}
	pending := make(map[string]bool)
	for index, message := range value.Transcript {
		if message.Role == client.RoleUser && index > 0 && len(pending) == 0 {
			boundaries = append(boundaries, index)
		}
		for _, call := range message.ToolCalls {
			pending[call.ID] = true
		}
		if message.Role == client.RoleTool {
			delete(pending, message.ToolCallID)
		}
	}
	// Only the newest page may end inside a still-running turn. An arbitrary
	// older cursor is rounded back to a complete turn boundary.
	if end < len(value.Transcript) {
		requested := end
		end = 0
		for _, boundary := range boundaries {
			if boundary <= requested {
				end = boundary
			}
		}
	}
	start := 0
	for _, boundary := range boundaries {
		if boundary <= max(0, end-limit) {
			start = boundary
		}
	}
	detail := detailFromSessionRange(root, store, value, start, end)
	detail.History = &sessionHistory{Start: start, End: end, Total: len(value.Transcript)}
	return detail, nil
}
