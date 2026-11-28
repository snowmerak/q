package qdriver_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	qdriver "github.com/snowmerak/q/v2/pkg/cdp"
)

func frameByName(t *testing.T, p *qdriver.Page, name string) qdriver.FrameInfo {
	t.Helper()
	frames, err := p.Frames(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range frames {
		if frame.Name == name {
			return frame
		}
	}
	t.Fatalf("frame %q missing: %+v", name, frames)
	return qdriver.FrameInfo{}
}

func makeFrameFixture(t *testing.T, b *qdriver.Browser) *qdriver.Page {
	t.Helper()
	var originA, originB, originC string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/":
			_, _ = fmt.Fprintf(w, `<!doctype html><style>body{margin:30px;font:16px sans-serif}iframe{width:460px;height:240px;border:7px solid teal;display:block;margin:20px 0 20px 55px}#local{transform:scale(.9);transform-origin:0 0}.remote-box{position:relative;transform:scale(.85);transform-origin:0 0;width:560px}.cover{position:absolute;inset:0;background:white;z-index:9}</style>
<button id="shared" onclick="document.body.dataset.shared='top'">Shared</button>
<button id="reload" onclick="document.getElementById('remote').src='%s/remote?v=two'">Reload remote</button>
<button id="swap" onclick="document.getElementById('remote').src='%s/swapped'">Swap renderer</button>
<button id="remove" onclick="document.getElementById('remote').remove()">Remove remote</button>
<button id="cover" onclick="document.getElementById('cover-frame').hidden=false">Cover remote</button>
<button id="rotate" onclick="document.getElementById('remote').style.rotate='10deg'">Rotate remote</button>
<output id="messages"></output>
<iframe id="local" name="local" src="%s/local"></iframe>
<div class="remote-box"><iframe id="remote" name="remote" src="%s/remote"></iframe><div hidden class="cover" id="cover-frame"></div></div>
<iframe id="second" name="second" src="%s/second"></iframe>
<script>window.events=[];addEventListener('message',e=>{events.push(e.data);document.getElementById('messages').textContent=JSON.stringify(events)})</script>`, originB, originA, originA, originB, originC)
		case "/local":
			_, _ = fmt.Fprintf(w, `<!doctype html><style>body{margin:18px}iframe{height:120px;width:300px;border:5px solid blue;margin:24px}</style><button id="local-action" onclick="top.postMessage('local-click','*')">Local button</button><iframe id="nested" name="nested" src="%s/nested"></iframe>`, originA)
		case "/nested":
			_, _ = fmt.Fprint(w, `<!doctype html><body style="margin:16px"><button onclick="top.postMessage('nested-click','*')">Nested button</button>`)
		case "/remote":
			version := r.URL.Query().Get("v")
			_, _ = fmt.Fprintf(w, `<!doctype html><body style="margin:14px"><label for="remote-name">Remote name</label><input id="remote-name" oninput="top.postMessage({value:this.value},'*')"><button class="cross-match" id="remote-action" onclick="top.postMessage('remote-click','*')">Remote button</button><button onclick="top.postMessage('remote-shared','*')">Shared</button><div id="version-%s">Version %s</div><iframe name="remote-nested" src="%s/remote-nested" style="width:300px;height:140px;border:4px solid purple;margin-top:25px"></iframe>`, version, version, originC)
		case "/remote-nested":
			_, _ = fmt.Fprint(w, `<!doctype html><button onclick="top.postMessage('remote-nested-click','*')">Remote nested</button>`)
		case "/second":
			_, _ = fmt.Fprint(w, `<!doctype html><button class="cross-match" onclick="top.postMessage('second-click','*')">Second remote</button>`)
		case "/swapped":
			_, _ = fmt.Fprint(w, `<!doctype html><button onclick="top.postMessage('swapped-click','*')">Swapped button</button>`)
		}
	}))
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	originA = "http://site-a.test:" + u.Port()
	originB = "http://site-b.test:" + u.Port()
	originC = "http://site-c.test:" + u.Port()
	task := newTask(t, b)
	t.Cleanup(func() {
		if _, err := task.Finish(context.Background()); err != nil {
			t.Error(err)
		}
	})
	p := newPage(t, task)
	if err := p.Goto(t.Context(), originA); err != nil {
		t.Fatal(err)
	}
	return p
}

func startFrameChrome(t *testing.T) *chromeFixture {
	t.Helper()
	return startChromeWithArgs(t, "--site-per-process", "--host-resolver-rules=MAP *.test 127.0.0.1", "--no-proxy-server")
}

func TestFrameSnapshotAndNativeInput(t *testing.T) {
	f := startFrameChrome(t)
	b := attachDriver(t, f.endpoint)
	p := makeFrameFixture(t, b)
	remote, local, second := frameByName(t, p, "remote"), frameByName(t, p, "local"), frameByName(t, p, "second")
	if !remote.OutOfProcess || !second.OutOfProcess || local.OutOfProcess {
		t.Fatalf("incorrect real renderer topology: remote=%+v local=%+v second=%+v", remote, local, second)
	}
	if nested := frameByName(t, p, "remote-nested"); !nested.OutOfProcess || nested.ParentID != remote.ID {
		t.Fatalf("nested OOPIF topology not established: %+v", nested)
	}
	actual, err := cdp.Call(t.Context(), f.control, target.GetTargets, target.GetTargetsParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{remote.ID, second.ID} {
		if !slices.ContainsFunc(actual.TargetInfos, func(info *target.Info) bool { return string(info.TargetID) == id && info.Type == "iframe" }) {
			t.Fatalf("no actual OOPIF target for %s", id)
		}
	}
	s := takeSnapshot(t, p, qdriver.FullPage)
	t.Logf("frames: %+v", s.Frames)
	if receipt, err := p.Click(t.Context(), qdriver.CSS(".cross-match")); !errors.Is(err, qdriver.ErrAmbiguous) || receipt.InputStarted {
		t.Fatalf("ambiguous child frames were guessed: %+v %v", receipt, err)
	}
	for _, name := range []string{"Local button", "Nested button", "Remote button", "Remote nested", "Second remote"} {
		ref := findRef(t, s, "button", name)
		if receipt, err := p.Click(t.Context(), qdriver.ByRef(ref)); err != nil || !receipt.InputCompleted {
			if events, err := p.Evaluate(t.Context(), "window.events"); err == nil {
				t.Logf("events on failure: %s", events)
			}
			t.Fatalf("%s ref=%s receipt=%+v err=%v", name, ref, receipt, err)
		}
	}
	name := findRef(t, s, "textbox", "Remote name")
	if receipt, err := p.Fill(t.Context(), qdriver.ByRef(name), "OOPIF 안녕"); err != nil || !receipt.InputCompleted {
		t.Fatalf("remote fill: %+v %v", receipt, err)
	}
	if _, err := p.Press(t.Context(), qdriver.ByRef(name), "Control+A"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Press(t.Context(), qdriver.ByRef(name), "Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(t.Context(), qdriver.Role("button", "Shared")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(t.Context(), qdriver.Role("button", "Shared").InFrame(remote.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(t.Context(), qdriver.Text("Nested button")); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitForSelector(t.Context(), qdriver.CSS("#remote-name").InFrame(remote.ID)); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitForFunction(t.Context(), "['local-click','nested-click','remote-click','remote-nested-click','second-click','remote-shared'].every(e=>window.events.includes(e))&&window.events.some(e=>e.value==='Z')"); err != nil {
		t.Fatal(err)
	}
	evaluateJSON(t, p, "document.body.dataset.shared", `"top"`)
	for _, event := range []string{"local-click", "nested-click", "remote-click", "remote-nested-click", "second-click", "remote-shared"} {
		evaluateJSON(t, p, "window.events.includes("+fmt.Sprintf("%q", event)+")", "true")
	}
	evaluateJSON(t, p, "window.events.some(e=>e.value==='Z')", "true")
}

func TestFrameNavigationRemovalAndOverlays(t *testing.T) {
	f := startFrameChrome(t)
	b := attachDriver(t, f.endpoint)
	p := makeFrameFixture(t, b)
	remote := frameByName(t, p, "remote")
	s := takeSnapshot(t, p, qdriver.FullPage)
	oldRemote := findRef(t, s, "button", "Remote button")
	local := findRef(t, s, "button", "Local button")
	if _, err := p.Click(t.Context(), qdriver.CSS("#reload")); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitForSelector(t.Context(), qdriver.CSS("#version-two").InFrame(remote.ID)); err != nil {
		t.Fatal(err)
	}
	if receipt, err := p.Click(t.Context(), qdriver.ByRef(oldRemote)); !errors.Is(err, qdriver.ErrStaleRef) || receipt.InputStarted {
		t.Fatalf("reloaded frame ref accepted: %+v %v", receipt, err)
	}
	if _, err := p.Click(t.Context(), qdriver.ByRef(local)); err != nil {
		t.Fatalf("unrelated frame ref invalidated: %v", err)
	}
	fresh := findRef(t, takeSnapshot(t, p, qdriver.FullPage), "button", "Remote button")
	if _, err := p.Click(t.Context(), qdriver.CSS("#cover")); err != nil {
		t.Fatal(err)
	}
	if receipt, err := p.Click(t.Context(), qdriver.ByRef(fresh)); !errors.Is(err, qdriver.ErrNotActionable) || receipt.InputStarted {
		t.Fatalf("ancestor frame overlay bypassed: %+v %v", receipt, err)
	}
	if _, err := p.Click(t.Context(), qdriver.CSS("#remove")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(t.Context(), qdriver.ByRef(fresh)); !errors.Is(err, qdriver.ErrStaleRef) {
		t.Fatalf("removed frame ref accepted: %v", err)
	}
	if _, err := p.Click(t.Context(), qdriver.CSS("button").InFrame(remote.ID)); !errors.Is(err, qdriver.ErrFrameNotFound) {
		t.Fatalf("removed explicit frame scope accepted: %v", err)
	}
}

func TestFrameRendererSwapAndSubtree(t *testing.T) {
	f := startFrameChrome(t)
	b := attachDriver(t, f.endpoint)
	p := makeFrameFixture(t, b)
	remote := frameByName(t, p, "remote")
	s := takeSnapshot(t, p, qdriver.FullPage)
	old := findRef(t, s, "button", "Remote button")
	var owner qdriver.Ref
	// Identify the exact owner via its subtree, without relying on ref numbering.
	for _, node := range s.Nodes {
		if !strings.EqualFold(node.Role, "iframe") {
			continue
		}
		sub, err := p.Snapshot(t.Context(), qdriver.SnapshotOptions{Scope: qdriver.Subtree, Root: node.Ref})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sub.Content, "Remote button") {
			owner = node.Ref
			break
		}
	}
	if owner.String() == "" {
		t.Fatal("no iframe root ref")
	}
	sub, err := p.Snapshot(t.Context(), qdriver.SnapshotOptions{Scope: qdriver.Subtree, Root: owner})
	if err != nil || !strings.Contains(sub.Content, "Remote button") || strings.Contains(sub.Content, "Local button") || strings.Contains(sub.Content, "Second remote") {
		t.Fatalf("incorrect subtree: %s %v", sub.Content, err)
	}
	if _, err := p.Click(t.Context(), qdriver.CSS("#swap")); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitForSelector(t.Context(), qdriver.Text("Swapped button").InFrame(remote.ID)); err != nil {
		t.Fatal(err)
	}
	if frameByName(t, p, "remote").OutOfProcess {
		t.Fatal("OOPIF did not swap back into the root renderer")
	}
	if _, err := p.Click(t.Context(), qdriver.ByRef(old)); !errors.Is(err, qdriver.ErrStaleRef) {
		t.Fatalf("renderer-local ref survived a session swap: %v", err)
	}
	if _, err := p.Click(t.Context(), qdriver.Role("button", "Swapped button").InFrame(remote.ID)); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitForFunction(t.Context(), "window.events.includes('swapped-click')"); err != nil {
		t.Fatal(err)
	}
	evaluateJSON(t, p, "window.events.includes('swapped-click')", "true")
}

func TestOpenShadowSelectorsAndSubtree(t *testing.T) {
	f := startChrome(t)
	b := attachDriver(t, f.endpoint)
	p := interactionPage(t, b, `<!doctype html><div id="host"></div><div id="closed"></div><script>
const s=host.attachShadow({mode:'open'});s.innerHTML='<label for="name">Shadow name</label><input id="name"><button id="save">Shadow save</button><div id="nested"></div><slot></slot>';
s.getElementById('save').onclick=()=>document.body.dataset.saved=s.getElementById('name').value;
const inner=s.getElementById('nested').attachShadow({mode:'open'});inner.innerHTML='<button id="deep">Deep shadow</button>';inner.getElementById('deep').onclick=()=>document.body.dataset.deep='yes';
const light=document.createElement('button');light.textContent='Slotted';light.onclick=()=>document.body.dataset.slotted='yes';host.append(light);
document.getElementById('closed').attachShadow({mode:'closed'}).innerHTML='<button>Closed secret</button><iframe srcdoc="<button>Closed frame secret</button>"></iframe>';
</script>`)
	s := takeSnapshot(t, p, qdriver.FullPage)
	if strings.Contains(s.Content, "Closed secret") || strings.Contains(s.Content, "Closed frame secret") || !slices.Contains(s.Unsupported, "closed shadow DOM") {
		t.Fatalf("closed shadow contents escaped boundary: %+v", s)
	}
	input := findRef(t, s, "textbox", "Shadow name")
	if _, err := p.Fill(t.Context(), qdriver.ByRef(input), "Shadow 안녕"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(t.Context(), qdriver.Role("button", "Shadow save")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(t.Context(), qdriver.CSS("#deep")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(t.Context(), qdriver.Text("Slotted")); err != nil {
		t.Fatal(err)
	}
	var host qdriver.Ref
	for _, node := range s.Nodes {
		if node.Role == "shadow_host" {
			host = node.Ref
			break
		}
	}
	if host.String() == "" {
		t.Fatal("no shadow host root ref")
	}
	sub, err := p.Snapshot(t.Context(), qdriver.SnapshotOptions{Scope: qdriver.Subtree, Root: host})
	if err != nil || !strings.Contains(sub.Content, "Deep shadow") || strings.Contains(sub.Content, "Closed secret") {
		t.Fatalf("shadow subtree: %s %v", sub.Content, err)
	}
	evaluateJSON(t, p, "document.body.dataset.saved", `"Shadow 안녕"`)
	evaluateJSON(t, p, "document.body.dataset.deep", `"yes"`)
	evaluateJSON(t, p, "document.body.dataset.slotted", `"yes"`)
	if _, err := p.Click(t.Context(), qdriver.Role("button", "Closed secret")); !errors.Is(err, qdriver.ErrElementNotFound) {
		t.Fatalf("role locator entered closed shadow: %v", err)
	}
}

func TestFrameLimitAndUnsupportedTransform(t *testing.T) {
	f := startFrameChrome(t)
	b, err := qdriver.Attach(t.Context(), f.endpoint, qdriver.Options{MaxFrames: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = b.Close(context.Background()) })
	p := makeFrameFixture(t, b)
	if _, err := p.Frames(t.Context()); !errors.Is(err, qdriver.ErrFrameLimit) {
		t.Fatalf("frame budget ignored: %v", err)
	}
	if _, err := p.Click(t.Context(), qdriver.CSS("button").InFrame("")); !errors.Is(err, qdriver.ErrFrameNotFound) {
		t.Fatalf("empty explicit frame scope fell back to the root: %v", err)
	}
	other := attachDriver(t, f.endpoint)
	p2 := makeFrameFixture(t, other)
	remote := findRef(t, takeSnapshot(t, p2, qdriver.FullPage), "button", "Remote button")
	if _, err := p2.Click(t.Context(), qdriver.CSS("#rotate")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if receipt, err := p2.Click(ctx, qdriver.ByRef(remote)); !errors.Is(err, qdriver.ErrUnsupported) || receipt.InputStarted {
		t.Fatalf("rotated frame was clicked at an unverified coordinate: %+v %v", receipt, err)
	}
}

func TestBorrowedPageKeepsOOPIFsOnRelease(t *testing.T) {
	f := startFrameChrome(t)
	b := attachDriver(t, f.endpoint)
	source := makeFrameFixture(t, b)
	address, err := source.URL(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	created, err := cdp.Call(t.Context(), f.control, target.CreateTarget, target.CreateTargetParams{URL: address})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = cdp.Call(ctx, f.control, target.CloseTarget, target.CloseTargetParams(created))
	})
	for round := range 2 {
		task := newTask(t, b)
		borrowed, err := task.Adopt(t.Context(), string(created.TargetID))
		if err != nil {
			t.Fatal(err)
		}
		if err := borrowed.WaitForSelector(t.Context(), qdriver.Role("button", "Remote button")); err != nil {
			t.Fatal(err)
		}
		remote := frameByName(t, borrowed, "remote")
		if !remote.OutOfProcess {
			t.Fatal("borrowed fixture has no real OOPIF")
		}
		if _, err := borrowed.Click(t.Context(), qdriver.Role("button", "Remote button").InFrame(remote.ID)); err != nil {
			t.Fatal(err)
		}
		if err := borrowed.WaitForFunction(t.Context(), fmt.Sprintf("window.events.filter(e=>e==='remote-click').length===%d", round+1)); err != nil {
			t.Fatal(err)
		}
		report, err := task.Finish(t.Context())
		if err != nil || !slices.Contains(report.Released, string(created.TargetID)) || len(report.Closed) != 0 {
			t.Fatalf("borrowed cleanup: %+v %v", report, err)
		}
		actual, err := cdp.Call(t.Context(), f.control, target.GetTargets, target.GetTargetsParams{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(actual.TargetInfos, func(info *target.Info) bool { return string(info.TargetID) == remote.ID && info.Type == "iframe" }) {
			t.Fatal("releasing a borrowed page closed its OOPIF")
		}
	}
}

func TestWaitForSelectorDuringOOPIFChurn(t *testing.T) {
	f := startFrameChrome(t)
	b := attachDriver(t, f.endpoint)
	p := makeFrameFixture(t, b)
	if _, err := p.Evaluate(t.Context(), `(()=>{
	 const first=document.querySelector('#remote'),host=first.parentElement,template=first.cloneNode(false);let n=0;
	 const timer=setInterval(()=>{
	   document.querySelector('#remote')?.remove();const child=template.cloneNode(false);
	   child.src=template.src+'?v='+(++n===12?'ready':n);host.prepend(child);
	   if(n===12)clearInterval(timer);
	 },16);
	})()`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	if err := p.WaitForSelector(ctx, qdriver.Text("Version ready")); err != nil {
		t.Fatalf("frame replacement escaped an observational wait: %v", err)
	}
	if remote := frameByName(t, p, "remote"); !remote.OutOfProcess {
		t.Fatal("churn fixture lost its OOPIF topology")
	}
}
