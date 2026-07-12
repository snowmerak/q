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

	stateMu  sync.Mutex
	writeMu  sync.Mutex
	pending  map[int64]chan rpcCallResult
	handlers map[string]ServerRequestHandler
	events   chan CodexEvent
	done     chan struct{}
	doneOnce sync.Once
	err      error

	eventMu     sync.Mutex
	eventCond   *sync.Cond
	eventQueue  []CodexEvent
	eventsEnded bool

	closeOnce sync.Once
	closeErr  error
}

type rpcCallResult struct {
	response rpcResponse
	err      error
}

type ServerRequestHandler func(context.Context, json.RawMessage) (any, error)

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

type rpcServerResponse struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result,omitempty"`
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
		cmd: cmd, stdin: stdin, pending: make(map[int64]chan rpcCallResult), handlers: make(map[string]ServerRequestHandler),
		events: make(chan CodexEvent, 128), done: make(chan struct{}),
	}
	c.eventCond = sync.NewCond(&c.eventMu)
	go c.eventLoop()
	go c.readLoop(stdout)
	return c, nil
}

func (c *CodexAppServer) Events() <-chan CodexEvent { return c.events }

func (c *CodexAppServer) Call(ctx context.Context, method string, params any, result any) error {
	c.stateMu.Lock()
	select {
	case <-c.done:
		err := c.err
		c.stateMu.Unlock()
		if err == nil {
			err = errors.New("codex app-server stopped")
		}
		return err
	default:
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcCallResult, 1)
	c.pending[id] = ch
	c.stateMu.Unlock()

	err := c.writeMessage(rpcRequest{ID: id, Method: method, Params: params})
	if err != nil {
		c.removePending(id)
		return err
	}

	select {
	case callResult := <-ch:
		if callResult.err != nil {
			return callResult.err
		}
		response := callResult.response
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
		c.removePending(id)
		c.stateMu.Lock()
		err := c.err
		c.stateMu.Unlock()
		if err == nil {
			err = errors.New("codex app-server stopped")
		}
		return err
	}
}

func (c *CodexAppServer) Notify(method string, params any) error {
	return c.writeMessage(rpcNotification{Method: method, Params: params})
}

func (c *CodexAppServer) RegisterServerRequestHandler(method string, handler ServerRequestHandler) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if handler == nil {
		delete(c.handlers, method)
	} else {
		c.handlers[method] = handler
	}
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
	return c.StartTurnWithSchema(ctx, threadID, prompt, nil)
}

func (c *CodexAppServer) StartTurnWithSchema(ctx context.Context, threadID, prompt string, outputSchema any) (string, error) {
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	params := map[string]any{"threadId": threadID, "input": []any{map[string]any{"type": "text", "text": prompt}}}
	if outputSchema != nil {
		params["outputSchema"] = outputSchema
	}
	if err := c.Call(ctx, "turn/start", params, &response); err != nil {
		return "", err
	}
	return response.Turn.ID, nil
}

func (c *CodexAppServer) Close() error {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		killed := false
		if c.cmd != nil && c.cmd.Process != nil {
			if err := c.cmd.Process.Kill(); err == nil {
				killed = true
			}
		}
		if c.cmd != nil {
			c.closeErr = c.cmd.Wait()
			if killed {
				c.closeErr = nil
			}
		}
	})
	return c.closeErr
}

func (c *CodexAppServer) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &envelope) != nil {
			continue
		}
		hasID := len(envelope.ID) > 0 && string(envelope.ID) != "null"
		if hasID && envelope.Method == "" {
			var response rpcResponse
			if json.Unmarshal(line, &response) == nil {
				c.stateMu.Lock()
				ch := c.pending[response.ID]
				delete(c.pending, response.ID)
				c.stateMu.Unlock()
				if ch != nil {
					ch <- rpcCallResult{response: response}
				}
			}
			continue
		}
		if envelope.Method != "" && hasID {
			go c.handleServerRequest(envelope.ID, envelope.Method, envelope.Params)
			continue
		}
		if envelope.Method != "" {
			c.enqueueEvent(CodexEvent{Method: envelope.Method, Params: envelope.Params})
		}
	}
	c.finish(scanner.Err())
}

func (c *CodexAppServer) removePending(id int64) {
	c.stateMu.Lock()
	delete(c.pending, id)
	c.stateMu.Unlock()
}

func (c *CodexAppServer) writeMessage(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return errors.New("codex app-server stopped")
	default:
	}
	n, err := c.stdin.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		err = fmt.Errorf("codex app-server write failed: %w", err)
		c.finish(err)
	}
	return err
}

func (c *CodexAppServer) finish(err error) {
	if err == nil {
		err = errors.New("codex app-server stopped")
	}
	c.doneOnce.Do(func() {
		c.stateMu.Lock()
		c.err = err
		waiters := make([]chan rpcCallResult, 0, len(c.pending))
		for id, ch := range c.pending {
			waiters = append(waiters, ch)
			delete(c.pending, id)
		}
		c.stateMu.Unlock()
		close(c.done)
		for _, ch := range waiters {
			ch <- rpcCallResult{err: err}
		}
		c.eventMu.Lock()
		c.eventsEnded = true
		c.eventCond.Broadcast()
		c.eventMu.Unlock()
	})
}

func (c *CodexAppServer) enqueueEvent(event CodexEvent) {
	c.eventMu.Lock()
	if !c.eventsEnded {
		c.eventQueue = append(c.eventQueue, event)
		c.eventCond.Signal()
	}
	c.eventMu.Unlock()
}

func (c *CodexAppServer) eventLoop() {
	for {
		c.eventMu.Lock()
		for len(c.eventQueue) == 0 && !c.eventsEnded {
			c.eventCond.Wait()
		}
		if len(c.eventQueue) == 0 && c.eventsEnded {
			c.eventMu.Unlock()
			close(c.events)
			return
		}
		event := c.eventQueue[0]
		c.eventQueue[0] = CodexEvent{}
		c.eventQueue = c.eventQueue[1:]
		c.eventMu.Unlock()
		c.events <- event
	}
}

func (c *CodexAppServer) handleServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	c.stateMu.Lock()
	handler := c.handlers[method]
	c.stateMu.Unlock()
	response := rpcServerResponse{ID: id}
	if handler == nil {
		response.Error = &rpcError{Code: -32601, Message: "unsupported server request: " + method}
	} else {
		result, err := handler(context.Background(), params)
		if err != nil {
			response.Error = &rpcError{Code: -32000, Message: err.Error()}
		} else {
			response.Result = result
		}
	}
	_ = c.writeMessage(response)
}
