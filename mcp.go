package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type JsonRpcRequest struct {
	JsonRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type JsonRpcNotification struct {
	JsonRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type JsonRpcResponse struct {
	JsonRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JsonRpcError   `json:"error,omitempty"`
}

type JsonRpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type MCPToolListResult struct {
	Tools []MCPTool `json:"tools"`
}

type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type MCPCallToolParams struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

type MCPCallToolResult struct {
	Content []MCPContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type MCPContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type MCPClient struct {
	name      string
	config    MCPServerConfig
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	pending   map[int64]chan *JsonRpcResponse
	pendingMu sync.Mutex
	nextID    int64
	ctx       context.Context
	cancel    context.CancelFunc
	waitGroup sync.WaitGroup
	IsRunning bool
}

func NewMCPClient(name string, config MCPServerConfig) *MCPClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &MCPClient{
		name:    name,
		config:  config,
		pending: make(map[int64]chan *JsonRpcResponse),
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (c *MCPClient) Start() error {
	c.cmd = exec.Command(c.config.Command, c.config.Args...)

	// Set environment variables
	env := os.Environ()
	for k, v := range c.config.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	c.cmd.Env = env

	var err error
	c.stdin, err = c.cmd.StdinPipe()
	if err != nil {
		return err
	}
	c.stdout, err = c.cmd.StdoutPipe()
	if err != nil {
		_ = c.stdin.Close()
		return err
	}
	c.stderr, err = c.cmd.StderrPipe()
	if err != nil {
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		return err
	}

	if err := c.cmd.Start(); err != nil {
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		_ = c.stderr.Close()
		return err
	}

	c.IsRunning = true

	// Start goroutine to read stdout
	c.waitGroup.Add(1)
	go c.readLoop()

	// Start goroutine to read stderr
	c.waitGroup.Add(1)
	go c.logLoop()

	// Perform initialize handshake
	if err := c.handshake(); err != nil {
		_ = c.Stop()
		return fmt.Errorf("handshake failed: %w", err)
	}

	return nil
}

func (c *MCPClient) readLoop() {
	defer c.waitGroup.Done()
	scanner := bufio.NewScanner(c.stdout)
	for scanner.Scan() {
		line := scanner.Text()
		var resp JsonRpcResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue
		}

		if resp.ID != nil {
			id := *resp.ID
			c.pendingMu.Lock()
			ch, ok := c.pending[id]
			if ok {
				select {
				case ch <- &resp:
				default:
				}
			}
			c.pendingMu.Unlock()
		}
	}
}

func (c *MCPClient) logLoop() {
	defer c.waitGroup.Done()
	scanner := bufio.NewScanner(c.stderr)
	for scanner.Scan() {
		// Stderr log reading. We discard to keep clean stdout/stderr interface.
	}
}

func (c *MCPClient) sendRequest(method string, params any) (*JsonRpcResponse, error) {
	id := atomic.AddInt64(&c.nextID, 1)
	req := JsonRpcRequest{
		JsonRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	ch := make(chan *JsonRpcResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	// Write request to server stdin
	_, err = c.stdin.Write(append(data, '\n'))
	if err != nil {
		return nil, err
	}

	// Wait for response with timeout
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("JSON-RPC error (%d): %s", resp.Error.Code, resp.Error.Message)
		}
		return resp, nil
	case <-c.ctx.Done():
		return nil, fmt.Errorf("client stopped")
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("request timed out after 30s")
	}
}

func (c *MCPClient) sendNotification(method string, params any) error {
	req := JsonRpcNotification{
		JsonRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return err
	}

	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

func (c *MCPClient) handshake() error {
	initParams := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "q-copilot",
			"version": "1.0.0",
		},
	}

	_, err := c.sendRequest("initialize", initParams)
	if err != nil {
		return err
	}

	// Send initialized notification
	err = c.sendNotification("notifications/initialized", map[string]any{})
	return err
}

func (c *MCPClient) ListTools() ([]MCPTool, error) {
	resp, err := c.sendRequest("tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}

	var result MCPToolListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, err
	}

	return result.Tools, nil
}

func (c *MCPClient) CallTool(name string, arguments any) (string, error) {
	params := MCPCallToolParams{
		Name:      name,
		Arguments: arguments,
	}

	resp, err := c.sendRequest("tools/call", params)
	if err != nil {
		return "", err
	}

	var result MCPCallToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", err
	}

	if len(result.Content) == 0 {
		return "", fmt.Errorf("empty content returned from tool %s", name)
	}

	var sb strings.Builder
	for _, item := range result.Content {
		if item.Type == "text" {
			sb.WriteString(item.Text)
		}
	}

	out := sb.String()
	if result.IsError {
		return out, fmt.Errorf("tool returned error: %s", out)
	}

	return out, nil
}

func (c *MCPClient) Stop() error {
	c.cancel()
	c.IsRunning = false

	if c.stdin != nil {
		_ = c.stdin.Close()
	}

	// Kill command if running
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}

	c.waitGroup.Wait()
	return nil
}
