package qdriver

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// EventKind selects a stream. Subscriptions are independent and ready on return.
type EventKind string

const (
	PopupOpened       EventKind = "popup"
	DialogOpened      EventKind = "dialog"
	FileChooserOpened EventKind = "fileChooser"
	DownloadStarted   EventKind = "download"
	RequestStarted    EventKind = "request"
	RequestFinished   EventKind = "requestFinished"
	RequestFailed     EventKind = "requestFailed"
)

// Event contains one payload corresponding to Kind. Handles remain bound to
// the originating page, session, and (where relevant) document.
type Event struct {
	Kind            EventKind
	PageID, FrameID string
	Popup           *Popup
	Dialog          *Dialog
	FileChooser     *FileChooser
	Download        *Download
	Request         RequestInfo
}

// RequestInfo omits headers and bodies. ID is unique only within SessionID.
type RequestInfo struct{ ID, SessionID, URL, Method, ResourceType, Error string }

// Subscription has a bounded queue. Overflow terminates it with
// ErrEventOverflow; it never silently discards an event. Next does not hold
// the browser operation gate. Close is safe concurrently with Next.
type Subscription struct {
	hub                 *eventHub
	kinds               map[EventKind]bool
	queue               chan Event
	done                chan struct{}
	err                 error // Protected by hub.mu.
	stop                func() bool
	claimPopup, claimed bool
}

type eventHub struct {
	mu            sync.Mutex
	page          *Page
	subs          map[*Subscription]bool
	err           error
	active        context.CancelCauseFunc
	dialog        *Dialog
	serial        uint64
	requests      map[string]bool
	requestFrames map[string]cdp.FrameID
	documents     map[cdp.FrameID]string
	sessions      map[string]bool
	epoch         uint64
	documentEpoch uint64
	networkErr    error
	changed       chan struct{}
	lastActivity  time.Time
}

func newEventHub(p *Page) *eventHub {
	return &eventHub{page: p, subs: make(map[*Subscription]bool), requests: make(map[string]bool), requestFrames: make(map[string]cdp.FrameID), documents: make(map[cdp.FrameID]string), sessions: make(map[string]bool), changed: make(chan struct{}), lastActivity: time.Now()}
}

func (h *eventHub) notifyLocked() {
	close(h.changed)
	h.changed = make(chan struct{})
	h.lastActivity = time.Now()
}
func (h *eventHub) finishLocked(s *Subscription, err error) {
	if s.err != nil {
		return
	}
	s.err = err
	delete(h.subs, s)
	close(s.done)
	if s.stop != nil {
		s.stop()
	}
}
func (h *eventHub) close(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return
	}
	h.err = err
	for s := range h.subs {
		h.finishLocked(s, err)
	}
	if h.active != nil {
		h.active(err)
	}
	h.notifyLocked()
}
func (h *eventHub) publishLocked(e Event) {
	e.PageID = h.page.id
	for s := range h.subs {
		if !s.kinds[e.Kind] {
			continue
		}
		select {
		case s.queue <- e:
		default:
			h.finishLocked(s, ErrEventOverflow)
		}
	}
}
func (h *eventHub) publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err == nil {
		h.publishLocked(e)
	}
}
func (h *eventHub) begin(cancel context.CancelCauseFunc) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return h.err
	}
	if h.dialog != nil {
		return ErrDialogOpen
	}
	h.active = cancel
	return nil
}
func (h *eventHub) end()        { h.mu.Lock(); h.active = nil; h.mu.Unlock() }
func (h *eventHub) modal() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.dialog != nil }

// Subscribe prepares observation before a triggering action. An empty kind
// list observes all supported events. ctx owns the subscription lifetime;
// each Next also accepts its own deadline. Waiting has no CommandTimeout.
func (p *Page) Subscribe(ctx context.Context, kinds ...EventKind) (*Subscription, error) {
	return p.subscribe(ctx, false, kinds...)
}

func (p *Page) subscribe(ctx context.Context, claim bool, kinds ...EventKind) (*Subscription, error) {
	selected := make(map[EventKind]bool)
	if len(kinds) == 0 {
		kinds = []EventKind{PopupOpened, DialogOpened, FileChooserOpened, DownloadStarted, RequestStarted, RequestFinished, RequestFailed}
	}
	for _, k := range kinds {
		switch k {
		case PopupOpened, DialogOpened, FileChooserOpened, DownloadStarted, RequestStarted, RequestFinished, RequestFailed:
			selected[k] = true
		default:
			return nil, errors.New("unknown event kind")
		}
	}
	_, err := runPage(ctx, p, "prepare event subscription", false, func(ctx context.Context, t *chromedp.Target) (struct{}, error) {
		_, err := p.refreshFrames(ctx, t)
		return struct{}{}, err
	})
	if err != nil {
		return nil, err
	}
	h := p.events
	h.mu.Lock()
	if h.err != nil {
		h.mu.Unlock()
		return nil, h.err
	}
	if len(h.subs) >= p.task.browser.options.MaxSubscriptions {
		h.mu.Unlock()
		return nil, ErrEventOverflow
	}
	s := &Subscription{hub: h, kinds: selected, queue: make(chan Event, p.task.browser.options.EventBuffer), done: make(chan struct{}), claimPopup: claim}
	h.subs[s] = true
	h.mu.Unlock()
	// Assign stop while holding the same mutex used by Close. A canceled
	// context may invoke the callback before AfterFunc itself returns.
	stop := context.AfterFunc(ctx, func() { s.closeWith(ctx.Err()) })
	h.mu.Lock()
	s.stop = stop
	closed := s.err != nil
	h.mu.Unlock()
	if closed {
		stop()
	}
	if ctx.Err() != nil {
		s.closeWith(ctx.Err())
		return nil, ctx.Err()
	}
	return s, nil
}

func (s *Subscription) closeWith(err error) {
	s.hub.mu.Lock()
	s.hub.finishLocked(s, err)
	stop := s.stop
	s.hub.mu.Unlock()
	if stop != nil {
		stop()
	}
}
func (s *Subscription) Close()          { s.closeWith(ErrSubscriptionClosed) }
func (s *Subscription) terminal() error { s.hub.mu.Lock(); defer s.hub.mu.Unlock(); return s.err }
func (s *Subscription) Next(ctx context.Context) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if err := s.terminal(); err != nil {
		return Event{}, err
	}
	select {
	case <-ctx.Done():
		return Event{}, ctx.Err()
	case <-s.done:
		return Event{}, s.terminal()
	case e := <-s.queue:
		if err := s.terminal(); err != nil {
			return Event{}, err
		}
		return e, nil
	}
}

func (b *Browser) startEvents(ctx context.Context) error {
	if err := chromedp.ListenBrowser(b.ctx, b.onBrowserEvent); err != nil {
		return err
	}
	if _, err := cdp.Call(ctx, b.session(), target.SetDiscoverTargets, target.SetDiscoverTargetsParams{Discover: true}); err != nil {
		return err
	}
	go func() {
		select {
		case <-b.ctx.Done():
		case <-b.session().LostConnection:
		}
		b.endEvents(b.connectionError())
	}()
	return nil
}
func (b *Browser) endEvents(err error) {
	if err == nil {
		err = ErrConnectionLost
	}
	b.eventsMu.Lock()
	defer b.eventsMu.Unlock()
	for _, p := range b.eventPages {
		p.events.close(err)
	}
	for _, d := range b.downloads {
		d.finish(err)
	}
}
func (b *Browser) onBrowserEvent(event any) {
	b.eventsMu.Lock()
	defer b.eventsMu.Unlock()
	switch e := event.(type) {
	case *target.EventTargetCreated:
		info := e.TargetInfo
		if info == nil || info.Type != "page" {
			return
		}
		p := b.eventPages[string(info.OpenerID)]
		if p == nil {
			p = b.eventFrames[info.OpenerFrameID]
		}
		if p == nil {
			return
		}
		popup := &Popup{opener: p, ID: string(info.TargetID), targetID: string(info.TargetID), URL: info.URL}
		h := p.events
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.err != nil {
			return
		}
		if !b.eventFinishing[p.task] {
			for s := range h.subs {
				if s.claimPopup && !s.claimed && s.err == nil {
					s.claimed = true
					popup.owned = true
				}
			}
		}
		if popup.owned {
			if b.pendingPopups[p.task] == nil {
				b.pendingPopups[p.task] = make(map[string]*Popup)
			}
			b.pendingPopups[p.task][popup.ID] = popup
		}
		h.publishLocked(Event{Kind: PopupOpened, FrameID: string(info.OpenerFrameID), Popup: popup})
	case *target.EventTargetDestroyed:
		if p := b.eventPages[string(e.TargetID)]; p != nil {
			p.events.close(ErrPageUnavailable)
		}
	case *cdpbrowser.EventDownloadWillBegin:
		b.downloadStartedLocked(e)
	case *cdpbrowser.EventDownloadProgress:
		if d := b.downloads[e.GUID]; d != nil {
			d.progress(e)
			if string(e.State) != "inProgress" {
				delete(b.downloads, e.GUID)
			}
		}
	}
}

func (p *Page) onTargetEvent(session *chromedp.Target, event any) {
	if session == nil {
		return
	}
	h := p.events
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return
	}
	sid := string(session.SessionID)
	switch e := event.(type) {
	case *cdppage.EventJavascriptDialogOpening:
		h.serial++
		d := &Dialog{page: p, session: session, serial: h.serial, Info: DialogInfo{Type: string(e.Type), Message: e.Message, DefaultPrompt: e.DefaultPrompt, URL: e.URL, FrameID: string(e.FrameID)}}
		h.dialog = d
		if h.active != nil {
			h.active(ErrDialogOpen)
		}
		h.publishLocked(Event{Kind: DialogOpened, FrameID: string(e.FrameID), Dialog: d})
	case *cdppage.EventJavascriptDialogClosed:
		if h.dialog != nil && h.dialog.session == session {
			h.dialog = nil
		}
	case *cdppage.EventFileChooserOpened:
		chooser := &FileChooser{page: p, session: session, backend: e.BackendNodeID, FrameID: string(e.FrameID), frame: e.FrameID, document: h.documents[e.FrameID], Multiple: string(e.Mode) == "selectMultiple", epoch: h.documentEpoch}
		h.publishLocked(Event{Kind: FileChooserOpened, FrameID: chooser.FrameID, FileChooser: chooser})
	case *cdppage.EventFrameNavigated:
		h.documentEpoch++
		h.notifyLocked()
	case *cdppage.EventFrameAttached:
		h.epoch++
		h.notifyLocked()
	case *cdppage.EventFrameDetached:
		for key, frame := range h.requestFrames {
			if frame == e.FrameID {
				delete(h.requests, key)
				delete(h.requestFrames, key)
			}
		}
		h.epoch++
		h.documentEpoch++
		h.notifyLocked()
	case *network.EventRequestWillBeSent:
		key := sid + "/" + string(e.RequestID)
		if e.Type != network.ResourceTypeWebSocket && e.Type != network.ResourceTypeEventSource {
			if len(h.requests) >= 4096 && !h.requests[key] {
				h.networkErr = ErrEventOverflow
			} else {
				h.requests[key] = true
				h.requestFrames[key] = e.FrameID
			}
		} else {
			delete(h.requests, key)
			delete(h.requestFrames, key)
		}
		h.notifyLocked()
		r := RequestInfo{ID: string(e.RequestID), SessionID: sid, ResourceType: string(e.Type)}
		if e.Request != nil {
			r.URL = e.Request.URL
			r.Method = e.Request.Method
		}
		h.publishLocked(Event{Kind: RequestStarted, FrameID: string(e.FrameID), Request: r})
	case *network.EventLoadingFinished:
		delete(h.requests, sid+"/"+string(e.RequestID))
		delete(h.requestFrames, sid+"/"+string(e.RequestID))
		h.notifyLocked()
		h.publishLocked(Event{Kind: RequestFinished, Request: RequestInfo{ID: string(e.RequestID), SessionID: sid}})
	case *network.EventLoadingFailed:
		delete(h.requests, sid+"/"+string(e.RequestID))
		delete(h.requestFrames, sid+"/"+string(e.RequestID))
		h.notifyLocked()
		h.publishLocked(Event{Kind: RequestFailed, Request: RequestInfo{ID: string(e.RequestID), SessionID: sid, Error: e.ErrorText}})
	}
}

func (p *Page) updateEventFrames(frames map[cdp.FrameID]*frameState) {
	b := p.task.browser
	b.eventsMu.Lock()
	defer b.eventsMu.Unlock()
	for id, owner := range b.eventFrames {
		if owner == p && frames[id] == nil {
			delete(b.eventFrames, id)
		}
	}
	for id := range frames {
		b.eventFrames[id] = p
	}
	p.events.mu.Lock()
	clear(p.events.documents)
	for id, state := range frames {
		p.events.documents[id] = state.doc.key
	}
	p.events.mu.Unlock()
}
func (h *eventHub) sessionAdded(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.sessions[id] {
		h.sessions[id] = true
		h.epoch++
		h.notifyLocked()
	}
}

func (h *eventHub) sessionRemoved(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, id)
	for key := range h.requests {
		if strings.HasPrefix(key, id+"/") {
			delete(h.requests, key)
			delete(h.requestFrames, key)
		}
	}
	h.epoch++
	h.notifyLocked()
}

// NetworkIdleWait observes requests from already attached page/OOPIF sessions.
// It fails conservatively if the frame tree changes while waiting. It is not
// proof of application readiness or of requests predating attachment.
type NetworkIdleWait struct {
	page  *Page
	epoch uint64
	armed time.Time
	idle  time.Duration
}

func (p *Page) ExpectNetworkIdle(ctx context.Context, idle time.Duration) (*NetworkIdleWait, error) {
	if idle < 0 {
		return nil, errors.New("negative idle duration")
	}
	if idle == 0 {
		idle = 500 * time.Millisecond
	}
	_, err := p.Frames(ctx)
	if err != nil {
		return nil, err
	}
	h := p.events
	h.mu.Lock()
	defer h.mu.Unlock()
	return &NetworkIdleWait{page: p, epoch: h.epoch, armed: time.Now(), idle: idle}, nil
}

func (w *NetworkIdleWait) Wait(ctx context.Context) error {
	h := w.page.events
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h.mu.Lock()
		err := h.err
		if err == nil {
			err = h.networkErr
		}
		if err == nil && h.epoch != w.epoch {
			err = ErrNetworkTrackingChanged
		}
		last := h.lastActivity
		if last.Before(w.armed) {
			last = w.armed
		}
		remaining := time.Until(last.Add(w.idle))
		empty := len(h.requests) == 0
		changed := h.changed
		h.mu.Unlock()
		if err != nil {
			return err
		}
		if empty && remaining <= 0 {
			return nil
		}
		if !empty || remaining <= 0 {
			remaining = w.idle
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (p *Page) WaitForNetworkIdle(ctx context.Context, idle time.Duration) error {
	w, err := p.ExpectNetworkIdle(ctx, idle)
	if err != nil {
		return err
	}
	return w.Wait(ctx)
}
