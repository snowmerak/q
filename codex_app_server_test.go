package main

import (
	"context"
	"encoding/json"
	"io"
	"testing"
)

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

func TestCodexCallRoutesResponse(t *testing.T) {
	reader, writer := io.Pipe()
	c := &CodexAppServer{stdin: writer, pending: make(map[int64]chan rpcResponse), events: make(chan CodexEvent, 1), done: make(chan struct{})}
	go c.readLoop(reader)
	go func() {
		c.mu.Lock()
		for c.nextID == 0 {
			c.mu.Unlock()
			c.mu.Lock()
		}
		id := c.nextID
		ch := c.pending[id]
		c.mu.Unlock()
		ch <- rpcResponse{ID: id, Result: json.RawMessage(`{"ok":true}`)}
	}()
	var result struct {
		OK bool `json:"ok"`
	}
	if err := c.Call(context.Background(), "test", nil, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatal("response was not decoded")
	}
	_ = writer.Close()
}
