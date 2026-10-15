package websearch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/client/systemone"
)

type searchFunc func(context.Context, Input) (Output, error)

func (f searchFunc) Search(ctx context.Context, in Input) (Output, error) { return f(ctx, in) }

func batchTask(id string) BatchTask { return BatchTask{ID: id, Search: Input{Goal: id}} }

func TestBatchOverlapsSearchesWithinLimitAndKeepsInputOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan string, 3)
	release := map[string]chan struct{}{"slow": make(chan struct{}), "fast": make(chan struct{}), "third": make(chan struct{})}
	var active, peak atomic.Int32
	search := searchFunc(func(ctx context.Context, in Input) (Output, error) {
		n := active.Add(1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		defer active.Add(-1)
		started <- in.Goal
		select {
		case <-release[in.Goal]:
			return Output{Status: "complete", Evidence: []Evidence{{Text: in.Goal}}, SystemOneCalls: 3, CoverageCalls: 1}, nil
		case <-ctx.Done():
			return Output{}, ctx.Err()
		}
	})
	type returned struct {
		out BatchOutput
		err error
	}
	done := make(chan returned, 1)
	go func() {
		out, err := SearchBatch(ctx, search, BatchInput{Tasks: []BatchTask{batchTask("slow"), batchTask("fast"), batchTask("third")}, Concurrency: 2})
		done <- returned{out, err}
	}()
	seen := map[string]bool{}
	for range 2 {
		select {
		case goal := <-started:
			seen[goal] = true
		case <-ctx.Done():
			t.Fatal("two searches did not overlap")
		}
	}
	if !seen["slow"] || !seen["fast"] {
		t.Fatalf("started=%v", seen)
	}
	close(release["fast"])
	select {
	case goal := <-started:
		if goal != "third" {
			t.Fatal(goal)
		}
	case <-ctx.Done():
		t.Fatal("queued task was not admitted")
	}
	close(release["third"])
	close(release["slow"])
	var got returned
	select {
	case got = <-done:
	case <-ctx.Done():
		t.Fatal("batch did not join workers")
	}
	if got.err != nil || got.out.Status != "complete" || peak.Load() != 2 || active.Load() != 0 || got.out.SystemOneCalls != 9 || got.out.CoverageCalls != 3 {
		t.Fatalf("output=%+v error=%v peak=%d active=%d", got.out, got.err, peak.Load(), active.Load())
	}
	for i, id := range []string{"slow", "fast", "third"} {
		if got.out.Results[i].ID != id || got.out.Results[i].Result.Evidence[0].Text != id {
			t.Fatalf("unordered or crossed results=%+v", got.out.Results)
		}
	}
}

func TestBatchFailureDoesNotCancelOtherTasks(t *testing.T) {
	search := searchFunc(func(ctx context.Context, in Input) (Output, error) {
		if in.Goal == "failed" {
			return Output{}, errors.New("fixture provider unavailable")
		}
		if err := ctx.Err(); err != nil {
			t.Error("sibling failure cancelled this search")
		}
		return Output{Status: "complete"}, nil
	})
	out, err := SearchBatch(t.Context(), search, BatchInput{Tasks: []BatchTask{batchTask("failed"), batchTask("ok")}})
	if err != nil || out.Status != "incomplete" || out.Results[0].Status != "error" || out.Results[0].Error.Code != "search_error" || out.Results[1].Status != "complete" {
		t.Fatalf("output=%+v error=%v", out, err)
	}
}

func TestBatchCancellationPreservesEvidenceStopsQueueAndJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	var calls atomic.Int32
	var closed atomic.Bool
	search := searchFunc(func(ctx context.Context, _ Input) (Output, error) {
		calls.Add(1)
		defer closed.Store(true)
		close(started)
		<-ctx.Done()
		return Output{Status: "incomplete", Evidence: []Evidence{{Text: "partial evidence"}}}, ctx.Err()
	})
	done := make(chan BatchOutput, 1)
	go func() {
		out, _ := SearchBatch(ctx, search, BatchInput{Tasks: []BatchTask{batchTask("first"), batchTask("queued")}, Concurrency: 1})
		done <- out
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("search did not start")
	}
	cancel()
	var out BatchOutput
	select {
	case out = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not honor cancellation")
	}
	if calls.Load() != 1 || !closed.Load() || out.Status != "incomplete" || len(out.Results[0].Result.Evidence) != 1 || out.Results[1].Started || out.Results[1].Error.Code != "cancelled" {
		t.Fatalf("output=%+v calls=%d closed=%v", out, calls.Load(), closed.Load())
	}
}

func TestInvalidBatchStartsNoSearches(t *testing.T) {
	search := searchFunc(func(context.Context, Input) (Output, error) {
		t.Fatal("invalid batch started a search")
		return Output{}, nil
	})
	for name, in := range map[string]BatchInput{
		"empty":                {},
		"duplicate ids":        {Tasks: []BatchTask{batchTask("same"), batchTask("same")}},
		"invalid last task":    {Tasks: []BatchTask{batchTask("valid"), {ID: "invalid"}}},
		"too much concurrency": {Tasks: []BatchTask{batchTask("one")}, Concurrency: 5},
		"whitespace id":        {Tasks: []BatchTask{batchTask("has\u00a0space")}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := SearchBatch(t.Context(), search, in); err == nil {
				t.Fatal("invalid batch accepted")
			}
		})
	}
}

func TestConcurrentEngineSearchesKeepSessionsAndEvidenceIndependent(t *testing.T) {
	var mu sync.Mutex
	var sessions []*fakeSession
	pages := map[string]Document{}
	var tasks []BatchTask
	for i := range 3 {
		id := fmt.Sprintf("task%d", i)
		u := "https://example.com/" + id
		pages[u] = doc(u, "Evidence only for "+id)
		tasks = append(tasks, BatchTask{ID: id, Search: Input{Goal: id, StartURL: u}})
	}
	engine := &Engine{DecisionModel: "gemma4-e4b", CoverageModel: "qwen3.8-27b", Discovery: metaSearchFunc(func(context.Context, string) (SearchResults, error) { return SearchResults{}, nil }), NewSession: func() Session {
		s := &fakeSession{pages: pages}
		mu.Lock()
		sessions = append(sessions, s)
		mu.Unlock()
		return s
	}}
	engine.Decision = decisionFunc(func(ctx context.Context, req systemone.Request, _ systemone.CallOptions) (*systemone.Result, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Minute || time.Until(deadline) < 29*time.Minute {
			t.Error("batch navigation must allow thirty minutes")
		}
		answers := map[string]systemone.Answer{}
		for key := range req.Questions {
			choice := "accept"
			if strings.HasPrefix(key, "block_") {
				choice = "keep"
			}
			if key == "action" {
				choice = "collect_0"
				if len(req.State.(map[string]any)["evidence"].([]Evidence)) > 0 {
					choice = "finish"
				}
			}
			answers[key] = systemone.Answer{Type: systemone.QuestionChoice, Choice: choice}
		}
		return &systemone.Result{Answers: answers}, nil
	})
	engine.CoverageDecision = decisionFunc(func(_ context.Context, req systemone.Request, _ systemone.CallOptions) (*systemone.Result, error) {
		if req.Model != "qwen3.8-27b" {
			t.Error("wrong completion model")
		}
		answers := map[string]systemone.Answer{}
		for key := range req.Questions {
			answers[key] = systemone.Answer{Type: systemone.QuestionChoice, Choice: "supported"}
		}
		return &systemone.Result{Answers: answers}, nil
	})
	out, err := SearchBatch(t.Context(), engine, BatchInput{Tasks: tasks})
	if err != nil || out.Status != "complete" || len(sessions) != 3 || out.CoverageCalls != 3 || out.PlannerCalls != 0 {
		t.Fatalf("output=%+v err=%v sessions=%d", out, err, len(sessions))
	}
	for i, item := range out.Results {
		if item.Result.Evidence[0].URL != tasks[i].Search.StartURL || item.Result.Evidence[0].Text != "Evidence only for "+tasks[i].ID {
			t.Fatalf("crossed evidence=%+v", item)
		}
	}
	for _, s := range sessions {
		if !s.closed {
			t.Fatal("session was not closed")
		}
	}
}

func TestBatchPreservesExplicitDecisionTimeout(t *testing.T) {
	search := searchFunc(func(_ context.Context, in Input) (Output, error) {
		if in.DecisionTimeoutSeconds != 37 {
			t.Fatalf("explicit timeout replaced: %d", in.DecisionTimeoutSeconds)
		}
		return Output{Status: "complete"}, nil
	})
	task := batchTask("explicit")
	task.Search.DecisionTimeoutSeconds = 37
	if out, err := SearchBatch(t.Context(), search, BatchInput{Tasks: []BatchTask{task}}); err != nil || out.Status != "complete" {
		t.Fatalf("output=%+v err=%v", out, err)
	}
}

func TestSearchLoopsOverlapWithSeparateModelConcurrency(t *testing.T) {
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			sessions := make(chan struct{}, 2)
			native := make(chan struct{}, 2)
			release := make(chan struct{})
			var calls, active, peak atomic.Int32
			engine := &Engine{NewSession: func() Session {
				// Both sessions can start while the first model call is blocked.
				sessions <- struct{}{}
				return &fakeSession{pages: map[string]Document{"https://example.com": doc("https://example.com", "Evidence.")}}
			}}
			engine.Decision = decisionFunc(func(ctx context.Context, _ systemone.Request, _ systemone.CallOptions) (*systemone.Result, error) {
				calls.Add(1)
				n := active.Add(1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				defer active.Add(-1)
				native <- struct{}{}
				select {
				case <-release:
					return &systemone.Result{Answers: map[string]systemone.Answer{"action": {Type: systemone.QuestionChoice, Choice: "stop"}}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			tasks := []BatchTask{batchTask("one"), batchTask("two")}
			for i := range tasks {
				tasks[i].Search.StartURL = "https://example.com"
			}
			done := make(chan BatchOutput, 1)
			go func() {
				out, _ := SearchBatch(ctx, engine, BatchInput{Tasks: tasks, Concurrency: 2, ModelConcurrency: limit})
				done <- out
			}()
			for range 2 {
				select {
				case <-sessions:
				case <-ctx.Done():
					t.Fatal("search sessions did not overlap")
				}
			}
			for range limit {
				select {
				case <-native:
				case <-ctx.Done():
					t.Fatal("model concurrency was not honored")
				}
			}
			close(release)
			select {
			case out := <-done:
				if out.ModelConcurrency != limit || peak.Load() != int32(limit) || calls.Load() != 2 || active.Load() != 0 {
					t.Fatalf("output=%+v peak=%d calls=%d", out, peak.Load(), calls.Load())
				}
			case <-ctx.Done():
				t.Fatal("model slots were not released")
			}
		})
	}
}
