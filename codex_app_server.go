package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// CodexAppServer is a small JSON-RPC client for `codex app-server --stdio`.
// It deliberately keeps protocol payloads as json.RawMessage: the app-server
// protocol is experimental and Codex can generate authoritative schemas for
// the installed CLI version.
type CodexAppServer struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	nextID int64

	mu      sync.Mutex
	pending map[int64]chan rpcResponse
	events  chan CodexEvent
	done    chan struct{}
	err     error
}

type rpcRequest struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type rpcNotification struct {
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type CodexEvent struct {
	Method string
	Params json.RawMessage
}

func StartCodexAppServer(ctx context.Context, codexPath string, stderr io.Writer) (*CodexAppServer, error) {
	if codexPath == "" {
		codexPath = "codex"
	}
	cmd := exec.CommandContext(ctx, codexPath, "app-server", "--stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}

	c := &CodexAppServer{
		cmd: cmd, stdin: stdin, pending: make(map[int64]chan rpcResponse),
		events: make(chan CodexEvent, 128), done: make(chan struct{}),
	}
	go c.readLoop(stdout)
	return c, nil
}

func (c *CodexAppServer) Events() <-chan CodexEvent { return c.events }

func (c *CodexAppServer) Call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	data, err := json.Marshal(rpcRequest{ID: id, Method: method, Params: params})
	if err == nil {
		_, err = c.stdin.Write(append(data, '\n'))
	}
	if err != nil {
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	c.mu.Unlock()

	select {
	case response := <-ch:
		if response.Error != nil {
			return fmt.Errorf("codex %s: rpc error %d: %s", method, response.Error.Code, response.Error.Message)
		}
		if result == nil || len(response.Result) == 0 {
			return nil
		}
		return json.Unmarshal(response.Result, result)
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-c.done:
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = errors.New("codex app-server stopped")
		}
		return err
	}
}

func (c *CodexAppServer) Notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := json.Marshal(rpcNotification{Method: method, Params: params})
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

func (c *CodexAppServer) Initialize(ctx context.Context) error {
	params := map[string]any{"clientInfo": map[string]any{"name": "q", "title": "q", "version": "0.1.0"}}
	if err := c.Call(ctx, "initialize", params, nil); err != nil {
		return err
	}
	return c.Notify("initialized", map[string]any{})
}

func (c *CodexAppServer) StartThread(ctx context.Context, cwd string) (string, error) {
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	params := map[string]any{
		"cwd": cwd, "approvalPolicy": "never", "sandbox": "workspace-write",
	}
	if err := c.Call(ctx, "thread/start", params, &response); err != nil {
		return "", err
	}
	if response.Thread.ID == "" {
		return "", errors.New("codex thread/start returned no thread id")
	}
	return response.Thread.ID, nil
}

func (c *CodexAppServer) ResumeThread(ctx context.Context, threadID, cwd string) error {
	params := map[string]any{
		"threadId": threadID, "cwd": cwd, "approvalPolicy": "never", "sandbox": "workspace-write",
	}
	return c.Call(ctx, "thread/resume", params, nil)
}

func (c *CodexAppServer) StartTurn(ctx context.Context, threadID, prompt string) (string, error) {
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	params := map[string]any{"threadId": threadID, "input": []any{map[string]any{"type": "text", "text": prompt}}}
	if err := c.Call(ctx, "turn/start", params, &response); err != nil {
		return "", err
	}
	return response.Turn.ID, nil
}

func (c *CodexAppServer) Close() error {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return c.cmd.Wait()
}

func (c *CodexAppServer) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var envelope struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &envelope) != nil {
			continue
		}
		if envelope.ID != nil && envelope.Method == "" {
			var response rpcResponse
			if json.Unmarshal(line, &response) == nil {
				c.mu.Lock()
				ch := c.pending[response.ID]
				delete(c.pending, response.ID)
				c.mu.Unlock()
				if ch != nil {
					ch <- response
				}
			}
			continue
		}
		if envelope.Method != "" && envelope.ID == nil {
			c.events <- CodexEvent{Method: envelope.Method, Params: envelope.Params}
		}
	}
	c.mu.Lock()
	c.err = scanner.Err()
	c.mu.Unlock()
	close(c.done)
	close(c.events)
}

func (c *CodexAppServer) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}
