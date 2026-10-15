package webbrowser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/snowmerak/q/diagnostics"
)

// ReaderPool reuses one Chrome process. Each Reader owns an incognito context;
// only page reads take capacity, so model inference does not occupy a read slot.
type ReaderPool struct {
	mu                      sync.Mutex
	owner                   *Runtime
	limit, active, contexts int
	changed                 chan struct{}
	idle                    *time.Timer
	closed                  bool
}

func NewReaderPool() *ReaderPool {
	return &ReaderPool{owner: New(), limit: 2, changed: make(chan struct{})}
}

func (p *ReaderPool) notify() { close(p.changed); p.changed = make(chan struct{}) }

// SetLimit updates process-wide admission. Existing reads finish when lowered.
func (p *ReaderPool) SetLimit(limit int) error {
	if limit < 1 || limit > 8 {
		return errors.New("browser: concurrent reads must be between 1 and 8")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("browser reader pool is closed")
	}
	p.limit = limit
	p.notify()
	return nil
}

func (p *ReaderPool) acquire(ctx context.Context) (func(), error) {
	for {
		p.mu.Lock()
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return nil, err
		}
		if p.closed {
			p.mu.Unlock()
			return nil, errors.New("browser reader pool is closed")
		}
		if p.active < p.limit {
			p.active++
			p.mu.Unlock()
			return func() { p.mu.Lock(); p.active--; p.notify(); p.mu.Unlock() }, nil
		}
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// NewReader is lazy: neither Chrome nor a tab is created until Read.
func (p *ReaderPool) NewReader() *Reader { return &Reader{pool: p} }

type Reader struct {
	mu      sync.Mutex
	pool    *ReaderPool
	browser *rod.Browser
	page    *rod.Page
	closed  bool
}

func (r *Reader) start(ctx context.Context) error {
	p := r.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("browser reader pool is closed")
	}
	if p.idle != nil {
		p.idle.Stop()
		p.idle = nil
	}
	reused := p.owner.browser != nil
	started := time.Now()
	if err := p.owner.start(ctx); err != nil {
		return err
	}
	diagnostics.Logger(ctx).Info("web_search.browser.ready", "process_reused", reused, "duration_ms", time.Since(started).Milliseconds())
	b, err := p.owner.browser.Context(ctx).Incognito()
	if err != nil {
		p.scheduleIdle()
		return err
	}
	// Detach the context's event lifetime from this individual read operation.
	b = b.Context(p.owner.browser.GetContext())
	stopCancellation := context.AfterFunc(ctx, func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = b.Context(cleanup).Close()
	})
	page, err := b.Page(proto.TargetCreateTarget{})
	stopCancellation()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = b.Context(cleanup).Close()
		p.scheduleIdle()
		return err
	}
	page = page.Context(b.GetContext())
	if err := page.Context(ctx).SetBlockedURLs([]string{"*.png*", "*.jpg*", "*.jpeg*", "*.gif*", "*.webp*", "*.svg*", "*.woff*", "*.ttf*", "*.mp4*", "*.webm*", "*.mp3*"}); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = b.Context(cleanup).Close()
		p.scheduleIdle()
		return err
	}
	r.browser, r.page = b, page
	p.contexts++
	return nil
}

// Read performs rendered navigation in the same isolated context across pages.
// Cookies and scripts work normally; common media/font assets are not fetched.
func (r *Reader) Read(ctx context.Context, target string) (PageContent, error) {
	if err := validateURL(target); err != nil {
		return PageContent{}, err
	}
	waiting := time.Now()
	diagnostics.Logger(ctx).Info("web_search.browser.waiting")
	release, err := r.pool.acquire(ctx)
	if err != nil {
		return PageContent{}, err
	}
	defer release()
	r.pool.mu.Lock()
	active, limit := r.pool.active, r.pool.limit
	r.pool.mu.Unlock()
	diagnostics.Logger(ctx).Info("web_search.browser.admitted", "queue_ms", time.Since(waiting).Milliseconds(), "active_reads", active, "read_limit", limit)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return PageContent{}, errors.New("browser reader is closed")
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if r.page == nil {
		if err := r.start(ctx); err != nil {
			return PageContent{}, err
		}
	}
	page := r.page.Context(ctx)
	if err := page.Navigate(target); err != nil {
		return PageContent{}, err
	}
	if err := page.Wait(rod.Eval(`() => document.readyState === 'interactive' || document.readyState === 'complete'`)); err != nil {
		return PageContent{}, err
	}
	if err := page.WaitDOMStable(300*time.Millisecond, .01); err != nil {
		return PageContent{}, err
	}
	obj, err := page.Eval(readScript, 250000)
	if err != nil {
		return PageContent{}, err
	}
	var result PageContent
	if err := obj.Value.Unmarshal(&result); err != nil {
		return PageContent{}, err
	}
	if result.StatusCode >= http.StatusBadRequest {
		return result, fmt.Errorf("browser: HTTP %d for %s", result.StatusCode, result.URL)
	}
	return result, validateURL(result.URL)
}

func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.browser == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := r.browser.Context(ctx).Close()
	r.pool.mu.Lock()
	defer r.pool.mu.Unlock()
	r.pool.contexts--
	r.pool.scheduleIdle()
	return err
}

// scheduleIdle is called with mu held, after a context is disposed or creation fails.
func (p *ReaderPool) scheduleIdle() {
	if p.closed || p.contexts != 0 {
		return
	}
	if p.idle != nil {
		p.idle.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(defaultIdleTimeout, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.idle == timer && p.contexts == 0 {
			_ = p.owner.stop()
			p.idle = nil
		}
	})
	p.idle = timer
}

func (p *ReaderPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.idle != nil {
		p.idle.Stop()
		p.idle = nil
	}
	p.notify()
	return p.owner.Close()
}

var sharedReaderPool struct {
	sync.Mutex
	pool       *ReaderPool
	references int
}

// NewWithSharedReaders ties a process-shared research pool lease to a tool
// runtime. The last runtime closes Chrome; an idle process also expires normally.
func NewWithSharedReaders() (*Runtime, *ReaderPool) {
	sharedReaderPool.Lock()
	defer sharedReaderPool.Unlock()
	if sharedReaderPool.pool == nil {
		sharedReaderPool.pool = NewReaderPool()
	}
	pool := sharedReaderPool.pool
	sharedReaderPool.references++
	runtime := New()
	runtime.releaseReaders = func() error {
		sharedReaderPool.Lock()
		defer sharedReaderPool.Unlock()
		sharedReaderPool.references--
		if sharedReaderPool.references == 0 {
			sharedReaderPool.pool = nil
			return pool.Close()
		}
		return nil
	}
	return runtime, pool
}
