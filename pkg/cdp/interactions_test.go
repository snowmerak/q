package qdriver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	qdriver "github.com/snowmerak/q/v2/pkg/cdp"
)

const interactionHTML = `<!doctype html><html lang="en"><title>Interaction fixture</title>
<style>body{margin:24px;font:16px sans-serif}label{display:block;margin:8px}button{margin:8px}td{padding:8px}.box{position:relative;width:260px;height:60px}.cover{position:absolute;inset:0;background:white;z-index:4}</style>
<h1>Report</h1><p>Plain report text 안녕 🧪</p>
<form onsubmit="event.preventDefault();window.submits=(window.submits||0)+1;document.getElementById('done').hidden=false">
<label for="name">Name</label><input id="name" value="before">
<button id="submit" type="submit">Submit</button><output id="done" hidden>Saved</output></form>
<label for="memo">Memo</label><textarea id="memo">old memo</textarea>
<div id="editor" contenteditable="true" role="textbox" aria-label="Editor">old edit</div>
<label for="password">Password</label><input id="password" type="PASSWORD" value="do-not-leak-password">
<button id="disabled" disabled>Disabled</button><div id="aria" role="button" tabindex="0" aria-disabled="true">Aria disabled</div>
<input id="readonly" readonly value="locked"><input id="number" type="number" value="3">
<input id="formatted" oninput="window.formatEvents=(window.formatEvents||0)+1;this.value=this.value.toUpperCase()">
<button id="hidden" style="display:none">Hidden secret text</button>
<button class="duplicate">Duplicate</button><button class="duplicate" hidden>Duplicate</button>
<div class="box"><button id="covered" onclick="window.coveredClicks=(window.coveredClicks||0)+1">Covered</button><div class="cover"></div></div>
<div class="box"><button id="hover" onmouseenter="document.getElementById('hover-cover').hidden=false" onclick="window.hovered=(window.hovered||0)+1">Hover target</button><div class="cover" id="hover-cover" hidden></div></div>
<button id="replace" onclick="document.getElementById('victim').outerHTML='<button id=victim onclick=window.replacementClicked=true>Victim</button>'">Replace node</button>
<button id="victim" onclick="window.originalClicked=true">Victim</button>
<button id="navigate" onclick="location.href='/next'">Navigate</button>
<table><tr><th>Column</th></tr><tr><td>Cell content</td></tr></table>
<div style="height:1500px"></div><button id="below" onclick="this.textContent='Below clicked'">Below fold</button>
</html>`

func interactionPage(t *testing.T, b *qdriver.Browser, html string) *qdriver.Page {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/next" {
			_, _ = fmt.Fprint(w, `<!doctype html><title>Next document</title><button id="next">Next</button>`)
		} else {
			_, _ = fmt.Fprint(w, html)
		}
	}))
	t.Cleanup(server.Close)
	task := newTask(t, b)
	t.Cleanup(func() {
		if _, err := task.Finish(context.Background()); err != nil {
			t.Error(err)
		}
	})
	p := newPage(t, task)
	if err := p.Goto(t.Context(), server.URL); err != nil {
		t.Fatal(err)
	}
	return p
}

func takeSnapshot(t *testing.T, p *qdriver.Page, scope qdriver.SnapshotScope) qdriver.Snapshot {
	t.Helper()
	s, err := p.Snapshot(t.Context(), qdriver.SnapshotOptions{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func findRef(t *testing.T, s qdriver.Snapshot, role, name string) qdriver.Ref {
	t.Helper()
	for _, node := range s.Nodes {
		if node.Role == role && node.Name == name && node.Ref.String() != "" {
			return node.Ref
		}
	}
	t.Fatalf("no ref for %s %q:\n%s", role, name, s.Content)
	return qdriver.Ref{}
}

func evaluateJSON(t *testing.T, p *qdriver.Page, expression, want string) {
	t.Helper()
	value, err := p.Evaluate(t.Context(), expression)
	var gotValue, wantValue any
	gotErr := json.Unmarshal(value, &gotValue)
	wantErr := json.Unmarshal([]byte(want), &wantValue)
	if err != nil || gotErr != nil || wantErr != nil || !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("expression %s = %s, want %s; error=%v", expression, value, want, err)
	}
}

func TestSnapshotRefInputWorkflow(t *testing.T) {
	f := startChrome(t)
	b := attachDriver(t, f.endpoint)
	p := interactionPage(t, b, interactionHTML)
	s := takeSnapshot(t, p, qdriver.FullPage)
	for _, text := range []string{"Plain report text 안녕 🧪", "Cell content", "Column"} {
		if !strings.Contains(s.Content, text) {
			t.Fatalf("snapshot lost ordinary content %q:\n%s", text, s.Content)
		}
	}
	if strings.Contains(s.Content, "do-not-leak-password") || strings.Contains(s.Content, "Hidden secret text") {
		t.Fatalf("snapshot exposed hidden/password content:\n%s", s.Content)
	}
	name := findRef(t, s, "textbox", "Name")
	submit := findRef(t, s, "button", "Submit")
	if ref, ok := s.Ref(name.String()); !ok || ref.String() != name.String() {
		t.Fatal("snapshot ref lookup failed")
	}
	if _, ok := s.Ref("@999999"); ok {
		t.Fatal("snapshot manufactured a ref")
	}
	if receipt, err := p.Fill(t.Context(), qdriver.ByRef(name), "Ada 안녕"); err != nil || !receipt.InputCompleted {
		t.Fatalf("fill=%+v err=%v", receipt, err)
	}
	after := takeSnapshot(t, p, qdriver.FullPage)
	if got := findRef(t, after, "textbox", "Name"); got.String() != name.String() {
		t.Fatalf("unchanged node ref changed: %s -> %s", name, got)
	}
	if receipt, err := p.Click(t.Context(), qdriver.ByRef(submit)); err != nil || !receipt.InputCompleted {
		t.Fatalf("click=%+v err=%v", receipt, err)
	}
	if err := p.WaitForSelector(t.Context(), qdriver.CSS("#done")); err != nil {
		t.Fatal(err)
	}
	if receipt, err := p.Press(t.Context(), qdriver.ByRef(name), "Enter"); err != nil || !receipt.InputCompleted {
		t.Fatalf("enter=%+v err=%v", receipt, err)
	}
	evaluateJSON(t, p, "window.submits", "2")
	evaluateJSON(t, p, "document.getElementById('name').value", `"Ada 안녕"`)
	if _, err := p.Fill(t.Context(), qdriver.CSS("#memo"), "안녕\nGo 🧪"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fill(t.Context(), qdriver.CSS("#memo"), ""); err != nil {
		t.Fatalf("empty fill: %v", err)
	}
	if _, err := p.Fill(t.Context(), qdriver.CSS("#editor"), "plain edit"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Press(t.Context(), qdriver.CSS("#name"), "Control+A"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Press(t.Context(), qdriver.CSS("#name"), "Z"); err != nil {
		t.Fatal(err)
	}
	evaluateJSON(t, p, "document.getElementById('name').value", `"Z"`)
	evaluateJSON(t, p, "document.getElementById('memo').value", `""`)
	evaluateJSON(t, p, "document.getElementById('editor').textContent", `"plain edit"`)
	if receipt, err := p.Fill(t.Context(), qdriver.CSS("#formatted"), "lower"); !errors.Is(err, qdriver.ErrFillRejected) || !receipt.InputCompleted {
		t.Fatalf("formatting was not reported as a completed but rejected edit: %+v, %v", receipt, err)
	}
	evaluateJSON(t, p, "window.formatEvents", "5")
	evaluateJSON(t, p, "document.getElementById('formatted').value", `"LOWER"`)
}

func TestInputRejectsUnsafeTargets(t *testing.T) {
	f := startChrome(t)
	b := attachDriver(t, f.endpoint)
	p := interactionPage(t, b, interactionHTML)
	for _, tc := range []struct {
		selector string
		fill     bool
		want     error
	}{
		{".duplicate", false, qdriver.ErrAmbiguous},
		{"#missing", false, qdriver.ErrElementNotFound},
		{"button > > input", false, qdriver.ErrInvalidLocator},
		{"#disabled", false, qdriver.ErrNotActionable},
		{"#aria", false, qdriver.ErrNotActionable},
		{"#hidden", false, qdriver.ErrNotActionable},
		{"#covered", false, qdriver.ErrNotActionable},
		{"#readonly", true, qdriver.ErrNotEditable},
		{"#number", true, qdriver.ErrNotEditable},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var receipt qdriver.ActionReceipt
			var err error
			if tc.fill {
				receipt, err = p.Fill(t.Context(), qdriver.CSS(tc.selector), "unexpected")
			} else {
				receipt, err = p.Click(t.Context(), qdriver.CSS(tc.selector))
			}
			if !errors.Is(err, tc.want) || receipt.InputStarted {
				t.Fatalf("receipt=%+v err=%v want=%v", receipt, err, tc.want)
			}
		})
	}
	if receipt, err := p.Click(t.Context(), qdriver.CSS("#hover")); !errors.Is(err, qdriver.ErrNotActionable) || receipt.InputCompleted {
		t.Fatalf("hover overlay was clicked: %+v, %v", receipt, err)
	}
	evaluateJSON(t, p, "window.coveredClicks||0", "0")
	evaluateJSON(t, p, "window.hovered||0", "0")
	if _, err := p.Press(t.Context(), qdriver.CSS("#name"), "NotAKey"); !errors.Is(err, qdriver.ErrUnsupported) {
		t.Fatalf("unsupported key accepted: %v", err)
	}
}

func TestRefProvenanceAndNavigation(t *testing.T) {
	f := startChrome(t)
	b := attachDriver(t, f.endpoint)
	p := interactionPage(t, b, interactionHTML)
	ref := findRef(t, takeSnapshot(t, p, qdriver.FullPage), "button", "Victim")
	other := interactionPage(t, b, `<button onclick="window.clicked=true">Victim</button>`)
	if receipt, err := other.Click(t.Context(), qdriver.ByRef(ref)); !errors.Is(err, qdriver.ErrWrongPage) || receipt.InputStarted {
		t.Fatalf("cross-page ref accepted: %+v, %v", receipt, err)
	}
	if _, err := p.Click(t.Context(), qdriver.CSS("#replace")); err != nil {
		t.Fatal(err)
	}
	if receipt, err := p.Click(t.Context(), qdriver.ByRef(ref)); !errors.Is(err, qdriver.ErrStaleRef) || receipt.InputStarted {
		t.Fatalf("removed ref retargeted: %+v, %v", receipt, err)
	}
	s := takeSnapshot(t, p, qdriver.FullPage)
	replacement := findRef(t, s, "button", "Victim")
	if replacement.String() == ref.String() {
		t.Fatal("replacement node reused the old ref number")
	}
	if _, err := p.Click(t.Context(), qdriver.CSS("#navigate")); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitForSelector(t.Context(), qdriver.CSS("#next")); err != nil {
		t.Fatal(err)
	}
	if receipt, err := p.Click(t.Context(), qdriver.ByRef(replacement)); !errors.Is(err, qdriver.ErrStaleRef) || receipt.InputStarted {
		t.Fatalf("old document ref accepted: %+v, %v", receipt, err)
	}
	next := findRef(t, takeSnapshot(t, p, qdriver.FullPage), "button", "Next")
	evaluateJSON(t, p, "1+1", "2")
	if _, err := p.Click(t.Context(), qdriver.ByRef(next)); !errors.Is(err, qdriver.ErrStaleRef) {
		t.Fatalf("Evaluate failed to invalidate refs: %v", err)
	}
}

func TestSnapshotScopesBudgetsAndWaits(t *testing.T) {
	f := startChrome(t)
	b := attachDriver(t, f.endpoint)
	p := interactionPage(t, b, interactionHTML)
	full := takeSnapshot(t, p, qdriver.FullPage)
	below := findRef(t, full, "button", "Below fold")
	viewport := takeSnapshot(t, p, qdriver.Viewport)
	if strings.Contains(viewport.Content, "Below fold") {
		t.Fatal("viewport included off-screen node")
	}
	if _, err := p.Click(t.Context(), qdriver.ByRef(below)); err != nil {
		t.Fatalf("partial snapshot lost an omitted ref or auto-scroll failed: %v", err)
	}
	if !strings.Contains(takeSnapshot(t, p, qdriver.Viewport).Content, "Below clicked") {
		t.Fatal("off-screen target was not scrolled into view and clicked")
	}
	limited, err := p.Snapshot(t.Context(), qdriver.SnapshotOptions{Scope: qdriver.FullPage, MaxNodes: 2, MaxBytes: 64})
	if err != nil || !limited.Truncated || len(limited.Nodes) > 2 || len(limited.Content) > 64 || !utf8.ValidString(limited.Content) {
		t.Fatalf("snapshot budget failed: %+v, %v", limited, err)
	}
	for _, node := range limited.Nodes {
		if node.Ref.String() != "" && !strings.Contains(limited.Content, node.Ref.String()) {
			t.Fatal("returned a ref missing from Content")
		}
	}
	if _, err := p.Click(t.Context(), qdriver.ByRef(below)); !errors.Is(err, qdriver.ErrStaleRef) {
		t.Fatalf("full snapshot did not replace the active set: %v", err)
	}
	waitPage := interactionPage(t, b, `<p>Waiting</p><script>setTimeout(()=>{const b=document.createElement('button');b.id='late';b.textContent='Ready';document.body.append(b)},150)</script>`)
	if err := waitPage.WaitForSelector(t.Context(), qdriver.CSS("#late")); err != nil {
		t.Fatal(err)
	}
	if err := waitPage.WaitForFunction(t.Context(), `document.getElementById('late').textContent==='Ready'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := waitPage.WaitForSelector(ctx, qdriver.CSS("#never")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait deadline: %v", err)
	}
	unsupported := interactionPage(t, b, `<iframe srcdoc="<button>Frame button</button>"></iframe><div id="host"></div><canvas></canvas><script>host.attachShadow({mode:'open'}).innerHTML='<button>Shadow button</button>'</script>`)
	u := takeSnapshot(t, unsupported, qdriver.FullPage)
	if slices.Contains(u.Unsupported, "frames") || slices.Contains(u.Unsupported, "shadow DOM") || !strings.Contains(u.Content, "Frame button") || !strings.Contains(u.Content, "Shadow button") || !slices.Contains(u.Unsupported, "canvas content") {
		t.Fatalf("frame/open-shadow capture or canvas limitation was incorrect: %+v", u)
	}
}

func TestDispatchedClickCancellationDoesNotReplay(t *testing.T) {
	f := startChrome(t)
	b := attachDriver(t, f.endpoint)
	started := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/started" {
			once.Do(func() { close(started) })
			_, _ = fmt.Fprint(w, "ok")
			return
		}
		_, _ = fmt.Fprint(w, `<button id="slow" onmousedown="window.presses=(window.presses||0)+1;fetch('/started');const until=performance.now()+400;while(performance.now()<until){}" onclick="window.clicks=(window.clicks||0)+1">Slow</button><button id="after" onclick="window.afterClicks=(window.afterClicks||0)+1">After</button>`)
	}))
	defer server.Close()
	p := newPage(t, newTask(t, b))
	if err := p.Goto(t.Context(), server.URL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type outcome struct {
		receipt qdriver.ActionReceipt
		err     error
	}
	finished := make(chan outcome, 1)
	go func() {
		receipt, err := p.Click(ctx, qdriver.CSS("#slow"))
		finished <- outcome{receipt, err}
	}()
	select {
	case <-started:
		cancel()
	case result := <-finished:
		t.Fatalf("click ended before mousedown signal: %+v, %v", result.receipt, result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("mouse press never reached the handler")
	}
	result := <-finished
	var operationErr *qdriver.OperationError
	if !errors.Is(result.err, context.Canceled) || !errors.As(result.err, &operationErr) || !operationErr.MayHaveEffects || !result.receipt.InputStarted {
		t.Fatalf("lost uncertain click outcome: %+v %v", result.receipt, result.err)
	}
	evaluateJSON(t, p, "window.presses", "1")
	if _, err := p.Click(t.Context(), qdriver.CSS("#after")); err != nil {
		t.Fatalf("input was left pressed: %v", err)
	}
	evaluateJSON(t, p, "window.afterClicks", "1")
	evaluateJSON(t, p, "(window.clicks||0)<=1", "true")
}

func TestSnapshotRetainedRefCapacity(t *testing.T) {
	f := startChrome(t)
	b, err := qdriver.Attach(t.Context(), f.endpoint, qdriver.Options{MaxRefs: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = b.Close(context.Background()) })
	p := interactionPage(t, b, `<button id="a" onclick="if(!document.getElementById('c')){const c=document.createElement('button');c.id='c';c.textContent='C';this.after(c)}">A</button><div style="height:1600px"></div><button id="b">B</button>`)
	a := findRef(t, takeSnapshot(t, p, qdriver.Viewport), "button", "A")
	if _, err := p.Click(t.Context(), qdriver.CSS("#b")); err != nil {
		t.Fatal(err)
	}
	oldB := findRef(t, takeSnapshot(t, p, qdriver.Viewport), "button", "B")
	if _, err := p.Click(t.Context(), qdriver.CSS("#a")); err != nil {
		t.Fatal(err)
	}
	s := takeSnapshot(t, p, qdriver.Viewport)
	currentA, currentC := findRef(t, s, "button", "A"), findRef(t, s, "button", "C")
	if a.String() != currentA.String() {
		t.Fatal("unmodified retained node lost its identity")
	}
	for _, ref := range []qdriver.Ref{currentA, currentC} {
		if _, err := p.Click(t.Context(), qdriver.ByRef(ref)); err != nil {
			t.Fatalf("capacity eviction invalidated a delivered ref: %s %v", ref, err)
		}
	}
	if _, err := p.Click(t.Context(), qdriver.ByRef(oldB)); !errors.Is(err, qdriver.ErrStaleRef) {
		t.Fatalf("omitted ref was not evicted at capacity: %v", err)
	}
}
