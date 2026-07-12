package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type bufferWriteCloser struct {
	bytes.Buffer
	mu sync.Mutex
}

func (w *bufferWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Buffer.Write(p)
}
func (w *bufferWriteCloser) Close() error { return nil }
func (w *bufferWriteCloser) StringSafe() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Buffer.String()
}

type errorWriteCloser struct{ err error }

func (w errorWriteCloser) Write([]byte) (int, error) { return 0, w.err }
func (w errorWriteCloser) Close() error              { return nil }

func newTestCodex(stdin io.WriteCloser, eventBuffer int) *CodexAppServer {
	c := &CodexAppServer{stdin: stdin, pending: make(map[int64]chan rpcCallResult), handlers: make(map[string]ServerRequestHandler), events: make(chan CodexEvent, eventBuffer), done: make(chan struct{})}
	c.eventCond = sync.NewCond(&c.eventMu)
	go c.eventLoop()
	return c
}

func waitForPending(t *testing.T, c *CodexAppServer) int64 {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c.stateMu.Lock()
		for id := range c.pending {
			c.stateMu.Unlock()
			return id
		}
		c.stateMu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("pending call was not registered")
	return 0
}

func TestCodexRPCRequestShape(t *testing.T) {
	b, err := json.Marshal(rpcRequest{ID: 7, Method: "turn/start", Params: map[string]any{"threadId": "t"}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != float64(7) || got["method"] != "turn/start" {
		t.Fatalf("unexpected request: %s", b)
	}
}

func TestCodexCallRoutesResponseThroughReadLoop(t *testing.T) {
	stdin := &bufferWriteCloser{}
	c := newTestCodex(stdin, 4)
	resultCh := make(chan error, 1)
	var result struct {
		OK bool `json:"ok"`
	}
	go func() { resultCh <- c.Call(context.Background(), "test", nil, &result) }()
	id := waitForPending(t, c)
	c.readLoop(strings.NewReader(`{"id":` + strconv.FormatInt(id, 10) + `,"result":{"ok":true}}` + "\n"))
	if err := <-resultCh; err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatal("response was not decoded")
	}
}

func TestCodexShutdownReleasesPendingCalls(t *testing.T) {
	c := newTestCodex(&bufferWriteCloser{}, 1)
	result := make(chan error, 1)
	go func() { result <- c.Call(context.Background(), "never", nil, nil) }()
	waitForPending(t, c)
	want := errors.New("transport stopped")
	c.finish(want)
	select {
	case err := <-result:
		if !errors.Is(err, want) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending call hung after shutdown")
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if len(c.pending) != 0 {
		t.Fatalf("pending entries=%d", len(c.pending))
	}
}

func TestCodexSlowEventConsumerDoesNotBlockResponses(t *testing.T) {
	c := newTestCodex(&bufferWriteCloser{}, 1)
	responseCh := make(chan rpcCallResult, 1)
	c.pending[1] = responseCh
	var input strings.Builder
	for i := 0; i < 300; i++ {
		input.WriteString(`{"method":"item/agentMessage/delta","params":{"delta":"x"}}` + "\n")
	}
	input.WriteString(`{"id":1,"result":{}}` + "\n")
	readDone := make(chan struct{})
	go func() { c.readLoop(strings.NewReader(input.String())); close(readDone) }()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("readLoop blocked on slow event consumer")
	}
	select {
	case <-responseCh:
	case <-time.After(time.Second):
		t.Fatal("response was not routed")
	}
	count := 0
	for range c.Events() {
		count++
	}
	if count != 300 {
		t.Fatalf("events=%d", count)
	}
}

func TestCodexWriteFailureRemovesPending(t *testing.T) {
	want := io.ErrClosedPipe
	c := newTestCodex(errorWriteCloser{err: want}, 1)
	if err := c.Call(context.Background(), "test", nil, nil); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	c.stateMu.Lock()
	pending := len(c.pending)
	c.stateMu.Unlock()
	if pending != 0 {
		t.Fatalf("pending entries=%d", pending)
	}
	c.finish(nil)
}

func TestCodexCancellationRemovesPendingBeforeLateResponse(t *testing.T) {
	c := newTestCodex(&bufferWriteCloser{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- c.Call(ctx, "test", nil, nil) }()
	id := waitForPending(t, c)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	c.readLoop(strings.NewReader(`{"id":` + strconv.FormatInt(id, 10) + `,"result":{}}` + "\n"))
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if len(c.pending) != 0 {
		t.Fatalf("pending entries=%d", len(c.pending))
	}
}

func TestCodexUnsupportedServerRequestGetsErrorResponse(t *testing.T) {
	stdin := &bufferWriteCloser{}
	c := newTestCodex(stdin, 1)
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() { c.readLoop(reader); close(done) }()
	_, _ = io.WriteString(writer, `{"id":"server-1","method":"permission/request","params":{}}`+"\n")
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(stdin.StringSafe(), `"code":-32601`) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := stdin.StringSafe(); !strings.Contains(got, `"id":"server-1"`) || !strings.Contains(got, `"code":-32601`) {
		t.Fatalf("response=%s", got)
	}
	_ = writer.Close()
	<-done
}

func TestCodexRegisteredServerRequestHandler(t *testing.T) {
	stdin := &bufferWriteCloser{}
	c := newTestCodex(stdin, 1)
	c.RegisterServerRequestHandler("echo", func(_ context.Context, params json.RawMessage) (any, error) { return map[string]any{"ok": true}, nil })
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() { c.readLoop(reader); close(done) }()
	_, _ = io.WriteString(writer, `{"id":9,"method":"echo","params":{}}`+"\n")
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(stdin.StringSafe(), `"ok":true`) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := stdin.StringSafe(); !strings.Contains(got, `"id":9`) || !strings.Contains(got, `"ok":true`) {
		t.Fatalf("response=%s", got)
	}
	_ = writer.Close()
	<-done
}

func TestCodexCloseIsIdempotent(t *testing.T) {
	if os.Getenv("Q_CODEX_HELPER_PROCESS") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestCodexCloseIsIdempotent")
	cmd.Env = append(os.Environ(), "Q_CODEX_HELPER_PROCESS=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := newTestCodex(stdin, 1)
	c.cmd = cmd
	go c.readLoop(stdout)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
