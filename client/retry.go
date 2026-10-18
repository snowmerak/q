package client

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/snowmerak/q/diagnostics"
)

// RetryPolicy bounds a logical request, including stream reconnections before
// the first returned chunk. MaxAttempts includes the initial attempt; 0 or 1
// disables retries. Backoff waits always respect the caller's context.
type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

type requestRetry struct {
	ctx      context.Context
	policy   RetryPolicy
	attempts int
}

func retryRequest[T any](retry *requestRetry, operation func() (T, error)) (T, error) {
	for {
		if err := retry.ctx.Err(); err != nil {
			var zero T
			return zero, err
		}
		retry.attempts++
		result, err := operation()
		if err == nil {
			return result, nil
		}
		if err := retry.wait(err); err != nil {
			return result, err
		}
	}
}

func (retry *requestRetry) wait(cause error) error {
	if err := retry.ctx.Err(); err != nil {
		return err
	}
	if retry.attempts >= max(1, retry.policy.MaxAttempts) || !retryableRequestError(cause) {
		return cause
	}
	delay := retry.policy.InitialBackoff
	if delay <= 0 {
		delay = 250 * time.Millisecond
	}
	maximum := retry.policy.MaxBackoff
	if maximum <= 0 {
		maximum = 2 * time.Second
	}
	for index := 1; index < retry.attempts && delay < maximum; index++ {
		delay = min(delay, maximum/2) * 2
	}
	delay = min(delay, maximum)
	diagnostics.Logger(retry.ctx).Warn("model.request.retrying", "attempt", retry.attempts+1,
		"max_attempts", retry.policy.MaxAttempts, "delay_ms", delay.Milliseconds(), "reason_code", "transient_request_failure")
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-retry.ctx.Done():
		return retry.ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryableRequestError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if apiError, ok := errors.AsType[*APIError](err); ok {
		return apiError.StatusCode == http.StatusRequestTimeout || apiError.StatusCode == http.StatusTooManyRequests ||
			(apiError.StatusCode >= 500 && apiError.StatusCode <= 599)
	}
	var networkError net.Error
	return errors.As(err, &networkError) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

type retryStreamSource[T any] interface {
	Recv() (*T, error)
	Close() error
}

type retryStream[T any] struct {
	mu       sync.Mutex
	recvMu   sync.Mutex
	cancel   context.CancelFunc
	inner    retryStreamSource[T]
	open     func() (retryStreamSource[T], error)
	retry    requestRetry
	received bool
	closed   bool
}

func openRetryStream[T any](ctx context.Context, policy RetryPolicy, open func(context.Context) (retryStreamSource[T], error)) (*retryStream[T], error) {
	ctx, cancel := context.WithCancel(ctx)
	attempt := func() (retryStreamSource[T], error) { return open(ctx) }
	stream := &retryStream[T]{open: attempt, cancel: cancel, retry: requestRetry{ctx: ctx, policy: policy}}
	inner, err := retryRequest(&stream.retry, attempt)
	if err != nil {
		cancel()
		return nil, err
	}
	stream.inner = inner
	return stream, nil
}

func (stream *retryStream[T]) Recv() (*T, error) {
	stream.recvMu.Lock()
	defer stream.recvMu.Unlock()
	stream.mu.Lock()
	closed := stream.closed
	inner := stream.inner
	stream.mu.Unlock()
	if closed {
		return nil, io.EOF
	}
	for {
		chunk, err := inner.Recv()
		if chunk != nil {
			stream.received = true
		}
		// Never replay content, reasoning or tool fragments already exposed to
		// the caller. A normal EOF retains the existing empty-response policy.
		if err == nil || stream.received || errors.Is(err, io.EOF) {
			return chunk, err
		}
		_ = inner.Close()
		if err := stream.retry.wait(err); err != nil {
			return nil, err
		}
		inner, err = retryRequest(&stream.retry, stream.open)
		if err != nil {
			return nil, err
		}
		stream.mu.Lock()
		if stream.closed {
			stream.mu.Unlock()
			_ = inner.Close()
			return nil, context.Canceled
		}
		stream.inner = inner
		stream.mu.Unlock()
	}
}

func (stream *retryStream[T]) Close() error {
	stream.mu.Lock()
	if stream.closed {
		stream.mu.Unlock()
		return nil
	}
	stream.closed = true
	inner := stream.inner
	stream.mu.Unlock()
	stream.cancel()
	return inner.Close()
}

func (stream *retryStream[T]) ResponseHeaders() http.Header {
	stream.mu.Lock()
	inner := stream.inner
	stream.mu.Unlock()
	if headers, ok := inner.(ResponseHeaderer); ok {
		return headers.ResponseHeaders()
	}
	return nil
}
