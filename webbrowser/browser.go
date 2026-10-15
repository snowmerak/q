// Package webbrowser owns Q's optional, lazily launched headless browser.
package webbrowser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

const (
	defaultIdleTimeout = 2 * time.Minute
	operationTimeout   = 30 * time.Second
	maximumTabs        = 8
	maximumElements    = 100
)

type OpenInput struct {
	URL string `json:"url" jsonschema:"Absolute HTTP or HTTPS URL to open in a new tab."`
}

type TabInput struct {
	TabID string `json:"tab_id" jsonschema:"Tab ID returned by browser_open. Tabs expire after two minutes without browser tool calls."`
}

type ClickInput struct {
	TabID string `json:"tab_id"`
	Ref   string `json:"ref" jsonschema:"Element ref from the latest browser_snapshot for this tab."`
}

type FillInput struct {
	TabID string `json:"tab_id"`
	Ref   string `json:"ref" jsonschema:"Editable element ref from the latest browser_snapshot for this tab."`
	Text  string `json:"text" jsonschema:"Replacement text for the input or textarea."`
}

type Link struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type PageContent struct {
	StatusCode int    `json:"status_code,omitempty"`
	TabID      string `json:"tab_id"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	Links      []Link `json:"links"`
	Truncated  bool   `json:"truncated"`
}

type Element struct {
	Ref       string `json:"ref"`
	Role      string `json:"role"`
	Name      string `json:"name"`
	URL       string `json:"url,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
	InputType string `json:"input_type,omitempty"`
	Value     string `json:"value,omitempty"`
	Search    bool   `json:"search,omitempty"`
}

type Snapshot struct {
	TabID     string    `json:"tab_id"`
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Elements  []Element `json:"elements"`
	Truncated bool      `json:"truncated"`
}

type ActionResult struct {
	TabID         string `json:"tab_id"`
	NeedsSnapshot bool   `json:"needs_snapshot"`
}

type CloseResult struct {
	TabID  string `json:"tab_id"`
	Closed bool   `json:"closed"`
}

type tab struct {
	page     *rod.Page
	document string
	refs     map[string]*rod.Element
}

// Runtime serializes browser operations so idle cleanup cannot race a call.
// It owns a temporary Chrome profile; no user's existing browser is attached.
type Runtime struct {
	releaseReaders func() error
	mu             sync.Mutex
	browser        *rod.Browser
	cancelBrowser  context.CancelFunc
	launcher       *launcher.Launcher
	tabs           map[string]*tab
	nextTab        uint64
	nextSnapshot   uint64
	idleTimeout    time.Duration
	idle           *time.Timer
	closed         bool
}

// New creates an inactive runtime. Chrome is only started by Open.
func New() *Runtime {
	return NewWithIdleTimeout(defaultIdleTimeout)
}

// NewWithIdleTimeout creates an inactive runtime with caller-selected expiry.
// Nonpositive values use the default. The caller must still close the runtime.
func NewWithIdleTimeout(timeout time.Duration) *Runtime {
	if timeout <= 0 {
		timeout = defaultIdleTimeout
	}
	return &Runtime{tabs: make(map[string]*tab), idleTimeout: timeout}
}

func validateURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return errors.New("browser URL must be an absolute HTTP or HTTPS URL without embedded credentials")
	}
	return nil
}

// begin holds the runtime lock until finish, including browser startup.
func (r *Runtime) begin(ctx context.Context) (context.Context, func(), error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, errors.New("browser runtime is closed")
	}
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return nil, nil, err
	}
	if r.idle != nil {
		r.idle.Stop()
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	return ctx, func() {
		cancel()
		if r.browser != nil {
			// A stopped timer's callback may already be waiting for the lock.
			// Compare identity before stopping the currently active browser.
			var timer *time.Timer
			timer = time.AfterFunc(r.idleTimeout, func() {
				r.mu.Lock()
				defer r.mu.Unlock()
				if r.idle == timer {
					_ = r.stop()
				}
			})
			r.idle = timer
		}
		r.mu.Unlock()
	}, nil
}

func (r *Runtime) start(ctx context.Context) error {
	if r.browser != nil {
		return nil
	}
	bin := strings.TrimSpace(os.Getenv("Q_BROWSER_PATH"))
	if bin != "" {
		resolved, err := exec.LookPath(bin)
		if err != nil {
			return fmt.Errorf("Q_BROWSER_PATH: %w", err)
		}
		bin = resolved
	} else {
		var found bool
		bin, found = launcher.LookPath()
		if !found {
			return errors.New("Chrome/Chromium was not found; install a browser or set Q_BROWSER_PATH to its executable")
		}
	}
	profile, err := os.MkdirTemp("", "q-browser-")
	if err != nil {
		return err
	}
	// A fixed executable disables Rod's automatic browser download. Override
	// Rod environment defaults so Q always owns an isolated headless process.
	l := launcher.New().Bin(bin).UserDataDir(profile).RemoteDebuggingPort(0).
		HeadlessNew(true).NoSandbox(false).Leakless(false).Context(ctx)
	// Do not extract/execute Rod's leakless helper. Q owns normal shutdown and
	// idle cleanup directly; this also avoids Windows antivirus false positives.
	l.Delete("disable-features").Delete("disable-site-isolation-trials")
	endpoint, err := l.Launch()
	if err != nil {
		// Launch may fail before a process is started; Cleanup would then wait forever.
		l.Kill()
		_ = os.RemoveAll(profile)
		return fmt.Errorf("launch browser: %w", err)
	}
	// The CDP connection must outlive the tool call that first opened it.
	connectionContext, cancelConnection := context.WithCancel(context.Background())
	stopCancellation := context.AfterFunc(ctx, cancelConnection)
	b := rod.New().ControlURL(endpoint).Context(connectionContext)
	connectErr := b.Connect()
	stopCancellation()
	if connectErr != nil || connectionContext.Err() != nil {
		cancelConnection()
		l.Kill()
		l.Cleanup()
		return fmt.Errorf("connect browser: %w", errors.Join(connectErr, connectionContext.Err()))
	}
	r.browser, r.launcher, r.cancelBrowser = b, l, cancelConnection
	return nil
}

func (r *Runtime) getTab(id string) (*tab, error) {
	t, ok := r.tabs[id]
	if !ok {
		return nil, errors.New("browser tab is unknown or expired; call browser_open to create a new tab")
	}
	return t, nil
}

func (r *Runtime) Open(ctx context.Context, input OpenInput) (PageContent, error) {
	input.URL = strings.TrimSpace(input.URL)
	if err := validateURL(input.URL); err != nil {
		return PageContent{}, err
	}
	ctx, finish, err := r.begin(ctx)
	if err != nil {
		return PageContent{}, err
	}
	defer finish()
	if len(r.tabs) >= maximumTabs {
		return PageContent{}, errors.New("browser tab limit reached; close a tab before opening another")
	}
	if err := r.start(ctx); err != nil {
		return PageContent{}, err
	}
	// Rod binds a page's event stream to the context used to create it. Create
	// it under the connection lifetime, then bound navigation/read separately.
	stopCancellation := context.AfterFunc(ctx, r.cancelBrowser)
	p, err := r.browser.Page(proto.TargetCreateTarget{})
	stopCancellation()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		if p != nil {
			closePage(p)
		}
		if len(r.tabs) == 0 || r.browser.GetContext().Err() != nil {
			_ = r.stop()
		}
		return PageContent{}, err
	}
	p = p.Context(ctx)
	r.nextTab++
	id := fmt.Sprintf("tab-%d", r.nextTab)
	var result PageContent
	err = p.Navigate(input.URL)
	if err == nil {
		result, err = readPage(p, id)
	}
	if err == nil {
		r.tabs[id] = &tab{page: p.Context(r.browser.GetContext())}
		return result, nil
	}
	closePage(p)
	if len(r.tabs) == 0 {
		_ = r.stop()
	}
	return PageContent{}, err
}

func (r *Runtime) Read(ctx context.Context, input TabInput) (PageContent, error) {
	ctx, finish, err := r.begin(ctx)
	if err != nil {
		return PageContent{}, err
	}
	defer finish()
	t, err := r.getTab(input.TabID)
	if err != nil {
		return PageContent{}, err
	}
	return readPage(t.page.Context(ctx), input.TabID)
}

func readPage(p *rod.Page, id string) (PageContent, error) {
	if err := p.WaitLoad(); err != nil {
		return PageContent{}, err
	}
	obj, err := p.Eval(readScript)
	if err != nil {
		return PageContent{}, err
	}
	var result PageContent
	if err := obj.Value.Unmarshal(&result); err != nil {
		return PageContent{}, err
	}
	result.TabID = id
	return result, validateURL(result.URL)
}

func (r *Runtime) Snapshot(ctx context.Context, input TabInput) (Snapshot, error) {
	ctx, finish, err := r.begin(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer finish()
	t, err := r.getTab(input.TabID)
	if err != nil {
		return Snapshot{}, err
	}
	p := t.page.Context(ctx)
	if err := p.WaitLoad(); err != nil {
		return Snapshot{}, err
	}
	clearRefs(t)
	doc, err := p.Eval(documentScript)
	if err != nil {
		return Snapshot{}, err
	}
	info, err := p.Info()
	if err != nil {
		return Snapshot{}, err
	}
	if err := validateURL(info.URL); err != nil {
		return Snapshot{}, err
	}
	elements, err := p.ElementsByJS(rod.Eval(elementsScript, maximumElements+1))
	if err != nil {
		return Snapshot{}, err
	}
	r.nextSnapshot++
	t.document, t.refs = doc.Value.Str(), make(map[string]*rod.Element)
	result := Snapshot{TabID: input.TabID, URL: info.URL, Title: info.Title, Elements: []Element{}, Truncated: len(elements) > maximumElements}
	for index, el := range elements {
		if index >= maximumElements {
			_ = el.Release()
			continue
		}
		obj, evalErr := el.Eval(elementScript)
		if evalErr != nil {
			for _, remaining := range elements[index:] {
				_ = remaining.Context(ctx).Release()
			}
			clearRefs(t)
			return Snapshot{}, evalErr
		}
		var entry Element
		if err := obj.Value.Unmarshal(&entry); err != nil {
			clearRefs(t)
			return Snapshot{}, err
		}
		entry.Ref = fmt.Sprintf("s%d-e%d", r.nextSnapshot, index+1)
		t.refs[entry.Ref] = el.Context(context.Background())
		result.Elements = append(result.Elements, entry)
	}
	return result, nil
}

func (r *Runtime) act(ctx context.Context, id, ref string, action func(*rod.Element) error) (ActionResult, error) {
	ctx, finish, err := r.begin(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	defer finish()
	t, err := r.getTab(id)
	if err != nil {
		return ActionResult{}, err
	}
	el, found := t.refs[ref]
	if !found {
		return ActionResult{}, errors.New("element ref is unknown or stale; call browser_snapshot again")
	}
	doc, err := t.page.Context(ctx).Eval(documentScript)
	if err != nil || doc.Value.Str() != t.document {
		clearRefs(t)
		return ActionResult{}, errors.New("page changed; call browser_snapshot again")
	}
	connected, err := el.Context(ctx).Eval(`() => this.isConnected`)
	if err != nil || !connected.Value.Bool() {
		clearRefs(t)
		return ActionResult{}, errors.New("element changed; call browser_snapshot again")
	}
	err = action(el.Context(ctx))
	// Even a failed interaction may have changed page state.
	clearRefs(t)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{TabID: id, NeedsSnapshot: true}, nil
}

func (r *Runtime) Click(ctx context.Context, input ClickInput) (ActionResult, error) {
	return r.act(ctx, input.TabID, input.Ref, func(el *rod.Element) error {
		return el.Click(proto.InputMouseButtonLeft, 1)
	})
}

// Navigate reuses an owned tab instead of opening another process or tab.
func (r *Runtime) Navigate(ctx context.Context, id, target string) (PageContent, error) {
	if err := validateURL(target); err != nil {
		return PageContent{}, err
	}
	ctx, finish, err := r.begin(ctx)
	if err != nil {
		return PageContent{}, err
	}
	defer finish()
	t, err := r.getTab(id)
	if err != nil {
		return PageContent{}, err
	}
	clearRefs(t)
	p := t.page.Context(ctx)
	if err := p.Navigate(target); err != nil {
		return PageContent{}, err
	}
	return readPage(p, id)
}

func searchElement(el *rod.Element, textbox bool) error {
	obj, err := el.Eval(elementScript)
	if err != nil {
		return err
	}
	var entry Element
	if err := obj.Value.Unmarshal(&entry); err != nil {
		return err
	}
	if !entry.Search || entry.Disabled || (textbox && entry.InputType != "search" && entry.InputType != "text") {
		return errors.New("web search interaction requires an enabled search control in a GET form or standalone search input")
	}
	return nil
}

// FillSearch restricts the research loop to structurally identified search inputs.
func (r *Runtime) FillSearch(ctx context.Context, value FillInput) (ActionResult, error) {
	return r.act(ctx, value.TabID, value.Ref, func(el *rod.Element) error {
		if err := searchElement(el, true); err != nil {
			return err
		}
		if err := el.SelectAllText(); err != nil {
			return err
		}
		return el.Input(value.Text)
	})
}

// SubmitSearch validates the live element again, after any DOM changes.
func (r *Runtime) SubmitSearch(ctx context.Context, value ClickInput, button bool) (ActionResult, error) {
	return r.act(ctx, value.TabID, value.Ref, func(el *rod.Element) error {
		if err := searchElement(el, !button); err != nil {
			return err
		}
		page := el.Page().Context(el.GetContext())
		const observation = `() => JSON.stringify([location.href, performance.timeOrigin, document.body?.innerText.slice(0,12000)])`
		before, err := page.Eval(observation)
		if err != nil {
			return err
		}
		if button {
			err = el.Click(proto.InputMouseButtonLeft, 1)
		} else {
			err = el.Type(input.Enter)
		}
		if err != nil {
			return err
		}
		// Key dispatch returns before navigation starts. Wait for navigation or
		// a same-document result update; standalone JS search need not navigate.
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-el.GetContext().Done():
				return el.GetContext().Err()
			case <-timer.C:
				return nil
			case <-tick.C:
				after, readErr := page.Eval(observation)
				if readErr == nil && after.Value.Str() != before.Value.Str() {
					return nil
				}
			}
		}
	})
}

func (r *Runtime) Fill(ctx context.Context, input FillInput) (ActionResult, error) {
	return r.act(ctx, input.TabID, input.Ref, func(el *rod.Element) error {
		editable, err := el.Eval(`() => this.tagName === 'INPUT' || this.tagName === 'TEXTAREA' || this.isContentEditable`)
		if err != nil {
			return err
		}
		if !editable.Value.Bool() {
			return errors.New("element is not a text input")
		}
		if err := el.SelectAllText(); err != nil {
			return err
		}
		return el.Input(input.Text)
	})
}

func (r *Runtime) CloseTab(ctx context.Context, input TabInput) (CloseResult, error) {
	ctx, finish, err := r.begin(ctx)
	if err != nil {
		return CloseResult{}, err
	}
	defer finish()
	t, err := r.getTab(input.TabID)
	if err != nil {
		return CloseResult{}, err
	}
	if err := t.page.Context(ctx).Close(); err != nil {
		return CloseResult{}, err
	}
	delete(r.tabs, input.TabID)
	if len(r.tabs) == 0 {
		if err := r.stop(); err != nil {
			return CloseResult{}, err
		}
	}
	return CloseResult{TabID: input.TabID, Closed: true}, nil
}

func clearRefs(t *tab) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, el := range t.refs {
		_ = el.Context(ctx).Release()
	}
	t.refs = nil
	t.document = ""
}

func closePage(p *rod.Page) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = p.Context(ctx).Close()
}

// stop is called with the runtime lock held. Tab IDs are never reused after idle expiry.
func (r *Runtime) stop() error {
	if r.idle != nil {
		r.idle.Stop()
		r.idle = nil
	}
	if r.browser == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := r.browser.Context(ctx).Close()
	r.cancelBrowser()
	r.launcher.Kill()
	r.launcher.Cleanup()
	r.browser, r.launcher = nil, nil
	r.tabs = make(map[string]*tab)
	return err
}

// Close terminates the owned browser, removes its temporary profile, and
// prevents subsequent tool calls from restarting it.
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	err := r.stop()
	if r.releaseReaders != nil {
		err = errors.Join(err, r.releaseReaders())
		r.releaseReaders = nil
	}
	return err
}
