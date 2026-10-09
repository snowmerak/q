package toolbatch

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
)

func testCall(id, name string) client.ToolCall {
	return client.ToolCall{ID: id, Function: client.FunctionCall{Name: name, Arguments: "{}"}}
}

func TestBatchRunsConcurrentlyBetweenCommittedBarriers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	calls := []client.ToolCall{
		testCall("repeated", "read_file"), testCall("repeated", "read_file"),
		testCall("memory", "memory_record_fact"),
		testCall("third", "read_file"), testCall("fourth", "read_file"),
		testCall("complete", "task_complete"), testCall("later", "read_file"),
	}
	secondDone, fourthDone := make(chan struct{}), make(chan struct{})
	var stage atomic.Int32
	var executed atomic.Int32
	batch := New(calls, IsLoopTool, func(ctx context.Context, index int, call client.ToolCall) (client.ToolResult, error) {
		executed.Add(1)
		if stage.Load() != int32(index/3) {
			t.Errorf("call %d overtook a barrier: stage=%d", index, stage.Load())
		}
		switch index {
		case 0:
			select {
			case <-secondDone:
			case <-ctx.Done():
				return client.ToolResult{}, ctx.Err()
			}
		case 1:
			close(secondDone)
		case 3:
			select {
			case <-fourthDone:
			case <-ctx.Done():
				return client.ToolResult{}, ctx.Err()
			}
		case 4:
			close(fourthDone)
		}
		return client.ToolResult{Content: strconv.Itoa(index)}, nil
	})
	for index := range 5 {
		if IsLoopTool(calls[index]) {
			// The owning loop commits memory before starting the next segment.
			stage.Add(1)
			continue
		}
		result, err := batch.Call(ctx, index)
		if err != nil || result.Content != strconv.Itoa(index) {
			t.Fatalf("result %d = %#v, %v", index, result, err)
		}
	}
	if executed.Load() != 4 {
		t.Fatalf("executed %d calls; terminal barrier or later call ran", executed.Load())
	}
}

func TestBatchBoundsWorkersAndKeepsSiblingErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	calls := make([]client.ToolCall, 12)
	for index := range calls {
		calls[index] = testCall(strconv.Itoa(index), "read_file")
	}
	started := make(chan struct{}, len(calls))
	release := make(chan struct{})
	var active, peak, count atomic.Int32
	wantErr := errors.New("one tool failed")
	batch := New(calls, IsLoopTool, func(ctx context.Context, index int, _ client.ToolCall) (client.ToolResult, error) {
		count.Add(1)
		now := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); now > old; old = peak.Load() {
			if peak.CompareAndSwap(old, now) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return client.ToolResult{}, ctx.Err()
		}
		if index == 1 {
			return client.ToolResult{}, wantErr
		}
		return client.ToolResult{Content: strconv.Itoa(index)}, nil
	})
	done := make(chan error, 1)
	go func() { _, err := batch.Call(ctx, 0); done <- err }()
	for range maximumParallelCalls {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("ordinary calls did not overlap")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for index := range calls {
		result, err := batch.Call(ctx, index)
		if index == 1 {
			if !errors.Is(err, wantErr) {
				t.Fatalf("sibling error lost: %v", err)
			}
		} else if err != nil || result.Content != strconv.Itoa(index) {
			t.Fatalf("sibling %d = %#v, %v", index, result, err)
		}
	}
	if count.Load() != int32(len(calls)) || peak.Load() != maximumParallelCalls {
		t.Fatalf("calls=%d peak workers=%d", count.Load(), peak.Load())
	}
}

func TestCancelledBatchJoinsWorkersAndSkipsQueuedCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := make([]client.ToolCall, 8)
	for index := range calls {
		calls[index] = testCall(strconv.Itoa(index), "read_file")
	}
	started, cancelled := make(chan struct{}, 4), make(chan struct{}, 4)
	release := make(chan struct{})
	var count atomic.Int32
	batch := New(calls, IsLoopTool, func(ctx context.Context, _ int, _ client.ToolCall) (client.ToolResult, error) {
		count.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		cancelled <- struct{}{}
		<-release
		return client.ToolResult{}, ctx.Err()
	})
	done := make(chan error, 1)
	go func() { _, err := batch.Call(ctx, 0); done <- err }()
	for range maximumParallelCalls {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	cancel()
	for range maximumParallelCalls {
		<-cancelled
	}
	select {
	case <-done:
		t.Fatal("batch returned before active workers stopped")
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) || count.Load() != maximumParallelCalls {
		t.Fatalf("err=%v calls=%d", err, count.Load())
	}
}

func TestBatchDoesNotDispatchWhenPreparationFails(t *testing.T) {
	wantErr := errors.New("checkpoint failed")
	batch := New([]client.ToolCall{testCall("first", "read_file"), testCall("second", "read_file")}, IsLoopTool,
		func(context.Context, int, client.ToolCall) (client.ToolResult, error) {
			t.Error("dispatched before preparation succeeded")
			return client.ToolResult{}, nil
		})
	batch.Before = func(index int, _ client.ToolCall) error {
		if index == 1 {
			return wantErr
		}
		return nil
	}
	if _, err := batch.Call(t.Context(), 0); !errors.Is(err, wantErr) {
		t.Fatalf("preparation error = %v", err)
	}
}
