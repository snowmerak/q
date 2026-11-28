package qdriver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// Options bounds calls, cleanup, retained refs, frames, and event subscriptions.
// Zero values use the defaults.
type Options struct {
	CommandTimeout   time.Duration // Default: 15 seconds; includes queue time.
	CleanupTimeout   time.Duration // Default: 5 seconds for the entire cleanup.
	MaxRefs          int           // Default: 10,000 retained refs per page.
	MaxFrames        int           // Default: 128 reachable frames per page.
	EventBuffer      int           // Default: 64 events per subscription.
	MaxSubscriptions int           // Default: 128 subscriptions per page.
	InputTiming      *InputTiming  // Nil uses paced input; &InputTiming{} disables delays.
}

// Browser is a connection to an external browser. Operations are serialized
// per connection in this initial implementation; queueing respects contexts.
type Browser struct {
	ctx       context.Context
	cancel    context.CancelFunc
	rootStop  context.CancelFunc
	allocStop context.CancelFunc
	ops       context.Context
	stopOps   context.CancelFunc
	options   Options
	gate      chan struct{}
	closing   atomic.Bool
	closeOnce sync.Once
	closeErr  error
	closeInfo FinishResult
	// The gate protects all task/page state and chromedp context initialization.
	tasks          map[*Task]struct{}
	pages          map[string]*Page
	eventsMu       sync.Mutex // Event callbacks never acquire the operation gate.
	eventPages     map[string]*Page
	eventFrames    map[cdp.FrameID]*Page
	pendingPopups  map[*Task]map[string]*Popup
	eventFinishing map[*Task]bool
	downloadDir    string
	downloads      map[string]*Download
}

// PageInfo identifies a page target, including pages outside this SDK's tasks.
type PageInfo struct {
	ID    string
	URL   string
	Title string
}

// Attach connects to an explicit HTTP discovery endpoint or browser WebSocket
// URL. ctx bounds attachment only; use Close to end a successful connection.
// It does not launch a browser or attach to an arbitrary existing tab.
func Attach(ctx context.Context, endpoint string, options Options) (*Browser, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || !slices.Contains([]string{"http", "https", "ws", "wss"}, u.Scheme) {
		return nil, errors.New("qdriver attach: provide an absolute HTTP or browser WebSocket endpoint")
	}
	if options.CommandTimeout < 0 || options.CleanupTimeout < 0 || options.MaxRefs < 0 || options.MaxFrames < 0 || options.EventBuffer < 0 || options.MaxSubscriptions < 0 {
		return nil, errors.New("qdriver attach: timeouts and ref capacity must not be negative")
	}
	timing, err := inputTiming(options.InputTiming)
	if err != nil {
		return nil, err
	}
	options.InputTiming = &timing // Snapshot caller-owned settings for this connection.
	if options.CommandTimeout == 0 {
		options.CommandTimeout = 15 * time.Second
	}
	if options.CleanupTimeout == 0 {
		options.CleanupTimeout = 5 * time.Second
	}
	if options.MaxRefs == 0 {
		options.MaxRefs = 10_000
	}
	if options.MaxFrames == 0 {
		options.MaxFrames = 128
	}
	if options.EventBuffer == 0 {
		options.EventBuffer = 64
	}
	if options.MaxSubscriptions == 0 {
		options.MaxSubscriptions = 128
	}
	if err := ctx.Err(); err != nil {
		return nil, &OperationError{Operation: "attach", Err: err}
	}
	// Resolve discovery ourselves so HTTPS, paths, and the caller's deadline
	// retain their meaning rather than being rewritten by the remote allocator.
	attachCtx, attachCancel := context.WithTimeout(ctx, options.CommandTimeout)
	defer attachCancel()
	endpoint, err = browserEndpoint(attachCtx, u)
	if err != nil {
		return nil, &OperationError{Operation: "attach", Err: err}
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	alloc, allocStop := remote.NewAllocator(lifetime, endpoint, remote.NoModifyURL,
		remote.WithDialTimeout(options.CommandTimeout))
	root, rootStop := chromedp.NewContext(alloc, chromedp.WithDetachOnCancel(),
		chromedp.WithBrowserOption(chromedp.WithBrowserLogf(func(string, ...any) {})))
	stopAttachment := context.AfterFunc(attachCtx, cancel)
	// Targets initializes only the browser connection. Do would also select or
	// create a tab, which is not part of attaching to an external browser.
	_, err = chromedp.Targets(root)
	stopAttachment()
	if err == nil {
		err = attachCtx.Err()
	}
	if err != nil {
		cancel()
		rootStop()
		allocStop()
		return nil, &OperationError{Operation: "attach", Err: errors.Join(err, attachCtx.Err())}
	}
	ops, stopOps := context.WithCancel(context.Background())
	b := &Browser{
		ctx: root, cancel: cancel, rootStop: rootStop, allocStop: allocStop,
		ops: ops, stopOps: stopOps, options: options, gate: make(chan struct{}, 1),
		tasks: make(map[*Task]struct{}), pages: make(map[string]*Page),
		eventPages: make(map[string]*Page), eventFrames: make(map[cdp.FrameID]*Page),
		pendingPopups: make(map[*Task]map[string]*Popup), eventFinishing: make(map[*Task]bool), downloads: make(map[string]*Download),
	}
	if err := b.startEvents(attachCtx); err != nil {
		cancel()
		rootStop()
		allocStop()
		return nil, err
	}
	return b, nil
}

func (b *Browser) connectionError() error {
	if b.closing.Load() {
		return ErrBrowserClosed
	}
	select {
	case <-chromedp.FromContext(b.ctx).Browser.LostConnection:
		return ErrConnectionLost
	case <-b.ctx.Done():
		return ErrConnectionLost
	default:
		return nil
	}
}

// operationContext preserves chromedp's context values but limits a single call,
// never the lifetime of the tab or browser. Close can interrupt active calls.
func (b *Browser) operationContext(caller, base context.Context) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(base, b.options.CommandTimeout)
	stopCaller := context.AfterFunc(caller, cancel)
	stopClosing := context.AfterFunc(b.ops, cancel)
	if caller.Err() != nil || b.ops.Err() != nil {
		cancel()
	}
	return ctx, func() { stopCaller(); stopClosing(); cancel() }
}

func (b *Browser) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case b.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			b.unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Browser) unlock() { <-b.gate }

func (b *Browser) operationError(caller context.Context, operation, id string, effects bool, err error) error {
	if err == nil {
		return nil
	}
	if caller.Err() != nil {
		err = errors.Join(err, caller.Err())
	}
	if connectionErr := b.connectionError(); connectionErr != nil {
		err = errors.Join(err, connectionErr)
	}
	return &OperationError{Operation: operation, PageID: id, MayHaveEffects: effects, Err: err}
}

func (b *Browser) session() *chromedp.Browser { return chromedp.FromContext(b.ctx).Browser }

// Pages lists page targets without claiming or attaching to them.
func (b *Browser) Pages(ctx context.Context) ([]PageInfo, error) {
	op, done := b.operationContext(ctx, b.ctx)
	defer done()
	if err := b.lock(op); err != nil {
		return nil, b.operationError(ctx, "list pages", "", false, err)
	}
	defer b.unlock()
	if err := b.connectionError(); err != nil {
		return nil, err
	}
	result, err := cdp.Call(op, b.session(), target.GetTargets, target.GetTargetsParams{})
	if err != nil {
		return nil, b.operationError(ctx, "list pages", "", false, err)
	}
	var pages []PageInfo
	for _, info := range result.TargetInfos {
		if info.Type == "page" {
			pages = append(pages, PageInfo{ID: string(info.TargetID), URL: info.URL, Title: info.Title})
		}
	}
	slices.SortFunc(pages, func(a, b PageInfo) int { return strings.Compare(a.ID, b.ID) })
	return pages, nil
}

// NewTask starts a logical group of tabs sharing the browser's default context.
func (b *Browser) NewTask(ctx context.Context, name string) (*Task, error) {
	op, done := b.operationContext(ctx, b.ctx)
	defer done()
	if err := b.lock(op); err != nil {
		return nil, b.operationError(ctx, "new task", "", false, err)
	}
	defer b.unlock()
	if err := b.connectionError(); err != nil {
		return nil, err
	}
	task := &Task{browser: b, name: name, pages: make(map[string]*Page)}
	b.tasks[task] = struct{}{}
	return task, nil
}

// Close interrupts active operations, cleans up tasks, and disconnects. It never
// sends Browser.close. Cleanup uses its own CleanupTimeout even if ctx has been
// canceled. Repeated calls return the same report. Unconfirmed tabs are Remaining.
func (b *Browser) Close(ctx context.Context) (FinishResult, error) {
	b.closeOnce.Do(func() {
		b.closing.Store(true)
		b.stopOps()
		b.endEvents(ErrBrowserClosed)
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), b.options.CleanupTimeout)
		defer cancel()
		if err := b.lock(cleanup); err != nil {
			b.closeErr = fmt.Errorf("qdriver disconnect: cleanup did not acquire the operation gate: %w", err)
		} else {
			for task := range b.tasks {
				info, err := task.finishLocked(cleanup, nil)
				b.closeInfo.append(info)
				b.closeErr = errors.Join(b.closeErr, err)
			}
			b.closeDownloads(cleanup)
			b.unlock()
		}
		b.cancel()
		b.rootStop()
		b.allocStop()
	})
	return b.closeInfo.clone(), b.closeErr
}
