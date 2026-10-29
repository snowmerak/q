package library

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/google/uuid"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/internal/fsopen"
	"github.com/snowmerak/q/workitems"
)

type WorkerMessage struct {
	Index      int          `json:"index"`
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	Name       string       `json:"name,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	ToolCalls  []WorkerCall `json:"tool_calls,omitempty"`
	Truncated  bool         `json:"truncated,omitempty"`
}
type WorkerCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Truncated bool   `json:"truncated,omitempty"`
}
type WorkerPage struct {
	WorkspaceID  string           `json:"workspace_id"`
	SessionID    string           `json:"session_id"`
	RunID        string           `json:"run_id"`
	Messages     []WorkerMessage  `json:"messages"`
	NextCursor   string           `json:"next_cursor,omitempty"`
	RunningCall  *WorkerToolCall  `json:"running_call,omitempty"`
	UnknownTools []WorkerToolCall `json:"unknown_tools,omitempty"`
}

type WorkerToolCall struct {
	MessageIndex int    `json:"message_index"`
	ToolIndex    int    `json:"tool_index"`
	CallID       string `json:"call_id"`
	Name         string `json:"name"`
}

// Read only the public transcript projection. Provider context/replay data and
// hidden reasoning are intentionally excluded, and Library never owns a worker
// session writer or imports the higher-level execution packages.
func readWorkerJSON(path string, output any) error {
	file, err := fsopen.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 64<<20 {
		return errors.New("worker projection is too large")
	}
	return json.NewDecoder(io.LimitReader(file, 64<<20)).Decode(output)
}

func (c *Client) ReadWorker(ctx context.Context, id, cursor string, limit int) (WorkerPage, error) {
	query := url.Values{"cursor": {cursor}, "limit": {strconv.Itoa(limit)}}
	var out WorkerPage
	err := c.doJSON(ctx, http.MethodGet, "/attempts/"+url.PathEscape(id)+"/worker?"+query.Encode(), nil, &out)
	return out, err
}

func registerWorkerRoute(mux *http.ServeMux, s *workitems.Store, directory string) {
	mux.HandleFunc("GET /v1/attempts/{attempt}/worker", func(w http.ResponseWriter, r *http.Request) {
		a, err := s.GetAttempt(r.Context(), r.PathValue("attempt"))
		if err != nil {
			workspaceError(w, err)
			return
		}
		if uuid.Validate(a.WorkspaceID) != nil || uuid.Validate(a.WorkerSessionID) != nil {
			workspaceError(w, workitems.ErrInvalidInput)
			return
		}
		path := filepath.Join(directory, "workspaces", a.WorkspaceID, "workers", ".q", "sessions", a.WorkerSessionID)
		var session struct {
			ID         string           `json:"id"`
			RunID      string           `json:"run_id"`
			Transcript []client.Message `json:"transcript"`
		}
		if err := readWorkerJSON(filepath.Join(path, "session.json"), &session); err != nil {
			writeLibraryError(w, 404, err)
			return
		}
		if session.ID != a.WorkerSessionID {
			workspaceError(w, workitems.ErrConflict)
			return
		}
		type cursor struct {
			SessionID    string `json:"s"`
			Upper, After int
			Digest       string `json:"d"`
		}
		position := cursor{SessionID: session.ID, Upper: len(session.Transcript)}
		token := r.URL.Query().Get("cursor")
		if token != "" {
			raw, err := base64.RawURLEncoding.DecodeString(token)
			if len(token) > 2048 || err != nil || json.Unmarshal(raw, &position) != nil || position.SessionID != session.ID || position.After < 0 || position.Upper < position.After || position.Upper > len(session.Transcript) {
				workspaceError(w, workitems.ErrConflict)
				return
			}
		}
		raw, _ := json.Marshal(session.Transcript[:position.Upper])
		sum := sha256.Sum256(raw)
		digest := hex.EncodeToString(sum[:])
		if position.Digest != "" && position.Digest != digest {
			workspaceError(w, workitems.ErrConflict)
			return
		}
		position.Digest = digest
		limit, err := queryLimit(r)
		if err != nil {
			workspaceError(w, err)
			return
		}
		if limit <= 0 {
			limit = 10
		}
		limit = min(limit, 20)
		end := min(position.Upper, position.After+limit)
		out := WorkerPage{WorkspaceID: a.WorkspaceID, SessionID: session.ID, RunID: session.RunID, Messages: []WorkerMessage{}}
		for index := position.After; index < end; index++ {
			message := session.Transcript[index]
			content, truncated := workerExcerpt(message.TextContent(), 2048)
			view := WorkerMessage{Index: index, Role: string(message.Role), Content: content, Name: message.Name, ToolCallID: message.ToolCallID, Truncated: truncated}
			for _, call := range message.ToolCalls {
				args, truncated := workerExcerpt(call.Function.Arguments, 2048)
				view.ToolCalls = append(view.ToolCalls, WorkerCall{ID: call.ID, Name: call.Function.Name, Arguments: args, Truncated: truncated})
			}
			out.Messages = append(out.Messages, view)
		}
		var state struct {
			RunningCall  *WorkerToolCall  `json:"running_call"`
			UnknownTools []WorkerToolCall `json:"unknown_tools"`
		}
		if err := readWorkerJSON(filepath.Join(path, "delegation-state.json"), &state); err == nil {
			out.RunningCall = state.RunningCall
			out.UnknownTools = state.UnknownTools
		} else if !errors.Is(err, os.ErrNotExist) {
			workspaceError(w, err)
			return
		}
		if end < position.Upper {
			position.After = end
			raw, _ := json.Marshal(position)
			out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		}
		writeJSON(w, 200, out)
	})
}
func workerExcerpt(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]), true
	}
	return value, false
}
