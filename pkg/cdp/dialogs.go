package qdriver

import (
	"context"
	"errors"

	"github.com/chromedp/cdproto/cdp"
	cdppage "github.com/chromedp/cdproto/page"
)

type DialogInfo struct{ Type, Message, DefaultPrompt, URL, FrameID string }

// Dialog is a handle to one specific modal, not whichever dialog opens next.
// Opening a modal interrupts active renderer commands with ErrDialogOpen.
// Such commands may already have effects and must not be blindly retried.
type Dialog struct {
	Info    DialogInfo
	page    *Page
	session cdp.Session
	serial  uint64
}

func (p *Page) CurrentDialog() *Dialog {
	p.events.mu.Lock()
	defer p.events.mu.Unlock()
	return p.events.dialog
}
func (p *Page) ExpectDialog(ctx context.Context) (*Subscription, error) {
	return p.Subscribe(ctx, DialogOpened)
}
func (d *Dialog) Accept(ctx context.Context, promptText string) error {
	return d.handle(ctx, true, promptText)
}
func (d *Dialog) Dismiss(ctx context.Context) error { return d.handle(ctx, false, "") }

func (d *Dialog) handle(ctx context.Context, accept bool, text string) error {
	p := d.page
	b := p.task.browser
	op, done := b.operationContext(ctx, p.ctx)
	defer done()
	if err := b.lock(op); err != nil {
		return err
	}
	defer b.unlock()
	if err := p.check(); err != nil {
		return err
	}
	p.events.mu.Lock()
	current := p.events.dialog == d
	p.events.mu.Unlock()
	if !current {
		return ErrDialogStale
	}
	_, err := cdp.Call(op, d.session, cdppage.HandleJavaScriptDialog, cdppage.HandleJavaScriptDialogParams{Accept: accept, PromptText: text})
	if err == nil {
		p.events.mu.Lock()
		if p.events.dialog == d {
			p.events.dialog = nil
		}
		p.events.mu.Unlock()
		p.flushDeferred(op)
	}
	return b.operationError(ctx, "handle dialog", p.id, true, err)
}

// Renderer cleanup is delayed while a modal blocks renderer commands. The
// callbacks run once after handling it, or are discarded when detaching.
func (p *Page) deferForDialog(action func(context.Context)) bool {
	if !p.events.modal() {
		return false
	}
	p.deferredCleanup = append(p.deferredCleanup, action)
	return true
}
func (p *Page) flushDeferred(ctx context.Context) {
	pending := p.deferredCleanup
	p.deferredCleanup = nil
	for i, f := range pending {
		if ctx.Err() != nil || p.events.modal() {
			p.deferredCleanup = append(p.deferredCleanup, pending[i:]...)
			return
		}
		f(ctx)
	}
}
func (p *Page) cleanupEvents(ctx context.Context) error {
	var err error
	if d := p.CurrentDialog(); d != nil && p.initialized && !p.targetClosed {
		_, err = cdp.Call(ctx, d.session, cdppage.HandleJavaScriptDialog, cdppage.HandleJavaScriptDialogParams{Accept: false})
	}
	if p.interceptFiles && !p.targetClosed {
		err = errors.Join(err, p.setFileInterceptionLocked(ctx, false))
	}
	err = errors.Join(err, p.cancelDownloadsLocked(ctx))
	p.deferredCleanup = nil
	return err
}
