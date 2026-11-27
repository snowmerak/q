package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/snowmerak/q/diagnostics"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

// The pinned SDK predates ACP session compaction. Keep normal JSON-RPC on the
// SDK and adapt only initialization and compaction notifications for workers.
const acpCompactionMeta = "dev.snowmerak.q/compaction"

type acpCompactionWriter struct{ io.Writer }

func (w acpCompactionWriter) Write(frame []byte) (int, error) {
	if !bytes.Contains(frame, []byte(`"initialize"`)) {
		return w.Writer.Write(frame)
	}
	// The SDK writes one newline-delimited JSON-RPC frame per Write.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(frame, &envelope); err != nil || !bytes.Equal(envelope["method"], []byte(`"initialize"`)) {
		return w.Writer.Write(frame)
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(envelope["params"], &params); err != nil {
		return 0, err
	}
	capabilities := make(map[string]json.RawMessage)
	if raw := params["clientCapabilities"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &capabilities); err != nil {
			return 0, err
		}
	}
	session := make(map[string]json.RawMessage)
	if raw := capabilities["session"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &session); err != nil {
			return 0, err
		}
	}
	session["compaction"] = json.RawMessage(`{}`)
	capabilities["session"], _ = json.Marshal(session)
	params["clientCapabilities"], _ = json.Marshal(capabilities)
	envelope["params"], _ = json.Marshal(params)
	data, _ := json.Marshal(envelope)
	data = append(data, '\n')
	n, err := w.Writer.Write(data)
	if err != nil {
		return 0, err
	}
	if n != len(data) {
		return 0, io.ErrShortWrite
	}
	return len(frame), nil
}

type acpCompactionReader struct {
	scanner *bufio.Scanner
	pending []byte
	report  func(acp.SessionId, string, string)
}

func newACPCompactionReader(source io.Reader, report func(acp.SessionId, string, string)) *acpCompactionReader {
	scanner := bufio.NewScanner(source)
	// Match the SDK's existing bound for a single incoming frame.
	scanner.Buffer(make([]byte, 0, 4096), 10<<20)
	return &acpCompactionReader{scanner: scanner, report: report}
}

func (r *acpCompactionReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 {
		if !r.scanner.Scan() {
			if err := r.scanner.Err(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		frame := r.scanner.Bytes()
		if r.compactionNotification(frame) {
			continue
		}
		r.pending = append(r.pending[:0], frame...)
		r.pending = append(r.pending, '\n')
	}
	n := copy(buffer, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (r *acpCompactionReader) compactionNotification(frame []byte) bool {
	if !bytes.Contains(frame, []byte(`"compaction_update"`)) && !bytes.Contains(frame, []byte(`"compaction_summary_chunk"`)) {
		return false
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			SessionID acp.SessionId `json:"sessionId"`
			Update    struct {
				Type   string `json:"sessionUpdate"`
				ID     string `json:"compactionId"`
				Status string `json:"status"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(frame, &envelope) != nil || envelope.JSONRPC != "2.0" || len(envelope.ID) != 0 || envelope.Method != "session/update" {
		return false
	}
	switch update := envelope.Params.Update; update.Type {
	case "compaction_update":
		if envelope.Params.SessionID != "" && update.ID != "" {
			r.report(envelope.Params.SessionID, update.ID, update.Status)
		}
		return true
	case "compaction_summary_chunk":
		return true
	default:
		return false
	}
}

func (r *acpRemoteClient) observeCompaction(session acp.SessionId, id, status string) {
	key := acpCompactionKey{session, id}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.compactions == nil {
		r.compactions = make(map[acpCompactionKey]struct{})
	}
	_, seen := r.compactions[key]
	turn := r.turn
	active := turn != nil && turn.compacting && session == r.sessionID
	if !seen && len(r.compactions) < 4096 && len(id) <= 256 && len(session) <= 256 {
		r.compactions[key] = struct{}{}
		if active {
			turn.compactionIDs[id] = true
		}
	}
	if active && turn.compactionIDs[id] {
		switch status {
		case "completed", "failed", "cancelled":
			turn.compactionStatus = status
		}
	}
}

type acpCompactionKey struct {
	session acp.SessionId
	id      string
}

func (r *acpRemoteClient) compactTicketContext(ctx context.Context) (status string, err error) {
	r.mu.Lock()
	if r.turn != nil {
		r.mu.Unlock()
		return "", errors.New("another ACP turn is already active")
	}
	id := r.sessionID
	if !slices.ContainsFunc(r.slashCommandsBySession[id], func(command slashCommand) bool { return command.name == "/compact" }) {
		r.mu.Unlock()
		return "", errors.New("the ACP session does not advertise /compact")
	}
	turn := &acpRemoteTurn{ctx: ctx, compacting: true, compactionIDs: make(map[string]bool),
		toolTitles: make(map[acp.ToolCallId]string), toolCalls: make(map[acp.ToolCallId]struct{})}
	r.turn = turn
	r.mu.Unlock()
	start := time.Now()
	defer func() {
		r.mu.Lock()
		r.turn = nil
		r.mu.Unlock()
		diagnostics.Logger(ctx).InfoContext(ctx, "ticket.acp_compaction.finished", "duration_ms", time.Since(start).Milliseconds(), "status", status, "failed", err != nil)
	}()
	compactCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	response, err := r.connection.Prompt(compactCtx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("/compact")}})
	if err != nil {
		return "", fmt.Errorf("ACP /compact: %w", errors.Join(err, compactCtx.Err()))
	}
	if err := compactCtx.Err(); err != nil {
		return "", err
	}
	if response.StopReason != acp.StopReasonEndTurn {
		return "", fmt.Errorf("ACP /compact stopped with %s", response.StopReason)
	}
	r.mu.Lock()
	progress := turn.compactionStatus
	r.mu.Unlock()
	if _, toolCalls := turn.result(); toolCalls != 0 {
		return "", errors.New("ACP /compact attempted a tool call")
	}
	switch progress {
	case "completed":
		return progress, nil
	case "failed", "cancelled":
		return "", fmt.Errorf("ACP compaction %s", progress)
	}
	// Q's own ACP server returns a receipt after saving its checkpoint,
	// including when there is no history eligible for compaction.
	if receipt, _ := response.Meta[acpCompactionMeta].(string); receipt == "completed" || receipt == "not_needed" {
		return receipt, nil
	}
	return "", errors.New("the ACP agent returned no confirmed compaction result")
}
