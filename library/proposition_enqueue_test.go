package library

import (
	"context"
	"errors"
	"testing"
	"time"
)

type blockedEnqueueJudge struct {
	started chan struct{}
	release chan struct{}
}

func (j blockedEnqueueJudge) JudgeProposition(ctx context.Context, _ PropositionRegisterRequest, _ []PropositionSearchHit) (PropositionDecision, error) {
	j.started <- struct{}{}
	select {
	case <-ctx.Done():
		return PropositionDecision{}, ctx.Err()
	case <-j.release:
		return PropositionDecision{Action: PropositionActionCreate}, nil
	}
}

func enqueueTestInput() PropositionRegisterRequest {
	return PropositionRegisterRequest{Content: "Learning requests acknowledge durable acceptance.", Confidence: 1,
		Refs: []string{"user:confirmed"}, ExtractorModel: "acp-agent", ExtractorVersion: "q-library-mcp-v1"}
}

func TestEnqueuePropositionAcknowledgesBeforeReviewAndKeepsSynchronousAPI(t *testing.T) {
	judge := blockedEnqueueJudge{started: make(chan struct{}, 1), release: make(chan struct{})}
	options := testOptions(t.TempDir(), testConfig(t))
	options.Judge = judge
	runtime, err := EnsureWithOptions(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	input := enqueueTestInput()
	for range 2 {
		ack, err := runtime.Client().EnqueueProposition(ctx, "learning/one", input)
		if err != nil || !ack.Enqueued {
			t.Fatalf("queue acknowledgement: %+v, %v", ack, err)
		}
	}
	select {
	case <-judge.started:
	case <-ctx.Done():
		t.Fatal("review was not started")
	}
	var count int
	if err := runtime.leader.queue.db.QueryRow(`SELECT count(*) FROM proposition_jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("queued jobs=%d, %v", count, err)
	}
	conflicting := input
	conflicting.Content = "A different learning request."
	if ack, err := runtime.Client().EnqueueProposition(ctx, "learning/one", conflicting); err == nil || ack.Enqueued {
		t.Fatalf("conflicting retry accepted: %+v, %v", ack, err)
	}
	// The original API still reports the eventual review outcome to Thinker.
	close(judge.release)
	registered, err := runtime.Client().RegisterProposition(ctx, "learning/one", input)
	if err != nil || registered.Action != PropositionActionCreate || registered.ID == "" {
		t.Fatalf("registration result: %+v, %v", registered, err)
	}
	ack, err := runtime.Client().EnqueueProposition(ctx, "learning/one", input)
	if err != nil || !ack.Enqueued {
		t.Fatalf("completed request retry: %+v, %v", ack, err)
	}
}

func TestEnqueuePropositionAcknowledgesWithoutLibrarian(t *testing.T) {
	runtime, err := EnsureWithOptions(t.Context(), EnsureOptions{Dir: t.TempDir(), Config: testConfig(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ack, err := runtime.Client().EnqueueProposition(ctx, "learning/no-model", enqueueTestInput())
	if err != nil || !ack.Enqueued {
		t.Fatalf("queue acknowledgement: %+v, %v", ack, err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state, detail string
		if err := runtime.leader.queue.db.QueryRow(`SELECT state, coalesce(last_error, '') FROM proposition_jobs WHERE idempotency_key = ?`, "learning/no-model").Scan(&state, &detail); err != nil {
			t.Fatal(err)
		}
		if state == "failed" {
			if detail != "library: proposition judge is not configured" {
				t.Fatalf("background failure: %s", detail)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("background failure was not recorded")
		case <-ticker.C:
		}
	}
	// Validation failures are still rejected before queue acceptance.
	invalid := enqueueTestInput()
	invalid.Content = ""
	if ack, err := runtime.Client().EnqueueProposition(ctx, "learning/invalid", invalid); err == nil || ack.Enqueued {
		t.Fatalf("invalid input accepted: %+v, %v", ack, err)
	}
}

func TestPropositionQueueEnqueueSurvivesCallerCancellationAndReopen(t *testing.T) {
	dir := t.TempDir()
	queue, err := openPropositionQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if _, err := queue.enqueue(ctx, "durable", "digest", enqueueTestInput()); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := queue.enqueue(ctx, "canceled", "digest", enqueueTestInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled queue write: %v", err)
	}
	if err := queue.close(); err != nil {
		t.Fatal(err)
	}
	queue, err = openPropositionQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := queue.claim(t.Context())
	if err != nil || !found || job.Key != "durable" || job.Request.Content != enqueueTestInput().Content {
		t.Fatalf("recovered job: %+v, %v, %v", job, found, err)
	}
	if err := queue.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.enqueue(t.Context(), "write-failure", "digest", enqueueTestInput()); err == nil {
		t.Fatal("failed queue write was acknowledged")
	}
}
