package qdriver

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Task groups managed pages. Its storage context is shared with the browser;
// it is not a separate profile or a security sandbox.
type Task struct {
	browser   *Browser
	name      string
	pages     map[string]*Page
	finishing bool
	finished  bool
	result    FinishResult
}

// FinishResult records confirmed cleanup outcomes, using browser target IDs.
// Remaining pages were not confirmed closed; they may still exist.
type FinishResult struct {
	Closed    []string
	Released  []string
	Kept      []string
	Remaining []string
}

func (r *FinishResult) append(other FinishResult) {
	r.Closed = append(r.Closed, other.Closed...)
	r.Released = append(r.Released, other.Released...)
	r.Kept = append(r.Kept, other.Kept...)
	r.Remaining = append(r.Remaining, other.Remaining...)
}

func (r FinishResult) clone() FinishResult {
	r = FinishResult{
		Closed: slices.Clone(r.Closed), Released: slices.Clone(r.Released),
		Kept: slices.Clone(r.Kept), Remaining: slices.Clone(r.Remaining),
	}
	slices.Sort(r.Closed)
	slices.Sort(r.Released)
	slices.Sort(r.Kept)
	slices.Sort(r.Remaining)
	return r
}

// Name returns the task's descriptive name.
func (t *Task) Name() string { return t.name }

func (t *Task) check() error {
	if err := t.browser.connectionError(); err != nil {
		return err
	}
	if t.finishing {
		return ErrTaskFinished
	}
	return nil
}

// NewPage opens an owned about:blank tab. If creation times out, the returned
// OperationError may indicate an unknown effect; the call is never retried.
// If creation succeeds but session setup fails, a non-nil Page is returned
// alongside the error so it can still be closed or cleaned up by Finish.
func (t *Task) NewPage(ctx context.Context) (*Page, error) {
	b := t.browser
	op, done := b.operationContext(ctx, b.ctx)
	defer done()
	if err := b.lock(op); err != nil {
		return nil, b.operationError(ctx, "new page", "", false, err)
	}
	defer b.unlock()
	if err := t.check(); err != nil {
		return nil, err
	}
	result, err := cdp.Call(op, b.session(), target.CreateTarget, target.CreateTargetParams{URL: "about:blank"})
	if err != nil {
		return nil, b.operationError(ctx, "new page", "", true, err)
	}
	if result.TargetID == "" {
		return nil, b.operationError(ctx, "new page", "", true, errors.New("browser returned no target ID"))
	}
	p := t.registerPage(string(result.TargetID), true)
	return p, b.operationError(ctx, "attach new page", p.id, true, p.initialize(op))
}

// Adopt borrows an existing page without changing its creation provenance.
// One page can belong to only one task on this Browser connection.
// A non-nil Page returned with a setup error remains managed until released.
func (t *Task) Adopt(ctx context.Context, id string) (*Page, error) {
	b := t.browser
	op, done := b.operationContext(ctx, b.ctx)
	defer done()
	if err := b.lock(op); err != nil {
		return nil, b.operationError(ctx, "adopt page", id, false, err)
	}
	defer b.unlock()
	if err := t.check(); err != nil {
		return nil, err
	}
	if _, exists := b.pages[id]; exists {
		return nil, ErrPageInUse
	}
	// A claimed popup is already a task resource even before its page session
	// is attached. Do not let Adopt silently downgrade or transfer ownership.
	b.eventsMu.Lock()
	pending := false
	for _, popups := range b.pendingPopups {
		if popups[id] != nil {
			pending = true
			break
		}
	}
	b.eventsMu.Unlock()
	if pending {
		return nil, ErrPageInUse
	}
	exists, err := b.pageExists(op, id)
	if err != nil {
		return nil, b.operationError(ctx, "adopt page", id, false, err)
	}
	if !exists {
		return nil, ErrPageNotFound
	}
	p := t.registerPage(id, false)
	return p, b.operationError(ctx, "attach borrowed page", id, false, p.initialize(op))
}

func (t *Task) registerPage(id string, owned bool) *Page {
	p := &Page{task: t, id: id, owned: owned, managed: true, stopped: make(chan struct{})}
	p.events = newEventHub(p)
	life, cancel := context.WithCancel(t.browser.ctx)
	ctx, waitCancel := chromedp.NewContext(life,
		chromedp.WithTargetID(target.ID(id)), chromedp.WithDetachOnCancel(),
		chromedp.WithListener(func(event any) { p.onTargetEvent(chromedp.FromContext(p.ctx).Target, event) }))
	p.ctx, p.cancel, p.waitCancel = ctx, cancel, waitCancel
	t.pages[id] = p
	t.browser.pages[id] = p
	t.browser.eventsMu.Lock()
	t.browser.eventPages[id] = p
	t.browser.eventsMu.Unlock()
	return p
}

// Release returns a managed page without closing its tab, even if it was
// originally created by this task. Its old Page handle becomes unusable.
func (t *Task) Release(ctx context.Context, page *Page) error {
	b := t.browser
	op, done := b.operationContext(ctx, b.ctx)
	defer done()
	if err := b.lock(op); err != nil {
		return b.operationError(ctx, "release page", "", false, err)
	}
	defer b.unlock()
	if err := t.check(); err != nil {
		return err
	}
	if page == nil || page.task != t || t.pages[page.id] != page {
		return ErrInvalidPage
	}
	t.forget(page)
	return page.stop(op)
}

func (t *Task) forget(page *Page) {
	page.managed = false
	page.invalidateRefs()
	delete(t.pages, page.id)
	delete(t.browser.pages, page.id)
	t.browser.eventsMu.Lock()
	delete(t.browser.eventPages, page.id)
	for id, p := range t.browser.eventFrames {
		if p == page {
			delete(t.browser.eventFrames, id)
		}
	}
	t.browser.eventsMu.Unlock()
}

// Finish closes owned tabs and releases borrowed tabs. keep can name owned tabs
// to leave open. Cleanup uses a fresh CleanupTimeout, independent of ctx's
// cancellation. A partial finish can be retried; new work remains disabled.
func (t *Task) Finish(ctx context.Context, keep ...*Page) (FinishResult, error) {
	b := t.browser
	cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), b.options.CleanupTimeout)
	defer done()
	if err := b.lock(cleanup); err != nil {
		return FinishResult{}, err
	}
	defer b.unlock()
	if t.finished {
		return t.result.clone(), nil
	}
	if err := b.connectionError(); err != nil {
		t.drainPopupsLocked()
		info := t.result.clone()
		for id := range t.pages {
			info.Remaining = append(info.Remaining, id)
		}
		return info.clone(), err
	}
	kept := make(map[*Page]bool, len(keep))
	for _, p := range keep {
		if p == nil || p.task != t || !p.owned || t.pages[p.id] != p {
			return FinishResult{}, ErrInvalidPage
		}
		kept[p] = true
	}
	return t.finishLocked(cleanup, kept)
}

func (t *Task) finishLocked(ctx context.Context, keep map[*Page]bool) (FinishResult, error) {
	t.finishing = true
	t.drainPopupsLocked()
	var cleanupErr error
	for _, p := range t.pages {
		if !p.owned || keep[p] {
			t.forget(p)
			if p.owned {
				t.result.Kept = append(t.result.Kept, p.id)
			} else {
				t.result.Released = append(t.result.Released, p.id)
			}
			cleanupErr = errors.Join(cleanupErr, p.stop(ctx))
			continue
		}
		if err := p.closeLocked(ctx); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
		if !p.managed {
			t.result.Closed = append(t.result.Closed, p.id)
		}
	}
	t.finished = len(t.pages) == 0
	if t.finished {
		delete(t.browser.tasks, t)
	}
	info := t.result.clone()
	for id := range t.pages {
		info.Remaining = append(info.Remaining, id)
	}
	return info.clone(), cleanupErr
}

func (b *Browser) pageExists(ctx context.Context, id string) (bool, error) {
	result, err := cdp.Call(ctx, b.session(), target.GetTargets, target.GetTargetsParams{})
	if err != nil {
		return false, err
	}
	for _, info := range result.TargetInfos {
		if string(info.TargetID) == id && info.Type == "page" {
			return true, nil
		}
	}
	return false, nil
}

func (b *Browser) waitPageClosed(ctx context.Context, id string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		exists, err := b.pageExists(ctx, id)
		if err != nil || !exists {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
