package qdriver

import "context"

// Popup identifies a tab with a verified managed opener. ExpectPopup claims
// the first observed popup for task cleanup even if Page is never called.
// A popup from an ordinary Subscribe is borrowed when opened with Page.
type Popup struct {
	ID, URL  string
	targetID string // Immutable identity; exported fields are descriptive copies.
	opener   *Page
	owned    bool
}

// ExpectPopup arms a single ownership claim before a click or other trigger.
// Call Close on the returned subscription when finished with it.
func (p *Page) ExpectPopup(ctx context.Context) (*Subscription, error) {
	return p.subscribe(ctx, true, PopupOpened)
}

// Page registers the popup in its opener's task and attaches to it. The same
// task receives the same handle on repeated calls. Closed popups fail normally.
func (p *Popup) Page(ctx context.Context) (*Page, error) {
	b := p.opener.task.browser
	op, done := b.operationContext(ctx, b.ctx)
	defer done()
	if err := b.lock(op); err != nil {
		return nil, err
	}
	defer b.unlock()
	if err := p.opener.task.check(); err != nil {
		return nil, err
	}
	page := b.pages[p.targetID]
	if page != nil && page.task != p.opener.task {
		return nil, ErrPageInUse
	}
	if page == nil {
		exists, err := b.pageExists(op, p.targetID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrPageNotFound
		}
		page = p.opener.task.registerPage(p.targetID, p.owned)
	}
	b.eventsMu.Lock()
	delete(b.pendingPopups[page.task], p.targetID)
	b.eventsMu.Unlock()
	return page, b.operationError(ctx, "attach popup", p.targetID, false, page.initialize(op))
}

func (t *Task) drainPopupsLocked() {
	b := t.browser
	b.eventsMu.Lock()
	b.eventFinishing[t] = true
	pending := b.pendingPopups[t]
	delete(b.pendingPopups, t)
	b.eventsMu.Unlock()
	for id := range pending {
		if b.pages[id] == nil {
			t.registerPage(id, true)
		}
	}
}
