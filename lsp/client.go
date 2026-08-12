// Package lsp implements an LSP client over a language server's standard
// input and output streams.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	ErrClosed         = errors.New("lsp: client is closed")
	ErrNotInitialized = errors.New("lsp: client is not initialized")
	ErrInitialized    = errors.New("lsp: client is already initialized")
)

type Config struct {
	Command             string
	Args                []string
	Directory           string
	Environment         []string
	Stderr              io.Writer
	Handler             Handler
	MaximumMessageBytes int
}

type response struct {
	result json.RawMessage
	err    error
}

type processState struct {
	command *exec.Cmd
	done    chan struct{}
	mu      sync.Mutex
	err     error
}

func (p *processState) wait() error {
	if p == nil {
		return nil
	}
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

type clientPhase uint8

const (
	phaseUninitialized clientPhase = iota
	phaseInitializing
	phaseInitialized
	phaseShuttingDown
)

type Client struct {
	ctx       context.Context
	cancel    context.CancelFunc
	input     io.ReadCloser
	output    io.WriteCloser
	process   *processState
	handler   Handler
	maximum   int
	nextID    atomic.Int64
	writeMu   sync.Mutex
	mu        sync.Mutex
	pending   map[string]chan response
	phase     clientPhase
	terminal  error
	done      chan struct{}
	closeOnce sync.Once
}

// Start launches a language server and connects a JSON-RPC client to its
// stdin/stdout. The supplied context owns the whole server lifetime.
func Start(ctx context.Context, config Config) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("lsp: context is nil")
	}
	commandName := strings.TrimSpace(config.Command)
	if commandName == "" {
		return nil, errors.New("lsp: command is required")
	}
	command := exec.Command(commandName, config.Args...)
	command.Dir = config.Directory
	if config.Environment != nil {
		command.Env = append([]string(nil), config.Environment...)
	}
	if config.Stderr != nil {
		command.Stderr = config.Stderr
	} else {
		command.Stderr = io.Discard
	}
	configureChildProcess(command)
	input, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: open server stdout: %w", err)
	}
	output, err := command.StdinPipe()
	if err != nil {
		_ = input.Close()
		return nil, fmt.Errorf("lsp: open server stdin: %w", err)
	}
	if err := command.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, fmt.Errorf("lsp: start server: %w", err)
	}
	process := &processState{command: command, done: make(chan struct{})}
	go func() {
		err := command.Wait()
		process.mu.Lock()
		process.err = err
		process.mu.Unlock()
		close(process.done)
	}()
	return newClient(ctx, input, output, process, config.Handler, config.MaximumMessageBytes), nil
}

func newClient(
	ctx context.Context,
	input io.ReadCloser,
	output io.WriteCloser,
	process *processState,
	handler Handler,
	maximum int,
) *Client {
	clientCtx, cancel := context.WithCancel(ctx)
	if handler == nil {
		handler = HandlerFuncs{}
	}
	if maximum <= 0 {
		maximum = defaultMaximumMessageBytes
	}
	c := &Client{
		ctx: clientCtx, cancel: cancel, input: input, output: output,
		process: process, handler: handler, maximum: maximum,
		pending: make(map[string]chan response), done: make(chan struct{}),
	}
	go c.readLoop()
	go func() {
		select {
		case <-clientCtx.Done():
			c.fail(clientCtx.Err())
		case <-c.done:
		}
	}()
	if process != nil {
		go func() {
			select {
			case <-process.done:
				err := process.wait()
				c.mu.Lock()
				shuttingDown := c.phase == phaseShuttingDown
				c.mu.Unlock()
				if shuttingDown && err == nil {
					c.fail(ErrClosed)
					return
				}
				if err == nil {
					err = io.EOF
				}
				c.fail(fmt.Errorf("lsp: server exited: %w", err))
			case <-c.done:
			}
		}()
	}
	return c
}

// Request sends one JSON-RPC request and decodes its result into result. A nil
// result discards a successful response. Context cancellation also sends the
// LSP $/cancelRequest notification on a best-effort basis.
func (c *Client) Request(ctx context.Context, method string, params, result any) error {
	if ctx == nil {
		return errors.New("lsp: request context is nil")
	}
	method = strings.TrimSpace(method)
	if method == "" {
		return errors.New("lsp: request method is required")
	}
	paramsBody, err := marshalOptional(params)
	if err != nil {
		return fmt.Errorf("lsp: encode %s params: %w", method, err)
	}
	id := c.nextID.Add(1)
	idBody := json.RawMessage(fmt.Sprintf("%d", id))
	key := string(idBody)
	responseChannel := make(chan response, 1)
	c.mu.Lock()
	if c.terminal != nil {
		err := c.terminal
		c.mu.Unlock()
		return err
	}
	c.pending[key] = responseChannel
	c.mu.Unlock()

	message := wireMessage{JSONRPC: "2.0", ID: idBody, Method: method, Params: paramsBody}
	if err := c.write(message); err != nil {
		c.removePending(key)
		c.fail(err)
		return err
	}
	select {
	case received := <-responseChannel:
		if received.err != nil {
			return received.err
		}
		if result == nil || len(received.result) == 0 || string(received.result) == "null" {
			return nil
		}
		if err := json.Unmarshal(received.result, result); err != nil {
			return fmt.Errorf("lsp: decode %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		if c.removePending(key) {
			go func() {
				_ = c.Notify(c.ctx, "$/cancelRequest", map[string]any{"id": id})
			}()
		}
		return ctx.Err()
	case <-c.done:
		c.mu.Lock()
		err := c.terminal
		c.mu.Unlock()
		if err == nil {
			err = ErrClosed
		}
		return err
	}
}

func (c *Client) Notify(ctx context.Context, method string, params any) error {
	if ctx == nil {
		return errors.New("lsp: notification context is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	method = strings.TrimSpace(method)
	if method == "" {
		return errors.New("lsp: notification method is required")
	}
	paramsBody, err := marshalOptional(params)
	if err != nil {
		return fmt.Errorf("lsp: encode %s params: %w", method, err)
	}
	return c.write(wireMessage{JSONRPC: "2.0", Method: method, Params: paramsBody})
}

// Initialize performs the LSP initialize handshake, including the initialized
// notification. It may be called successfully only once.
func (c *Client) Initialize(ctx context.Context, params InitializeParams) (InitializeResult, error) {
	c.mu.Lock()
	if c.terminal != nil {
		err := c.terminal
		c.mu.Unlock()
		return InitializeResult{}, err
	}
	if c.phase != phaseUninitialized {
		c.mu.Unlock()
		return InitializeResult{}, ErrInitialized
	}
	c.phase = phaseInitializing
	c.mu.Unlock()
	if params.Capabilities == nil {
		params.Capabilities = map[string]any{}
	}
	var result InitializeResult
	if err := c.Request(ctx, "initialize", params, &result); err != nil {
		c.resetInitializing()
		return InitializeResult{}, err
	}
	if err := c.Notify(ctx, "initialized", map[string]any{}); err != nil {
		c.resetInitializing()
		return InitializeResult{}, err
	}
	c.mu.Lock()
	c.phase = phaseInitialized
	c.mu.Unlock()
	return result, nil
}

// Shutdown performs the graceful LSP shutdown/exit sequence and then closes
// the process transport. Close can be used directly for forced cleanup.
func (c *Client) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	if c.terminal != nil {
		err := c.terminal
		c.mu.Unlock()
		if errors.Is(err, ErrClosed) {
			return nil
		}
		return err
	}
	if c.phase != phaseInitialized {
		c.mu.Unlock()
		return ErrNotInitialized
	}
	c.phase = phaseShuttingDown
	c.mu.Unlock()
	requestErr := c.Request(ctx, "shutdown", nil, nil)
	notifyErr := c.Notify(ctx, "exit", nil)
	var waitErr error
	if c.process != nil && notifyErr == nil {
		select {
		case <-c.process.done:
			waitErr = c.process.wait()
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
	}
	closeErr := c.Close()
	return errors.Join(requestErr, notifyErr, waitErr, closeErr)
}

// Close terminates the transport and language server without requiring a
// successful initialize handshake. It is safe to call more than once.
func (c *Client) Close() error {
	c.fail(ErrClosed)
	return nil
}

func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) readLoop() {
	reader := bufio.NewReader(c.input)
	for {
		body, err := readFrame(reader, c.maximum)
		if err != nil {
			c.mu.Lock()
			shuttingDown := c.phase == phaseShuttingDown
			c.mu.Unlock()
			if shuttingDown && errors.Is(err, io.EOF) {
				return
			}
			c.fail(err)
			return
		}
		var message wireMessage
		if err := json.Unmarshal(body, &message); err != nil {
			c.fail(fmt.Errorf("lsp: decode JSON-RPC message: %w", err))
			return
		}
		if message.JSONRPC != "2.0" {
			c.fail(errors.New("lsp: received message with invalid jsonrpc version"))
			return
		}
		if message.Method != "" {
			c.dispatchServerCall(message)
			continue
		}
		if len(message.ID) == 0 {
			c.fail(errors.New("lsp: received response without an id"))
			return
		}
		c.dispatchResponse(message)
	}
}

func (c *Client) dispatchResponse(message wireMessage) {
	key := string(message.ID)
	c.mu.Lock()
	channel, found := c.pending[key]
	if found {
		delete(c.pending, key)
	}
	c.mu.Unlock()
	if !found {
		return
	}
	if message.Error != nil {
		channel <- response{err: message.Error}
		return
	}
	channel <- response{result: append(json.RawMessage(nil), message.Result...)}
}

func (c *Client) dispatchServerCall(message wireMessage) {
	params := append(json.RawMessage(nil), message.Params...)
	if len(message.ID) == 0 || string(message.ID) == "null" {
		go c.handler.Notification(c.ctx, message.Method, params)
		return
	}
	id := append(json.RawMessage(nil), message.ID...)
	go func() {
		result, err := c.handler.Request(c.ctx, message.Method, params)
		responseMessage := wireMessage{JSONRPC: "2.0", ID: id}
		if err != nil {
			var responseErr *ResponseError
			if !errors.As(err, &responseErr) {
				responseErr = &ResponseError{Code: InternalErrorCode, Message: err.Error()}
			}
			responseMessage.Error = responseErr
		} else {
			body, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				responseMessage.Error = &ResponseError{Code: InternalErrorCode, Message: marshalErr.Error()}
			} else {
				responseMessage.Result = body
			}
		}
		if err := c.write(responseMessage); err != nil {
			c.fail(err)
		}
	}()
}

func (c *Client) write(message wireMessage) error {
	c.mu.Lock()
	terminal := c.terminal
	c.mu.Unlock()
	if terminal != nil {
		return terminal
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := writeFrame(c.output, message, c.maximum); err != nil {
		return err
	}
	return nil
}

func (c *Client) removePending(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, found := c.pending[key]; !found {
		return false
	}
	delete(c.pending, key)
	return true
}

func (c *Client) resetInitializing() {
	c.mu.Lock()
	if c.phase == phaseInitializing && c.terminal == nil {
		c.phase = phaseUninitialized
	}
	c.mu.Unlock()
}

func (c *Client) fail(err error) {
	if err == nil {
		err = ErrClosed
	}
	c.mu.Lock()
	if c.terminal != nil {
		c.mu.Unlock()
		return
	}
	c.terminal = err
	pending := c.pending
	c.pending = make(map[string]chan response)
	close(c.done)
	c.mu.Unlock()
	for _, channel := range pending {
		channel <- response{err: err}
	}
	c.closeTransport()
}

func (c *Client) closeTransport() {
	c.closeOnce.Do(func() {
		c.cancel()
		_ = c.output.Close()
		_ = c.input.Close()
		if c.process == nil {
			return
		}
		select {
		case <-c.process.done:
		default:
			if c.process.command.Process != nil {
				_ = c.process.command.Process.Kill()
			}
		}
		_ = c.process.wait()
	})
}

func marshalOptional(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	body, err := json.Marshal(value)
	return body, err
}
