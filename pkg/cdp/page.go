package qdriver

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Page is a task-scoped handle to one browser tab. Operations are serialized
// with lifecycle changes. Use ID to correlate it with Browser.Pages.
type Page struct {
	task               *Task
	id                 string
	owned              bool
	managed            bool
	ctx                context.Context
	cancel             context.CancelFunc
	waitCancel         context.CancelFunc
	stopOnce           sync.Once
	stopped            chan struct{}
	refs               map[uint64]refEntry
	nextRef            uint64
	document           string
	frames             map[cdp.FrameID]*frameState
	frameSessions      map[cdp.FrameID]*frameConnection
	events             *eventHub
	initialized        bool
	targetClosed       bool
	interceptFiles     bool // Protected by the operation gate.
	deferredCleanup    []func(context.Context)
	pointerSession     string
	pointerX, pointerY float64
}

// ID returns this tab's browser target ID, which is not durable across restarts.
func (p *Page) ID() string { return p.id }

// Owned reports whether this handle was created, rather than borrowed, by a task.
func (p *Page) Owned() bool { return p.owned }

func (p *Page) check() error {
	if !p.managed {
		return ErrPageReleased
	}
	if err := p.task.check(); err != nil {
		return err
	}
	if p.ctx.Err() != nil {
		return ErrPageUnavailable
	}
	return nil
}

func (p *Page) initialize(ctx context.Context) error {
	if p.initialized {
		return nil
	}
	// chromedp starts its target event reader using the first Run context.
	// Initialize with the page lifetime, not a short-lived command context.
	// A failed setup cancels only this session and keeps the tab for cleanup.
	stop := context.AfterFunc(ctx, p.cancel)
	err := chromedp.Do(p.ctx)
	if err == nil {
		// The driver resolves isolated execution contexts explicitly. Runtime
		// event streaming is unnecessary after chromedp's bootstrap and can
		// cause observable console-object serialization in the document.
		_, err = cdp.Call(ctx, chromedp.FromContext(p.ctx).Target, runtime.Disable, cdp.Empty{})
	}
	stop()
	err = errors.Join(err, ctx.Err())
	if err != nil {
		p.cancel()
	}
	p.initialized = err == nil
	if p.initialized {
		p.events.sessionAdded(string(chromedp.FromContext(p.ctx).Target.SessionID))
	}
	return err
}

func runPage[T any](caller context.Context, p *Page, name string, effects bool, action chromedp.Action[T]) (T, error) {
	return runPageOperation(caller, p, name, func(ctx context.Context, target *chromedp.Target, mark func()) (T, error) {
		if effects {
			mark()
		}
		return action(ctx, target)
	})
}

func runPageOperation[T any](caller context.Context, p *Page, name string, action func(context.Context, *chromedp.Target, func()) (T, error)) (T, error) {
	var zero T
	b := p.task.browser
	op, done := b.operationContext(caller, b.ctx)
	defer done()
	if err := b.lock(op); err != nil {
		return zero, b.operationError(caller, name, p.id, false, err)
	}
	defer b.unlock()
	if err := p.check(); err != nil {
		return zero, err
	}
	if err := p.initialize(op); err != nil {
		return zero, err
	}
	pageContext, pageDone := b.operationContext(op, p.ctx)
	defer pageDone()
	pageContext, interrupt := context.WithCancelCause(pageContext)
	if err := p.events.begin(interrupt); err != nil {
		interrupt(nil)
		return zero, err
	}
	defer func() { p.events.end(); interrupt(nil) }()
	effects := false
	result, err := chromedp.Run(pageContext, func(ctx context.Context, target *chromedp.Target) (T, error) {
		return action(ctx, target, func() { effects = true })
	})
	if err != nil {
		err = errors.Join(err, context.Cause(pageContext))
	}
	return result, b.operationError(caller, name, p.id, effects, err)
}

// Goto navigates and waits for the load event. Timeout does not stop or undo
// navigation. A failed dispatched navigation is never automatically retried.
func (p *Page) Goto(ctx context.Context, url string) error {
	_, err := runPage(ctx, p, "navigate", true, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		p.invalidateRefs()
		p.pointerSession = ""
		_, err := chromedp.Navigate(url)(ctx, t)
		return err
	}))
	return err
}

// Title reads the document title.
func (p *Page) Title(ctx context.Context) (string, error) {
	return runPage(ctx, p, "title", false, chromedp.Title())
}

// URL reads the current document URL.
func (p *Page) URL(ctx context.Context) (string, error) {
	return runPage(ctx, p, "url", false, chromedp.Evaluate[string]("location.href"))
}

// Evaluate runs an expression in the top document, awaits a returned Promise,
// and returns its JSON value. JavaScript exceptions are returned as errors.
// The expression may have side effects that outlive a timeout.
func (p *Page) Evaluate(ctx context.Context, expression string) (json.RawMessage, error) {
	value, err := runPage(ctx, p, "evaluate", true, func(ctx context.Context, t *chromedp.Target) ([]byte, error) {
		p.invalidateRefs()
		return chromedp.Evaluate[[]byte](expression, chromedp.EvalAwaitPromise)(ctx, t)
	})
	return json.RawMessage(value), err
}

// Screenshot captures the visible viewport as PNG bytes.
func (p *Page) Screenshot(ctx context.Context) ([]byte, error) {
	return runPage(ctx, p, "screenshot", false, chromedp.CaptureScreenshot())
}

// Activate brings this managed tab to the foreground.
func (p *Page) Activate(ctx context.Context) error {
	_, err := runPage(ctx, p, "activate", true, chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		_, err := cdp.Call(ctx, p.task.browser.session(), target.ActivateTarget, target.ActivateTargetParams{TargetID: target.ID(p.id)})
		return err
	}))
	return err
}

// Close closes an owned tab and confirms its disappearance. Borrowed tabs must
// be released instead. Calling Close again on an already released owned handle
// is harmless; it cannot close a replacement tab.
func (p *Page) Close(ctx context.Context) error {
	b := p.task.browser
	op, done := b.operationContext(ctx, b.ctx)
	defer done()
	if err := b.lock(op); err != nil {
		return b.operationError(ctx, "close page", p.id, false, err)
	}
	defer b.unlock()
	if !p.owned {
		return ErrNotOwned
	}
	if !p.managed {
		return nil
	}
	if err := b.connectionError(); err != nil {
		return err
	}
	err := p.closeLocked(op)
	return b.operationError(ctx, "close page", p.id, true, err)
}

func (p *Page) closeLocked(ctx context.Context) error {
	b := p.task.browser
	exists, err := b.pageExists(ctx, p.id)
	if err != nil {
		return err
	}
	if exists {
		_, closeErr := cdp.Call(ctx, b.session(), target.CloseTarget, target.CloseTargetParams{TargetID: target.ID(p.id)})
		// CloseTarget can acknowledge before the target actually disappears.
		// If another client closed it concurrently, absence is also success.
		if err := b.waitPageClosed(ctx, p.id); err != nil {
			return errors.Join(closeErr, err)
		}
	}
	p.targetClosed = true
	p.task.forget(p)
	return p.stop(ctx)
}

func (p *Page) stop(ctx context.Context) error {
	p.events.close(ErrPageReleased)
	resourceErr := p.cleanupEvents(ctx)
	p.stopOnce.Do(func() {
		p.cancel()
		// chromedp's cancellation waits for detach. Let our cleanup budget bound
		// that wait; its own detach timeout bounds this finalizer if it runs late.
		go func() {
			p.waitCancel()
			close(p.stopped)
		}()
	})
	frameErr := resourceErr
	for _, connection := range p.frameSessions {
		frameErr = errors.Join(frameErr, connection.stop(ctx))
	}
	select {
	case <-p.stopped:
		return frameErr
	case <-ctx.Done():
		return errors.Join(frameErr, ctx.Err())
	}
}
