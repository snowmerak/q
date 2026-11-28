package cli

import (
	"context"
	"errors"
	"fmt"

	qdriver "github.com/snowmerak/q/v2/pkg/cdp"
)

func (s *service) arm(ctx context.Context, p *managedPage, kind qdriver.EventKind) (any, error) {
	s.handlesMu.Lock()
	full := len(s.subscriptions) >= maxHandles
	s.handlesMu.Unlock()
	if full {
		return nil, qdriver.ErrEventOverflow
	}
	life, cancel := context.WithCancel(s.eventsCtx)
	stop := context.AfterFunc(ctx, cancel)
	var sub *qdriver.Subscription
	var err error
	switch kind {
	case qdriver.PopupOpened:
		sub, err = p.page.ExpectPopup(life)
	case qdriver.DialogOpened:
		sub, err = p.page.ExpectDialog(life)
	case qdriver.DownloadStarted:
		sub, err = p.page.ExpectDownload(life)
	default:
		sub, err = p.page.Subscribe(life, kind)
	}
	stop()
	if err = errors.Join(err, ctx.Err()); err != nil {
		cancel()
		if sub != nil {
			sub.Close()
		}
		return nil, err
	}
	s.handlesMu.Lock()
	s.nextHandle++
	id := fmt.Sprintf("s%s.%d", s.instance, s.nextHandle)
	s.subscriptions[id] = subscriptionEntry{sub, p, cancel}
	s.handlesMu.Unlock()
	return map[string]string{"subscription": id, "kind": string(kind)}, nil
}

func (s *service) storeHandle(value any, p *managedPage) (string, error) {
	s.handlesMu.Lock()
	defer s.handlesMu.Unlock()
	return s.storeHandleLocked(value, p)
}
func (s *service) storeHandleLocked(value any, p *managedPage) (string, error) {
	if id := s.identities[value]; id != "" {
		return id, nil
	}
	if len(s.handles) >= maxHandles {
		return "", qdriver.ErrEventOverflow
	}
	s.nextHandle++
	id := fmt.Sprintf("h%s.%d", s.instance, s.nextHandle)
	s.handles[id] = handleEntry{value, p}
	s.identities[value] = id
	return id, nil
}
func (s *service) handle(id string) (handleEntry, error) {
	s.handlesMu.Lock()
	defer s.handlesMu.Unlock()
	h, ok := s.handles[id]
	if !ok {
		return h, missing("handle", id)
	}
	return h, nil
}
func (s *service) forgetHandle(id string) {
	s.handlesMu.Lock()
	defer s.handlesMu.Unlock()
	if h, ok := s.handles[id]; ok {
		delete(s.identities, h.value)
		delete(s.handles, id)
	}
}

func (s *service) nextEvent(ctx context.Context, id string) (any, error) {
	s.handlesMu.Lock()
	entry, ok := s.subscriptions[id]
	s.handlesMu.Unlock()
	if !ok {
		return nil, missing("subscription", id)
	}
	e, err := entry.sub.Next(ctx)
	if err != nil {
		// A timeout ends this wait only. Terminal subscriptions release their
		// retained lifetime; SDK connection errors remain visible to the caller.
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			s.handlesMu.Lock()
			delete(s.subscriptions, id)
			s.handlesMu.Unlock()
			entry.sub.Close()
			entry.cancel()
		}
		return nil, err
	}
	result := map[string]any{"kind": e.Kind, "pageId": e.PageID, "frameId": e.FrameID}
	var value any
	switch e.Kind {
	case qdriver.PopupOpened:
		value = e.Popup
		result["popup"] = map[string]string{"id": e.Popup.ID, "url": e.Popup.URL}
	case qdriver.DialogOpened:
		value = e.Dialog
		result["dialog"] = e.Dialog.Info
	case qdriver.FileChooserOpened:
		value = e.FileChooser
		result["chooser"] = map[string]any{"frameId": e.FileChooser.FrameID, "multiple": e.FileChooser.Multiple}
	case qdriver.DownloadStarted:
		value = e.Download
		result["download"] = e.Download.Info()
	default:
		result["request"] = e.Request
	}
	s.handlesMu.Lock()
	defer s.handlesMu.Unlock()
	if _, exists := s.subscriptions[id]; !exists {
		return nil, qdriver.ErrSubscriptionClosed
	}
	if value != nil {
		handle, err := s.storeHandleLocked(value, entry.page)
		if err != nil {
			return nil, err
		}
		result["handle"] = handle
	}
	return result, nil
}

func (s *service) adoptPopup(ctx context.Context, r request) (any, error) {
	h, err := s.handle(r.ID)
	if err != nil {
		return nil, err
	}
	popup, ok := h.value.(*qdriver.Popup)
	if !ok {
		return nil, invalid("handle is not a popup")
	}
	for _, p := range s.pages {
		if p.page.ID() == popup.ID {
			return p.view(), nil
		}
	}
	name, err := s.pageName(r.Name)
	if err != nil {
		return nil, err
	}
	page, err := popup.Page(ctx)
	if page == nil {
		return nil, err
	}
	entry := s.register(page, h.page.task, name)
	s.forgetHandle(r.ID)
	return entry.view(), err
}

func (s *service) handleCommand(ctx context.Context, r request) (any, error) {
	h, err := s.handle(r.ID)
	if err != nil {
		return nil, err
	}
	if r.Command == "handle.close" {
		s.forgetHandle(r.ID)
		return map[string]bool{"forgotten": true}, nil
	}
	var result any
	switch r.Command {
	case "dialog.accept", "dialog.dismiss":
		d, ok := h.value.(*qdriver.Dialog)
		if !ok {
			return nil, invalid("handle is not a dialog")
		}
		if r.Command == "dialog.accept" {
			err = d.Accept(ctx, r.Value)
		} else {
			err = d.Dismiss(ctx)
		}
	case "chooser.set", "chooser.cancel":
		f, ok := h.value.(*qdriver.FileChooser)
		if !ok {
			return nil, invalid("handle is not a file chooser")
		}
		if r.Command == "chooser.set" {
			result, err = f.SetFiles(ctx, r.Paths...)
		} else {
			err = f.Cancel(ctx)
		}
	case "download.cancel":
		d, ok := h.value.(*qdriver.Download)
		if !ok {
			return nil, invalid("handle is not a download")
		}
		err = d.Cancel(ctx)
	default:
		return nil, invalid("unknown handle operation")
	}
	if err == nil && r.Command != "download.cancel" {
		s.forgetHandle(r.ID)
	}
	if result == nil && err == nil {
		result = map[string]bool{"handled": true}
	}
	return result, err
}
