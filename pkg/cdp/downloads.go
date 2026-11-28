package qdriver

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
)

// EnableDownloads changes the default browser context's download policy to
// allowAndName and enables download events. Use a dedicated browser context:
// this affects other clients too, and Close restores default behavior, not a
// previous client's unknown policy. Chrome and Go must share the directory.
// Downloads use GUID filenames; suggested names never become filesystem paths.
func (b *Browser) EnableDownloads(ctx context.Context, directory string) error {
	if !filepath.IsAbs(directory) {
		return errors.New("download directory must be absolute")
	}
	op, done := b.operationContext(ctx, b.ctx)
	defer done()
	if err := b.lock(op); err != nil {
		return err
	}
	defer b.unlock()
	if err := b.connectionError(); err != nil {
		return err
	}
	b.eventsMu.Lock()
	active := len(b.downloads) > 0
	b.eventsMu.Unlock()
	if active {
		return errors.New("cannot change directory while downloads are active")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	directory = filepath.Clean(directory)
	// Publish before dispatch because downloadWillBegin may arrive before the
	// command acknowledgement. Keep it on an uncertain failure for cleanup.
	b.eventsMu.Lock()
	b.downloadDir = directory
	b.eventsMu.Unlock()
	_, err := cdp.Call(op, b.session(), cdpbrowser.SetDownloadBehavior, cdpbrowser.SetDownloadBehaviorParams{Behavior: cdpbrowser.SetDownloadBehaviorBehaviorAllowAndName, DownloadPath: directory, EventsEnabled: ptr(true)})
	return b.operationError(ctx, "enable downloads", "", true, err)
}

func (p *Page) ExpectDownload(ctx context.Context) (*Subscription, error) {
	b := p.task.browser
	b.eventsMu.Lock()
	enabled := b.downloadDir != ""
	b.eventsMu.Unlock()
	if !enabled {
		return nil, ErrDownloadsDisabled
	}
	return p.Subscribe(ctx, DownloadStarted)
}

type DownloadInfo struct {
	GUID, URL, SuggestedFilename, FrameID, State string
	ReceivedBytes, TotalBytes                    float64
}

// Download tracks one browser-reported GUID. All downloads from managed frames
// after EnableDownloads are task resources: active ones are canceled on page
// release/finish. Completed files remain in the configured directory.
type Download struct {
	mu       sync.Mutex
	page     *Page
	info     DownloadInfo
	path     string
	done     chan struct{}
	err      error
	finished bool
}

func (d *Download) Info() DownloadInfo { d.mu.Lock(); defer d.mu.Unlock(); return d.info }
func (d *Download) finish(err error)   { d.mu.Lock(); defer d.mu.Unlock(); d.finishLocked(err) }
func (d *Download) finishLocked(err error) {
	if !d.finished {
		d.finished = true
		d.err = err
		close(d.done)
	}
}
func (d *Download) progress(e *cdpbrowser.EventDownloadProgress) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.finished {
		return
	}
	d.info.State = string(e.State)
	d.info.TotalBytes = e.TotalBytes
	d.info.ReceivedBytes = e.ReceivedBytes
	switch string(e.State) {
	case "completed":
		d.finishLocked(nil)
	case "canceled":
		d.finishLocked(ErrDownloadCanceled)
	}
}

func (b *Browser) downloadStartedLocked(e *cdpbrowser.EventDownloadWillBegin) {
	p := b.eventFrames[e.FrameID]
	if p == nil || b.downloadDir == "" {
		return
	}
	// Bound retained in-flight state and never use an untrusted GUID as a path.
	if e.GUID == "" || strings.ContainsAny(e.GUID, `/\:`) || e.GUID == "." || e.GUID == ".." || len(b.downloads) >= 4096 {
		p.events.close(ErrEventOverflow)
		return
	}
	d := &Download{page: p, info: DownloadInfo{GUID: e.GUID, URL: e.URL, SuggestedFilename: e.SuggestedFilename, FrameID: string(e.FrameID), State: "inProgress"}, path: filepath.Join(b.downloadDir, e.GUID), done: make(chan struct{})}
	b.downloads[e.GUID] = d
	p.events.publish(Event{Kind: DownloadStarted, FrameID: string(e.FrameID), Download: d})
}

// Wait returns the GUID-named path only after Chrome reports completion. The
// file may still be unavailable on a remote/nonshared filesystem; SaveAs checks
// it. A canceled or failed transfer returns ErrDownloadCanceled.
func (d *Download) Wait(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-d.done:
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return "", d.err
	}
	return d.path, nil
}
func (d *Download) Cancel(ctx context.Context) error {
	b := d.page.task.browser
	op, done := b.operationContext(ctx, b.ctx)
	defer done()
	if err := b.lock(op); err != nil {
		return err
	}
	defer b.unlock()
	d.mu.Lock()
	finished := d.finished
	d.mu.Unlock()
	if finished {
		return nil
	}
	_, err := cdp.Call(op, b.session(), cdpbrowser.CancelDownload, cdpbrowser.CancelDownloadParams{GUID: d.Info().GUID})
	return b.operationError(ctx, "cancel download", d.page.id, true, err)
}

// SaveAs copies a completed file to an absolute destination using exclusive
// creation: existing files are never overwritten. It does not trust the
// suggested filename. Parent directories must already exist.
func (d *Download) SaveAs(ctx context.Context, destination string) (resultErr error) {
	if !filepath.IsAbs(destination) {
		return errors.New("download destination must be absolute")
	}
	source, err := d.Wait(ctx)
	if err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, in.Close()) }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("download is not a regular file")
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		resultErr = errors.Join(resultErr, out.Close())
		if !complete || resultErr != nil {
			resultErr = errors.Join(resultErr, os.Remove(destination))
		}
	}()
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := in.Read(buffer)
		if n > 0 {
			if _, err := out.Write(buffer[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			complete = true
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (p *Page) cancelDownloadsLocked(ctx context.Context) error {
	b := p.task.browser
	b.eventsMu.Lock()
	var active []*Download
	for _, d := range b.downloads {
		if d.page == p {
			active = append(active, d)
		}
	}
	b.eventsMu.Unlock()
	var result error
	for _, d := range active {
		_, err := cdp.Call(ctx, b.session(), cdpbrowser.CancelDownload, cdpbrowser.CancelDownloadParams{GUID: d.Info().GUID})
		result = errors.Join(result, err)
		d.finish(ErrPageReleased)
	}
	return result
}
func (b *Browser) closeDownloads(ctx context.Context) {
	b.eventsMu.Lock()
	enabled := b.downloadDir != ""
	b.eventsMu.Unlock()
	if enabled {
		_, err := cdp.Call(ctx, b.session(), cdpbrowser.SetDownloadBehavior, cdpbrowser.SetDownloadBehaviorParams{Behavior: cdpbrowser.SetDownloadBehaviorBehaviorDefault, EventsEnabled: ptr(false)})
		b.closeErr = errors.Join(b.closeErr, err)
	}
}
