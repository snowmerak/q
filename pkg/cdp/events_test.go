package qdriver_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	qdriver "github.com/snowmerak/q/v2/pkg/cdp"
)

func nextEvent(t *testing.T, s *qdriver.Subscription) qdriver.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	e, err := s.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestPopupOwnershipAndIndependentSubscriptions(t *testing.T) {
	f := startChrome(t)
	b := attachDriver(t, f.endpoint)
	p := interactionPage(t, b, `<!doctype html><button id="open" onclick="window.open('/next','_blank')">Open</button>`)
	claim, err := p.ExpectPopup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Close()
	observe, err := p.Subscribe(t.Context(), qdriver.PopupOpened)
	if err != nil {
		t.Fatal(err)
	}
	defer observe.Close()
	if _, err := p.Click(t.Context(), qdriver.CSS("#open")); err != nil {
		t.Fatal(err)
	}
	e, other := nextEvent(t, claim), nextEvent(t, observe)
	if e.Popup == nil || other.Popup == nil || e.Popup.ID != other.Popup.ID || e.PageID != p.ID() {
		t.Fatalf("popup routing: %+v %+v", e, other)
	}
	popup, err := e.Popup.Page(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !popup.Owned() {
		t.Fatal("expected popup was not owned")
	}
	if err := popup.WaitForFunction(t.Context(), `document.title==='Next document'`); err != nil {
		t.Fatal(err)
	}
	again, err := other.Popup.Page(t.Context())
	if err != nil || again != popup {
		t.Fatalf("duplicate popup handles: %p %p %v", popup, again, err)
	}
	if err := popup.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.pageIDs(t), popup.ID()) {
		t.Fatal("popup remained open")
	}
	// A prepared claim must be cleaned even when the caller never attaches.
	claim2, err := p.ExpectPopup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer claim2.Close()
	if _, err := p.Click(t.Context(), qdriver.CSS("#open")); err != nil {
		t.Fatal(err)
	}
	unopened := nextEvent(t, claim2).Popup
	if _, err := newTask(t, b).Adopt(t.Context(), unopened.ID); !errors.Is(err, qdriver.ErrPageInUse) {
		t.Fatalf("claimed popup ownership was downgraded: %v", err)
	}
	if _, err := b.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.pageIDs(t), unopened.ID) {
		t.Fatal("unconsumed expected popup leaked")
	}
}

func TestUnconsumedPopupEventCleanup(t *testing.T) {
	f := startChrome(t)
	b := attachDriver(t, f.endpoint)
	baseline := f.pageIDs(t)
	task := newTask(t, b)
	p := newPage(t, task)
	if err := p.Goto(t.Context(), `data:text/html,<button id="open" onclick="window.open('about:blank','_blank')">Open</button>`); err != nil {
		t.Fatal(err)
	}
	s, err := p.ExpectPopup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := p.Click(t.Context(), qdriver.CSS("#open")); err != nil {
		t.Fatal(err)
	}
	// Do not call Next or Popup.Page: cleanup must use observed ownership,
	// rather than relying on the user consuming the event.
	result, err := task.Finish(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Closed) != 2 {
		t.Fatalf("unconsumed popup missing from cleanup: %+v", result)
	}
	if got := f.pageIDs(t); !slices.Equal(got, baseline) {
		t.Fatalf("unconsumed popup leaked: %v", got)
	}
}

func TestDialogInterruptsRendererAndPreservesSession(t *testing.T) {
	f := startChrome(t)
	b := attachDriver(t, f.endpoint)
	p := interactionPage(t, b, `<!doctype html><button id="prompt" onclick="window.answer=prompt('Name?','initial')">Prompt</button><button id="confirm" onclick="window.confirmed=confirm('Continue?')">Confirm</button><button id="alert" onclick="alert('hello')">Alert</button>`)
	for _, tc := range []struct {
		selector, kind, message string
		accept                  bool
		want                    string
	}{
		{"#prompt", "prompt", "Name?", true, `window.answer==='Ada 안녕'`},
		{"#confirm", "confirm", "Continue?", false, `window.confirmed===false`},
		{"#alert", "alert", "hello", true, `true`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			s, err := p.ExpectDialog(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			started := time.Now()
			receipt, clickErr := p.Click(t.Context(), qdriver.CSS(tc.selector))
			if clickErr != nil && !errors.Is(clickErr, qdriver.ErrDialogOpen) {
				t.Fatalf("dialog click: %+v %v", receipt, clickErr)
			}
			if !receipt.InputStarted || time.Since(started) > 3*time.Second {
				t.Fatalf("dialog blocked operation gate: %+v %v", receipt, clickErr)
			}
			e := nextEvent(t, s)
			d := e.Dialog
			if d == nil || d.Info.Type != tc.kind || d.Info.Message != tc.message {
				t.Fatalf("dialog payload: %+v", e)
			}
			if _, err := p.Title(t.Context()); !errors.Is(err, qdriver.ErrDialogOpen) {
				t.Fatalf("renderer call while modal: %v", err)
			}
			if tc.accept {
				err = d.Accept(t.Context(), "Ada 안녕")
			} else {
				err = d.Dismiss(t.Context())
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := d.Dismiss(t.Context()); !errors.Is(err, qdriver.ErrDialogStale) {
				t.Fatalf("old dialog acted on a new one: %v", err)
			}
			if err := p.WaitForFunction(t.Context(), tc.want); err != nil {
				t.Fatal(err)
			}
		})
	}
	// Runtime.evaluate itself stalls on a modal; its cancellation must not
	// cancel the tab's lifetime event reader.
	s, err := p.ExpectDialog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := p.Evaluate(t.Context(), `alert('evaluate'); window.afterDialog=1`); !errors.Is(err, qdriver.ErrDialogOpen) {
		t.Fatalf("evaluate dialog: %v", err)
	}
	if err := nextEvent(t, s).Dialog.Accept(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitForFunction(t.Context(), `window.afterDialog===1`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Evaluate(t.Context(), `alert('first');alert('second');window.chained=true`); !errors.Is(err, qdriver.ErrDialogOpen) {
		t.Fatal(err)
	}
	first := nextEvent(t, s).Dialog
	if err := first.Accept(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	second := nextEvent(t, s).Dialog
	if err := first.Accept(t.Context(), ""); !errors.Is(err, qdriver.ErrDialogStale) {
		t.Fatalf("old handle accepted chained dialog: %v", err)
	}
	if err := second.Dismiss(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitForFunction(t.Context(), `window.chained===true`); err != nil {
		t.Fatal(err)
	}
}

func TestFileInputsAndChooser(t *testing.T) {
	f := startChrome(t)
	b := attachDriver(t, f.endpoint)
	p := interactionPage(t, b, `<!doctype html><input hidden type="file" id="hidden" multiple onchange="window.changes=(window.changes||0)+1"><input type="file" id="choose"><div id="host"></div><script>host.attachShadow({mode:'open'}).innerHTML='<input type="file" id="shadow">'</script>`)
	dir := t.TempDir()
	one, two := filepath.Join(dir, "한글 one.txt"), filepath.Join(dir, "two.txt")
	for _, file := range []string{one, two} {
		if err := os.WriteFile(file, []byte("upload content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if receipt, err := p.SetInputFiles(t.Context(), qdriver.CSS("#hidden"), one, two); err != nil || !receipt.InputCompleted {
		t.Fatalf("hidden multiple upload: %+v %v", receipt, err)
	}
	evaluateJSON(t, p, `[...document.querySelector('#hidden').files].map(f=>f.name)`, `["한글 one.txt","two.txt"]`)
	if _, err := p.SetInputFiles(t.Context(), qdriver.CSS("#shadow"), one); err != nil {
		t.Fatal(err)
	}
	evaluateJSON(t, p, `host.shadowRoot.querySelector('input').files[0].name`, `"한글 one.txt"`)
	if _, err := p.SetInputFiles(t.Context(), qdriver.CSS("#hidden")); err != nil {
		t.Fatal(err)
	}
	evaluateJSON(t, p, `document.querySelector('#hidden').files.length`, `0`)
	for _, paths := range [][]string{{"relative.txt"}, {dir}, {filepath.Join(dir, "missing")}, {one, two}} {
		if _, err := p.SetInputFiles(t.Context(), qdriver.CSS("#choose"), paths...); err == nil {
			t.Fatalf("accepted invalid files: %v", paths)
		}
	}
	if err := p.SetFileChooserInterception(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.SetFileChooserInterception(context.Background(), false); err != nil {
			t.Error(err)
		}
	}()
	s, err := p.Subscribe(t.Context(), qdriver.FileChooserOpened)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := p.Click(t.Context(), qdriver.CSS("#choose")); err != nil {
		t.Fatal(err)
	}
	chooser := nextEvent(t, s).FileChooser
	if chooser == nil || chooser.Multiple {
		t.Fatalf("chooser: %+v", chooser)
	}
	if receipt, err := chooser.SetFiles(t.Context(), one); err != nil || !receipt.InputCompleted {
		t.Fatalf("chooser upload: %+v %v", receipt, err)
	}
	if err := chooser.Cancel(t.Context()); !errors.Is(err, qdriver.ErrStaleRef) {
		t.Fatalf("chooser reused: %v", err)
	}
	evaluateJSON(t, p, `document.querySelector('#choose').files[0].name`, `"한글 one.txt"`)
	if _, err := p.Click(t.Context(), qdriver.CSS("#choose")); err != nil {
		t.Fatal(err)
	}
	if err := nextEvent(t, s).FileChooser.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	evaluateJSON(t, p, `document.querySelector('#choose').files.length`, `0`)
	if _, err := p.Click(t.Context(), qdriver.CSS("#choose")); err != nil {
		t.Fatal(err)
	}
	stale := nextEvent(t, s).FileChooser
	if _, err := p.Evaluate(t.Context(), `location.reload()`); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitForSelector(t.Context(), qdriver.CSS("#choose")); err != nil {
		t.Fatal(err)
	}
	if _, err := stale.SetFiles(t.Context(), one); !errors.Is(err, qdriver.ErrStaleRef) {
		t.Fatalf("chooser survived navigation: %v", err)
	}
}

func TestDownloadsCompleteCancelAndSaveAs(t *testing.T) {
	f := startChrome(t)
	b := attachDriver(t, f.endpoint)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, `<!doctype html><a id="download" href="/file">Download</a><a id="slow" href="/slow">Slow</a>`)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="../untrusted.txt"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		if r.URL.Path == "/slow" {
			w.Header().Set("Content-Length", "10000000")
			_, _ = fmt.Fprint(w, strings.Repeat("x", 64*1024))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = fmt.Fprint(w, "download content 안녕")
	}))
	t.Cleanup(func() { server.CloseClientConnections(); server.Close() })
	task := newTask(t, b)
	p := newPage(t, task)
	if err := p.Goto(t.Context(), server.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ExpectDownload(t.Context()); !errors.Is(err, qdriver.ErrDownloadsDisabled) {
		t.Fatalf("implicit browser policy change: %v", err)
	}
	dir := t.TempDir()
	if err := b.EnableDownloads(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	s, err := p.ExpectDownload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := p.Click(t.Context(), qdriver.CSS("#download")); err != nil {
		t.Fatal(err)
	}
	d := nextEvent(t, s).Download
	if d == nil {
		t.Fatal("missing download handle")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	path, err := d.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || filepath.Base(path) != d.Info().GUID {
		t.Fatalf("unsafe download path: %s", path)
	}
	destination := filepath.Join(t.TempDir(), "saved.txt")
	if err := d.SaveAs(ctx, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "download content 안녕" {
		t.Fatalf("download content: %q %v", data, err)
	}
	if err := d.SaveAs(ctx, destination); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrote destination: %v", err)
	}
	if _, err := p.Click(t.Context(), qdriver.CSS("#slow")); err != nil {
		t.Fatal(err)
	}
	slow := nextEvent(t, s).Download
	short, stop := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer stop()
	if _, err := slow.Wait(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unfinished download: %v", err)
	}
	if err := slow.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := slow.Wait(ctx); !errors.Is(err, qdriver.ErrDownloadCanceled) {
		t.Fatalf("canceled download: %v", err)
	}
	// Cleanup cancels active transfers and terminates pending event waits.
	if _, err := p.Click(t.Context(), qdriver.CSS("#slow")); err != nil {
		t.Fatal(err)
	}
	active := nextEvent(t, s).Download
	if _, err := task.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := active.Wait(ctx); err == nil {
		t.Fatal("cleanup reported unfinished transfer as complete")
	}
	if _, err := s.Next(ctx); !errors.Is(err, qdriver.ErrPageReleased) && !errors.Is(err, qdriver.ErrPageUnavailable) {
		t.Fatalf("subscription remained after finish: %v", err)
	}
}

func TestEventWaitNetworkIdleOverflowAndCancellation(t *testing.T) {
	f := startChrome(t)
	b, err := qdriver.Attach(t.Context(), f.endpoint, qdriver.Options{EventBuffer: 1, MaxSubscriptions: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := b.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	started, release := make(chan struct{}, 1), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
			}
			_, _ = fmt.Fprint(w, "done")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		// Automatic favicon traffic must not compete for the one-event budget.
		_, _ = fmt.Fprint(w, `<!doctype html><link rel="icon" href="data:,"><button id="fetch" onclick="fetch('/slow')">Fetch</button><button id="burst" onclick="fetch('/a');fetch('/b');fetch('/c')">Burst</button>`)
	}))
	defer func() {
		close(release) // Unblock slow handlers even if an earlier assertion fails.
		server.Close()
	}()
	p := newPage(t, newTask(t, b))
	if err := p.Goto(t.Context(), server.URL); err != nil {
		t.Fatal(err)
	}
	s, err := p.Subscribe(t.Context(), qdriver.RequestStarted)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	idle, err := p.ExpectNetworkIdle(t.Context(), 80*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(t.Context(), qdriver.CSS("#fetch")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	e := nextEvent(t, s)
	if !strings.HasSuffix(e.Request.URL, "/slow") {
		t.Fatalf("request event: %+v", e)
	}
	short, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	if err := idle.Wait(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("idle while request active: %v", err)
	}
	// This independent observer can overflow without breaking internal idle.
	if _, err := p.Click(t.Context(), qdriver.CSS("#burst")); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitForFunction(t.Context(), `true`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Next(t.Context()); !errors.Is(err, qdriver.ErrEventOverflow) {
		t.Fatalf("event loss hidden: %v", err)
	}
	release <- struct{}{}
	ctx, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	if err := idle.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	canceled, cancelSub := context.WithCancel(t.Context())
	wait, err := p.Subscribe(canceled, qdriver.PopupOpened)
	if err != nil {
		t.Fatal(err)
	}
	cancelSub()
	if _, err := wait.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled subscription: %v", err)
	}
	wait.Close()
	one, err := p.Subscribe(t.Context(), qdriver.PopupOpened)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	if _, err := b.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := one.Next(ctx); !errors.Is(err, qdriver.ErrBrowserClosed) {
		t.Fatalf("close did not wake event wait: %v", err)
	}
}

func TestOOPIFEventsAndFiles(t *testing.T) {
	f := startFrameChrome(t)
	b := attachDriver(t, f.endpoint)
	var remoteOrigin string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/file" {
			w.Header().Set("Content-Disposition", `attachment; filename="remote.txt"`)
			_, _ = fmt.Fprint(w, "OOPIF download")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			_, _ = fmt.Fprintf(w, `<!doctype html><iframe name="remote" src="%s/remote" style="height:220px;width:600px"></iframe><iframe name="local" src="/local"></iframe>`, remoteOrigin)
		case "/remote":
			_, _ = fmt.Fprint(w, `<!doctype html><input type="file" id="upload"><button id="popup" onclick="window.open('/child','_blank')">Popup</button><button id="request" onclick="fetch('/missing')">Request</button><a id="download" href="/file">Download</a>`)
		case "/local":
			_, _ = fmt.Fprint(w, `<!doctype html><button id="local-dialog" onclick="alert('local frame')">Dialog</button>`)
		case "/child":
			_, _ = fmt.Fprint(w, `<!doctype html><title>Remote popup</title>`)
		case "/missing":
			http.Error(w, "missing", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	remoteOrigin = "http://site-b.test:" + u.Port()
	p := newPage(t, newTask(t, b))
	if err := p.Goto(t.Context(), "http://site-a.test:"+u.Port()); err != nil {
		t.Fatal(err)
	}
	frame := frameByName(t, p, "remote")
	if !frame.OutOfProcess {
		t.Fatal("fixture did not create an OOPIF")
	}
	local := frameByName(t, p, "local")
	file := filepath.Join(t.TempDir(), "remote.txt")
	if err := os.WriteFile(file, []byte("OOPIF upload"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.SetInputFiles(t.Context(), qdriver.CSS("#upload").InFrame(frame.ID), file); err != nil {
		t.Fatal(err)
	}
	if err := p.SetFileChooserInterception(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	chooserSub, err := p.Subscribe(t.Context(), qdriver.FileChooserOpened)
	if err != nil {
		t.Fatal(err)
	}
	defer chooserSub.Close()
	if _, err := p.Click(t.Context(), qdriver.CSS("#upload").InFrame(frame.ID)); err != nil {
		t.Fatal(err)
	}
	chooserEvent := nextEvent(t, chooserSub)
	if chooserEvent.FrameID != frame.ID {
		t.Fatalf("wrong chooser frame: %+v", chooserEvent)
	}
	if _, err := chooserEvent.FileChooser.SetFiles(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	requestSub, err := p.Subscribe(t.Context(), qdriver.RequestStarted)
	if err != nil {
		t.Fatal(err)
	}
	defer requestSub.Close()
	idle, err := p.ExpectNetworkIdle(t.Context(), 60*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(t.Context(), qdriver.CSS("#request").InFrame(frame.ID)); err != nil {
		t.Fatal(err)
	}
	request := nextEvent(t, requestSub)
	if request.FrameID != frame.ID || !strings.HasSuffix(request.Request.URL, "/missing") || request.Request.SessionID == "" {
		t.Fatalf("OOPIF request routing: %+v", request)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	if err := idle.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	popupSub, err := p.ExpectPopup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer popupSub.Close()
	if _, err := p.Click(t.Context(), qdriver.CSS("#popup").InFrame(frame.ID)); err != nil {
		t.Fatal(err)
	}
	popup, err := nextEvent(t, popupSub).Popup.Page(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !popup.Owned() {
		t.Fatal("OOPIF opener did not confer expected ownership")
	}
	if err := popup.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := b.EnableDownloads(t.Context(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	downloadSub, err := p.ExpectDownload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer downloadSub.Close()
	if _, err := p.Click(t.Context(), qdriver.CSS("#download").InFrame(frame.ID)); err != nil {
		t.Fatal(err)
	}
	download := nextEvent(t, downloadSub)
	if download.FrameID != frame.ID {
		t.Fatalf("wrong download frame: %+v", download)
	}
	if _, err := download.Download.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	// Chromium suppresses JavaScript dialogs in cross-origin iframes. Test
	// supported same-origin iframe dialogs without disabling browser policy.
	dialogSub, err := p.ExpectDialog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer dialogSub.Close()
	if _, err := p.Click(t.Context(), qdriver.CSS("#local-dialog").InFrame(local.ID)); err != nil && !errors.Is(err, qdriver.ErrDialogOpen) {
		t.Fatal(err)
	}
	dialog := nextEvent(t, dialogSub)
	if dialog.FrameID != local.ID {
		t.Fatalf("wrong dialog frame: %+v", dialog)
	}
	if err := dialog.Dialog.Dismiss(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A changed frame tree invalidates an existing network-idle assertion.
	changed, err := p.ExpectNetworkIdle(t.Context(), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Evaluate(t.Context(), `document.querySelector('iframe').remove()`); err != nil {
		t.Fatal(err)
	}
	if err := changed.Wait(ctx); !errors.Is(err, qdriver.ErrNetworkTrackingChanged) {
		t.Fatalf("frame loss produced a false idle result: %v", err)
	}
}

func TestEventWaitConnectionLossAndSubscriptionCapacity(t *testing.T) {
	f := startChrome(t)
	b, err := qdriver.Attach(t.Context(), f.endpoint, qdriver.Options{MaxSubscriptions: 1})
	if err != nil {
		t.Fatal(err)
	}
	p := newPage(t, newTask(t, b))
	s, err := p.Subscribe(t.Context(), qdriver.RequestFailed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Subscribe(t.Context(), qdriver.PopupOpened); !errors.Is(err, qdriver.ErrEventOverflow) {
		t.Fatalf("subscription capacity: %v", err)
	}
	if _, err := p.Evaluate(t.Context(), `fetch('http://127.0.0.1:1/fail').catch(()=>{})`); err != nil {
		t.Fatal(err)
	}
	e := nextEvent(t, s)
	if e.Kind != qdriver.RequestFailed || e.Request.Error == "" {
		t.Fatalf("failed request: %+v", e)
	}
	s.Close()
	wait, err := p.Subscribe(t.Context(), qdriver.PopupOpened)
	if err != nil {
		t.Fatal(err)
	}
	defer wait.Close()
	// Kill only the process launched by this fixture. Graceful Browser.close
	// can destroy targets first, legitimately returning ErrPageUnavailable.
	if err := f.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := wait.Next(ctx); !errors.Is(err, qdriver.ErrConnectionLost) {
		t.Fatalf("lost connection wait: %v", err)
	}
	_, _ = b.Close(t.Context()) // The already-lost transport cannot confirm cleanup.
}

func TestReleaseBorrowedPageWithModalAndFileInterception(t *testing.T) {
	f := startChrome(t)
	baseline := f.pageIDs(t)
	b := attachDriver(t, f.endpoint)
	task := newTask(t, b)
	p, err := task.Adopt(t.Context(), baseline[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetFileChooserInterception(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	s, err := p.ExpectDialog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := p.Evaluate(t.Context(), `window.dismissed=confirm('borrowed')`); !errors.Is(err, qdriver.ErrDialogOpen) {
		t.Fatal(err)
	}
	nextEvent(t, s)
	if err := task.Release(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Next(t.Context()); !errors.Is(err, qdriver.ErrPageReleased) {
		t.Fatalf("release left subscription active: %v", err)
	}
	if !slices.Contains(f.pageIDs(t), p.ID()) {
		t.Fatal("release closed the borrowed tab")
	}
	reused, err := newTask(t, b).Adopt(t.Context(), p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := reused.WaitForFunction(t.Context(), `window.dismissed===false`); err != nil {
		t.Fatal(err)
	}
	if reused.CurrentDialog() != nil {
		t.Fatal("release left borrowed page modal")
	}
}

func TestEventsWorkflow(t *testing.T) {
	f := startChrome(t)
	baseline := f.pageIDs(t)
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file":
			w.Header().Set("Content-Disposition", `attachment; filename="report.txt"`)
			_, _ = fmt.Fprint(w, "report from Chrome")
		case "/popup":
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, `<!doctype html><title>Owned popup</title><h1>Popup ready</h1>`)
		case "/request":
			_, _ = fmt.Fprint(w, "done")
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, `<!doctype html><button id="popup" onclick="window.open('/popup','_blank')">Open</button><button id="prompt" onclick="window.answer=prompt('Your name?')">Prompt</button><input hidden type="file" id="upload"><a id="download" href="/file">Download</a><button id="request" onclick="fetch('/request')">Request</button>`)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	b, err := qdriver.Attach(ctx, f.endpoint, qdriver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = b.Close(context.Background()) }()

	task, err := b.NewTask(ctx, "events and files workflow")
	if err != nil {
		t.Fatal(err)
	}
	p, err := task.NewPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Goto(ctx, server.URL); err != nil {
		t.Fatal(err)
	}

	popups, err := p.ExpectPopup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer popups.Close()
	if _, err := p.Click(ctx, qdriver.CSS("#popup")); err != nil {
		t.Fatal(err)
	}
	opened, err := popups.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	popup, err := opened.Popup.Page(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := popup.WaitForSelector(ctx, qdriver.CSS("h1")); err != nil {
		t.Fatal(err)
	}
	popupTitle, err := popup.Title(ctx)
	if err != nil {
		t.Fatal(err)
	}

	dialogs, err := p.ExpectDialog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer dialogs.Close()
	if _, err := p.Click(ctx, qdriver.CSS("#prompt")); err != nil && !errors.Is(err, qdriver.ErrDialogOpen) {
		t.Fatal(err)
	}
	modal, err := dialogs.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := modal.Dialog.Accept(ctx, "Ada"); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitForFunction(ctx, `window.answer==='Ada'`); err != nil {
		t.Fatal(err)
	}

	upload := filepath.Join(dir, "upload.txt")
	if err := os.WriteFile(upload, []byte("upload from Go"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.SetInputFiles(ctx, qdriver.CSS("#upload"), upload); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitForFunction(ctx, `document.querySelector('#upload').files[0].name==='upload.txt'`); err != nil {
		t.Fatal(err)
	}

	if err := b.EnableDownloads(ctx, filepath.Join(dir, "downloads")); err != nil {
		t.Fatal(err)
	}
	downloads, err := p.ExpectDownload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer downloads.Close()
	if _, err := p.Click(ctx, qdriver.CSS("#download")); err != nil {
		t.Fatal(err)
	}
	transfer, err := downloads.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(dir, "saved.txt")
	if err := transfer.Download.SaveAs(ctx, saved); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}

	idle, err := p.ExpectNetworkIdle(ctx, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(ctx, qdriver.CSS("#request")); err != nil {
		t.Fatal(err)
	}
	if err := idle.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	cleanup, err := task.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if popupTitle != "Owned popup" || string(contents) != "report from Chrome" || len(cleanup.Closed) != 2 {
		t.Fatalf("events workflow: popup=%q download=%q cleanup=%+v", popupTitle, string(contents), cleanup)
	}
	if got := f.pageIDs(t); !slices.Equal(got, baseline) {
		t.Fatalf("events workflow changed external tabs: %v", got)
	}
}
