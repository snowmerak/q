package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	qdriver "github.com/snowmerak/q/v2/pkg/cdp"
)

type managedPage struct {
	page            *qdriver.Page
	task, name, key string
	refs            map[string]qdriver.Ref
	refOrder        []string
	session         string
}
type pageView struct {
	Name    string `json:"name,omitempty"`
	ID      string `json:"id"`
	Task    string `json:"task,omitempty"`
	Owned   bool   `json:"owned"`
	Managed bool   `json:"managed"`
	URL     string `json:"url,omitempty"`
	Title   string `json:"title,omitempty"`
	Session string `json:"session,omitempty"`
}

func (p *managedPage) view() pageView {
	return pageView{Name: p.name, ID: p.page.ID(), Task: p.task, Owned: p.page.Owned(), Managed: true, Session: p.session}
}

type subscriptionEntry struct {
	sub    *qdriver.Subscription
	page   *managedPage
	cancel context.CancelFunc
}
type handleEntry struct {
	value any
	page  *managedPage
}

// The coordinator protects CLI task/page/ref metadata. Blocking event and
// download waits never hold it. SDK modal interruption frees active commands.
type service struct {
	browser       *qdriver.Browser
	instance      string
	gate          chan struct{}
	tasks         map[string]*qdriver.Task
	pages         map[string]*managedPage
	nextPage      uint64
	handlesMu     sync.Mutex
	subscriptions map[string]subscriptionEntry
	handles       map[string]handleEntry
	identities    map[any]string
	nextHandle    uint64
	done          chan struct{}
	closeOnce     sync.Once
	closeResult   qdriver.FinishResult
	closeErr      error
	eventsCtx     context.Context
	stopEvents    context.CancelFunc
	sessionID     string
	store         *sessionStore
	ownedBrowser  *launchedBrowser
	inputMode     string
	closing       atomic.Bool
}

func newService(ctx context.Context, b *qdriver.Browser, instance string) (*service, error) {
	task, err := b.NewTask(ctx, "default")
	if err != nil {
		return nil, err
	}
	life, stop := context.WithCancel(context.Background())
	return &service{browser: b, instance: instance, gate: make(chan struct{}, 1), tasks: map[string]*qdriver.Task{"default": task}, pages: make(map[string]*managedPage), subscriptions: make(map[string]subscriptionEntry), handles: make(map[string]handleEntry), identities: make(map[any]string), done: make(chan struct{}), eventsCtx: life, stopEvents: stop}, nil
}
func (s *service) lock(ctx context.Context) error {
	select {
	case s.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			s.unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *service) unlock() { <-s.gate }
func (s *service) close() (qdriver.FinishResult, error) {
	s.closeOnce.Do(func() {
		s.closing.Store(true)
		s.stopEvents()
		s.closeResult, s.closeErr = s.browser.Close(context.Background())
		if s.ownedBrowser != nil {
			s.closeErr = errors.Join(s.closeErr, s.ownedBrowser.close())
		}
		if s.store != nil {
			if err := s.store.finish(s.closeResult); err != nil {
				s.closeErr = errors.Join(s.closeErr, &commandError{Code: "storage_error", Message: err.Error(), MayHaveEffects: true})
			}
		}
		close(s.done)
	})
	return s.closeResult, s.closeErr
}

func (s *service) run(ctx context.Context, r request) (any, error) {
	if r.Command == "status" {
		state := "running"
		if s.closing.Load() {
			state = "closing"
		}
		result := map[string]any{"instance": s.instance, "pid": os.Getpid(), "version": protocolVersion, "session": s.sessionID, "state": state}
		result["inputMode"] = s.inputMode
		if s.ownedBrowser != nil {
			result["browser"] = s.ownedBrowser.info()
		}
		return result, nil
	}
	if r.Command == "stop" {
		return s.close()
	}
	select {
	case <-s.done:
		return nil, qdriver.ErrBrowserClosed
	default:
	}
	switch r.Command {
	case "events.next":
		return s.nextEvent(ctx, r.ID)
	case "download.wait", "download.info", "download.save":
		h, err := s.handle(r.ID)
		if err != nil {
			return nil, err
		}
		d, ok := h.value.(*qdriver.Download)
		if !ok {
			return nil, invalid("handle is not a download")
		}
		switch r.Command {
		case "download.info":
			return d.Info(), nil
		case "download.wait":
			path, err := d.Wait(ctx)
			return map[string]any{"path": path, "info": d.Info()}, err
		default:
			err := d.SaveAs(ctx, r.Value)
			return map[string]string{"path": r.Value}, err
		}
	case "page.wait-idle":
		if err := s.lock(ctx); err != nil {
			return nil, err
		}
		p, err := s.page(r.Page)
		if err != nil {
			s.unlock()
			return nil, err
		}
		wait, err := p.page.ExpectNetworkIdle(ctx, time.Duration(r.DurationMS)*time.Millisecond)
		s.unlock()
		if err != nil {
			return nil, err
		}
		if err := wait.Wait(ctx); err != nil {
			return nil, err
		}
		return map[string]bool{"idle": true}, nil
	}
	if err := s.lock(ctx); err != nil {
		return nil, err
	}
	defer s.unlock()
	result, err := s.command(ctx, r)
	if changesInventory(r.Command) {
		if storageErr := s.persistInventory(); storageErr != nil {
			err = errors.Join(err, &commandError{Code: "storage_error", Message: storageErr.Error(), MayHaveEffects: true})
		}
	}
	return result, err
}

func changesInventory(command string) bool {
	switch command {
	case "task.new", "task.finish", "page.new", "page.adopt", "page.close", "page.release", "popup.adopt":
		return true
	default:
		return false
	}
}

// Called with the CLI coordinator held, or before the listener is published.
// It records observations, not a crash-recovery authority for browser targets.
func (s *service) persistInventory() error {
	if s.store == nil {
		return nil
	}
	tasks := make([]string, 0, len(s.tasks))
	for name := range s.tasks {
		tasks = append(tasks, name)
	}
	slices.Sort(tasks)
	pages := make([]pageView, 0, len(s.pages))
	for _, p := range s.pages {
		pages = append(pages, p.view())
	}
	slices.SortFunc(pages, func(a, b pageView) int { return strings.Compare(a.Name, b.Name) })
	return s.store.update(tasks, pages)
}

func (s *service) page(name string) (*managedPage, error) {
	if p := s.pages[name]; p != nil {
		return p, nil
	}
	for _, p := range s.pages {
		if p.page.ID() == name {
			return p, nil
		}
	}
	return nil, missing("page", name)
}
func (s *service) pageName(name string) (string, error) {
	if name == "" {
		for {
			candidate := fmt.Sprintf("page%d", s.nextPage+1)
			if s.pages[candidate] == nil {
				return candidate, nil
			}
			s.nextPage++
		}
	}
	if len(name) > 128 || strings.TrimSpace(name) == "" {
		return "", invalid("page names must contain 1..128 characters")
	}
	if s.pages[name] != nil {
		return "", &commandError{Code: "already_exists", Message: "page name is already managed"}
	}
	// Disallow using a current target ID as a second page's alias.
	for _, p := range s.pages {
		if p.page.ID() == name {
			return "", invalid("page name collides with a target ID")
		}
	}
	return name, nil
}
func (s *service) register(p *qdriver.Page, task, name string) *managedPage {
	s.nextPage++
	entry := &managedPage{page: p, task: task, name: name, key: fmt.Sprintf("p%d", s.nextPage), refs: make(map[string]qdriver.Ref), session: s.sessionID}
	s.pages[name] = entry
	return entry
}
func (s *service) forget(p *managedPage) {
	delete(s.pages, p.name)
	s.handlesMu.Lock()
	defer s.handlesMu.Unlock()
	for id, e := range s.subscriptions {
		if e.page == p {
			e.sub.Close()
			e.cancel()
			delete(s.subscriptions, id)
		}
	}
	for id, e := range s.handles {
		if e.page == p {
			delete(s.identities, e.value)
			delete(s.handles, id)
		}
	}
}
func (s *service) ref(p *managedPage, token string) (qdriver.Ref, error) {
	if ref, ok := p.refs[token]; ok {
		return ref, nil
	}
	if strings.HasPrefix(token, "@"+s.instance+".") && !strings.HasPrefix(token, "@"+s.instance+"."+p.key+".") {
		return qdriver.Ref{}, qdriver.ErrWrongPage
	}
	return qdriver.Ref{}, qdriver.ErrStaleRef
}
func (s *service) locator(p *managedPage, l locator) (qdriver.Locator, error) {
	var result qdriver.Locator
	switch l.Kind {
	case "ref":
		ref, err := s.ref(p, l.Value)
		if err != nil {
			return result, err
		}
		if l.FrameSet {
			return result, invalid("ref has its own frame")
		}
		return qdriver.ByRef(ref), nil
	case "css":
		result = qdriver.CSS(l.Value)
	case "text":
		result = qdriver.Text(l.Value)
	case "role":
		result = qdriver.Role(l.Value, l.Name)
	default:
		return result, invalid("a locator is required")
	}
	if l.FrameSet {
		result = result.InFrame(l.Frame)
	}
	return result, nil
}

func (s *service) command(ctx context.Context, r request) (any, error) {
	switch r.Command {
	case "task.new":
		if strings.TrimSpace(r.Task) == "" || len(r.Task) > 128 {
			return nil, invalid("task names must contain 1..128 characters")
		}
		if s.tasks[r.Task] != nil {
			return nil, &commandError{Code: "already_exists", Message: "task already exists"}
		}
		t, err := s.browser.NewTask(ctx, r.Task)
		if err != nil {
			return nil, err
		}
		s.tasks[r.Task] = t
		return map[string]string{"name": r.Task}, nil
	case "task.list":
		names := make([]string, 0, len(s.tasks))
		for name := range s.tasks {
			names = append(names, name)
		}
		slices.Sort(names)
		return names, nil
	case "task.finish":
		t := s.tasks[r.Task]
		if t == nil {
			return nil, missing("task", r.Task)
		}
		var keep []*qdriver.Page
		for _, name := range r.Keep {
			p, err := s.page(name)
			if err != nil {
				return nil, err
			}
			if p.task != r.Task {
				return nil, qdriver.ErrInvalidPage
			}
			keep = append(keep, p.page)
		}
		result, err := t.Finish(ctx, keep...)
		for _, id := range append(append(slices.Clone(result.Closed), result.Released...), result.Kept...) {
			for _, p := range s.pages {
				if p.page.ID() == id {
					s.forget(p)
					break
				}
			}
		}
		if err == nil && len(result.Remaining) == 0 {
			delete(s.tasks, r.Task)
		}
		return result, err
	case "page.list":
		pages, err := s.browser.Pages(ctx)
		if err != nil {
			return nil, err
		}
		result := make([]pageView, 0, len(pages))
		for _, info := range pages {
			v := pageView{ID: info.ID, URL: info.URL, Title: info.Title}
			for _, p := range s.pages {
				if p.page.ID() == info.ID {
					v = p.view()
					v.URL = info.URL
					v.Title = info.Title
					break
				}
			}
			if s.sessionID == "" || r.All || v.Managed {
				result = append(result, v)
			}
		}
		return result, nil
	case "page.new", "page.adopt":
		t := s.tasks[r.Task]
		if t == nil {
			return nil, missing("task", r.Task)
		}
		name, err := s.pageName(r.Name)
		if err != nil {
			return nil, err
		}
		var p *qdriver.Page
		if r.Command == "page.new" {
			p, err = t.NewPage(ctx)
		} else {
			p, err = t.Adopt(ctx, r.ID)
		}
		if p == nil {
			return nil, err
		}
		entry := s.register(p, r.Task, name)
		if err == nil && r.Command == "page.new" && r.Value != "" {
			err = p.Goto(ctx, r.Value)
		}
		return entry.view(), err
	case "events.close":
		s.handlesMu.Lock()
		e, ok := s.subscriptions[r.ID]
		delete(s.subscriptions, r.ID)
		s.handlesMu.Unlock()
		if !ok {
			return nil, missing("subscription", r.ID)
		}
		e.sub.Close()
		e.cancel()
		return map[string]bool{"closed": true}, nil
	case "popup.adopt":
		return s.adoptPopup(ctx, r)
	case "dialog.accept", "dialog.dismiss", "chooser.set", "chooser.cancel", "download.cancel", "handle.close":
		return s.handleCommand(ctx, r)
	case "download.enable":
		return map[string]string{"directory": r.Value}, s.browser.EnableDownloads(ctx, r.Value)
	}
	p, err := s.page(r.Page)
	if err != nil {
		return nil, err
	}
	switch r.Command {
	case "page.goto":
		return p.view(), p.page.Goto(ctx, r.Value)
	case "page.title":
		return p.page.Title(ctx)
	case "page.url":
		return p.page.URL(ctx)
	case "page.frames":
		return p.page.Frames(ctx)
	case "page.activate":
		return p.view(), p.page.Activate(ctx)
	case "page.close":
		err := p.page.Close(ctx)
		v := p.view()
		if err == nil {
			s.forget(p)
			v.Managed = false
		}
		return v, err
	case "page.release":
		err := s.tasks[p.task].Release(ctx, p.page)
		v := p.view()
		if err == nil {
			s.forget(p)
			v.Managed = false
		}
		return v, err
	case "page.eval":
		value, err := p.page.Evaluate(ctx, r.Value)
		if len(value) == 0 {
			return nil, err
		}
		return json.RawMessage(value), err
	case "page.wait-function":
		if err := p.page.WaitForFunction(ctx, r.Value); err != nil {
			return nil, err
		}
		return map[string]bool{"ready": true}, nil
	case "page.screenshot":
		n, err := captureScreenshot(ctx, p.page, r.Value)
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": r.Value, "bytes": n}, nil
	case "page.snapshot":
		return s.snapshot(ctx, p, r)
	case "page.click", "page.fill", "page.press", "page.upload", "page.wait-selector":
		loc, err := s.locator(p, r.Locator)
		if err != nil {
			return nil, err
		}
		switch r.Command {
		case "page.click":
			return p.page.Click(ctx, loc)
		case "page.fill":
			return p.page.Fill(ctx, loc, r.Value)
		case "page.press":
			return p.page.Press(ctx, loc, r.Value)
		case "page.upload":
			return p.page.SetInputFiles(ctx, loc, r.Paths...)
		default:
			if err := p.page.WaitForSelector(ctx, loc); err != nil {
				return nil, err
			}
			return map[string]bool{"ready": true}, nil
		}
	case "events.arm":
		return s.arm(ctx, p, qdriver.EventKind(r.Value))
	case "dialog.current":
		d := p.page.CurrentDialog()
		if d == nil {
			return nil, missing("dialog", r.Page)
		}
		id, err := s.storeHandle(d, p)
		return map[string]any{"handle": id, "info": d.Info}, err
	case "chooser.interception":
		if r.Value != "on" && r.Value != "off" {
			return nil, invalid("interception must be on or off")
		}
		return map[string]bool{"enabled": r.Value == "on"}, p.page.SetFileChooserInterception(ctx, r.Value == "on")
	default:
		return nil, invalid("unknown command %q", r.Command)
	}
}

type nodeView struct {
	Role    string            `json:"role"`
	Name    string            `json:"name"`
	Value   string            `json:"value,omitempty"`
	States  map[string]string `json:"states,omitempty"`
	Ref     string            `json:"ref,omitempty"`
	Depth   int               `json:"depth"`
	FrameID string            `json:"frameId"`
}
type snapshotView struct {
	Content     string                `json:"content"`
	Nodes       []nodeView            `json:"nodes"`
	Scope       qdriver.SnapshotScope `json:"scope"`
	DocumentID  string                `json:"documentId"`
	Truncated   bool                  `json:"truncated"`
	Unsupported []string              `json:"unsupported,omitempty"`
	Frames      []qdriver.FrameInfo   `json:"frames"`
}

func (s *service) snapshot(ctx context.Context, p *managedPage, r request) (snapshotView, error) {
	var result snapshotView
	if r.MaxNodes < 1 || r.MaxNodes > 10000 || r.MaxBytes < 1 || r.MaxBytes > 1<<20 {
		return result, invalid("invalid snapshot budget")
	}
	options := qdriver.SnapshotOptions{Scope: qdriver.SnapshotScope(r.Scope), MaxNodes: r.MaxNodes, MaxBytes: r.MaxBytes}
	if r.Root != "" {
		ref, err := s.ref(p, r.Root)
		if err != nil {
			return result, err
		}
		options.Root = ref
	}
	snapshot, err := p.page.Snapshot(ctx, options)
	if err != nil {
		return result, err
	}
	if snapshot.Scope == qdriver.FullPage {
		clear(p.refs)
		p.refOrder = nil
	}
	result = snapshotView{Content: snapshot.Content, Scope: snapshot.Scope, DocumentID: snapshot.DocumentID, Truncated: snapshot.Truncated, Unsupported: snapshot.Unsupported, Frames: snapshot.Frames, Nodes: make([]nodeView, 0, len(snapshot.Nodes))}
	delivered := make(map[string]bool)
	var replacements []string
	for _, node := range snapshot.Nodes {
		ref := ""
		if node.Ref.String() != "" {
			ref = fmt.Sprintf("@%s.%s.%s", s.instance, p.key, strings.TrimPrefix(node.Ref.String(), "@"))
			if _, exists := p.refs[ref]; !exists {
				p.refOrder = append(p.refOrder, ref)
			}
			p.refs[ref] = node.Ref
			delivered[ref] = true
			replacements = append(replacements, "[ref="+node.Ref.String()+"]", "[ref="+ref+"]")
		}
		result.Nodes = append(result.Nodes, nodeView{node.Role, node.Name, node.Value, node.States, ref, node.Depth, node.FrameID})
	}
	order := p.refOrder[:0]
	for _, ref := range p.refOrder {
		if len(p.refs) > 10000 && !delivered[ref] {
			delete(p.refs, ref)
		} else {
			order = append(order, ref)
		}
	}
	p.refOrder = order
	result.Content = strings.NewReplacer(replacements...).Replace(result.Content)
	if len(result.Content) > r.MaxBytes {
		result.Content = result.Content[:strings.LastIndex(result.Content[:r.MaxBytes], "\n")+1]
		result.Truncated = true
	}
	return result, nil
}

func captureScreenshot(ctx context.Context, p *qdriver.Page, path string) (n int, resultErr error) {
	if !filepath.IsAbs(path) {
		return 0, invalid("output path must be absolute")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return 0, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, f.Close())
		if resultErr != nil {
			resultErr = errors.Join(resultErr, os.Remove(path))
			resultErr = failure(resultErr, true) // The tab may already have been activated.
		}
	}()
	// Check exclusive creation before contacting Chrome. Activating the chosen
	// tab avoids compositor waits for background tabs in headless Chromium.
	if err := p.Activate(ctx); err != nil {
		return 0, err
	}
	data, err := p.Screenshot(ctx)
	if err != nil {
		return 0, err
	}
	return f.Write(data)
}
