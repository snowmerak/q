package qdriver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	qdriver "github.com/snowmerak/q/v2/pkg/cdp"
)

func pacedDriver(t *testing.T, endpoint string, timing qdriver.InputTiming) *qdriver.Browser {
	t.Helper()
	b, err := qdriver.Attach(t.Context(), endpoint, qdriver.Options{InputTiming: &timing})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := b.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return b
}

func TestPacedFillEventsAndPointerInterpolation(t *testing.T) {
	f := startChrome(t)
	b := pacedDriver(t, f.endpoint, qdriver.InputTiming{MouseSteps: 8, MouseDuration: 160 * time.Millisecond, ButtonHold: 25 * time.Millisecond, KeyHold: 30 * time.Millisecond, KeyInterval: 40 * time.Millisecond, Jitter: 8 * time.Millisecond})
	p := interactionPage(t, b, `<!doctype html><style>button{position:absolute;width:100px;height:40px}#a{left:20px;top:30px}#b{left:420px;top:230px}textarea{position:absolute;top:300px}</style><button id="a">A</button><button id="b" onclick="window.clicks++">B</button><textarea id="text">old</textarea><script>
window.keys=[];window.moves=[];window.clicks=0;
for(const type of ['keydown','keypress','keyup','input']) document.addEventListener(type,e=>keys.push({type,key:e.key||'',code:e.code||'',shift:e.shiftKey||false,trusted:e.isTrusted,time:e.timeStamp}));
document.addEventListener('mousemove',e=>moves.push({x:e.clientX,y:e.clientY,time:e.timeStamp,trusted:e.isTrusted}));</script>`)
	if _, err := p.Click(t.Context(), qdriver.CSS("#a")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Evaluate(t.Context(), "moves=[]"); err != nil {
		t.Fatal(err)
	}
	if receipt, err := p.Click(t.Context(), qdriver.CSS("#b")); err != nil || !receipt.InputCompleted {
		t.Fatalf("interpolated click: %+v %v", receipt, err)
	}
	var motion struct {
		Moves []struct {
			X, Y, Time float64
			Trusted    bool
		}
		Clicks int
	}
	value, err := p.Evaluate(t.Context(), "({moves,clicks})")
	if err != nil || json.Unmarshal(value, &motion) != nil || len(motion.Moves) < 4 || motion.Clicks != 1 {
		t.Fatalf("missing pointer interpolation: %s %v", value, err)
	}
	for i, point := range motion.Moves {
		if !point.Trusted || i > 0 && (point.X < motion.Moves[i-1].X || point.Y < motion.Moves[i-1].Y) {
			t.Fatalf("unexpected pointer path: %+v", motion.Moves)
		}
	}
	if elapsed := motion.Moves[len(motion.Moves)-1].Time - motion.Moves[0].Time; elapsed < 100 {
		t.Fatalf("pointer motion was instantaneous: %fms", elapsed)
	}
	text := "aA1+! 안녕🧪\n\t"
	if receipt, err := p.Fill(t.Context(), qdriver.CSS("#text"), text); err != nil || !receipt.InputCompleted {
		t.Fatalf("paced Unicode fill: %+v %v", receipt, err)
	}
	var typed struct {
		Value string
		Keys  []struct {
			Type, Key, Code string
			Shift, Trusted  bool
			Time            float64
		}
	}
	value, err = p.Evaluate(t.Context(), "({value:document.querySelector('textarea').value,keys})")
	if err != nil || json.Unmarshal(value, &typed) != nil || typed.Value != text {
		t.Fatalf("typed text changed: %s %v", value, err)
	}
	var down, up float64
	shiftedPlus, unidentified, keyPress := false, false, false
	for _, event := range typed.Keys {
		if !event.Trusted {
			t.Fatalf("synthetic DOM event: %+v", event)
		}
		if event.Type == "keydown" && event.Key == "a" {
			down = event.Time
		}
		if event.Type == "keyup" && event.Key == "a" {
			up = event.Time
		}
		if event.Type == "keydown" && event.Key == "+" && event.Code == "Equal" && event.Shift {
			shiftedPlus = true
		}
		if event.Key == "Unidentified" {
			unidentified = true
		}
		if event.Type == "keypress" {
			keyPress = true
		}
	}
	if up-down < 18 || !shiftedPlus || !unidentified || !keyPress {
		t.Fatalf("missing native key sequence/hold: down=%f up=%f plus=%t fallback=%t keypress=%t", down, up, shiftedPlus, unidentified, keyPress)
	}
}

func TestPacedFillStopsOnFocusChange(t *testing.T) {
	f := startChrome(t)
	for _, modeled := range []bool{false, true} {
		t.Run(fmt.Sprintf("modeled=%t", modeled), func(t *testing.T) {
			b := pacedDriver(t, f.endpoint, qdriver.InputTiming{KeyHold: 20 * time.Millisecond, KeyInterval: 20 * time.Millisecond, KeyTransitions: modeled})
			p := interactionPage(t, b, `<!doctype html><input id="source" oninput="document.querySelector('#other').focus()"><input id="other">`)
			receipt, err := p.Fill(t.Context(), qdriver.CSS("#source"), "ab")
			if !errors.Is(err, qdriver.ErrNotActionable) || !receipt.InputStarted || receipt.InputCompleted {
				t.Fatalf("typing continued after focus moved: %+v %v", receipt, err)
			}
			evaluateJSON(t, p, "[document.querySelector('#source').value,document.querySelector('#other').value]", `["a",""]`)
		})
	}
}

func TestCurvedPointerNativePath(t *testing.T) {
	f := startChrome(t)
	b := pacedDriver(t, f.endpoint, qdriver.InputTiming{MouseSteps: 16, MouseDuration: 180 * time.Millisecond, MouseCurve: true})
	p := interactionPage(t, b, `<!doctype html><style>button{position:absolute;left:400px;top:200px;width:100px;height:40px}</style><button onclick="clicks++">Target</button><script>window.moves=[];window.clicks=0;document.addEventListener('mousemove',e=>moves.push({x:e.clientX,y:e.clientY,trusted:e.isTrusted}));</script>`)
	if receipt, err := p.Click(t.Context(), qdriver.CSS("button")); err != nil || !receipt.InputCompleted {
		t.Fatalf("curved click: %+v %v", receipt, err)
	}
	var got struct {
		Moves []struct {
			X, Y    float64
			Trusted bool
		}
		Clicks int
	}
	value, err := p.Evaluate(t.Context(), "({moves,clicks})")
	if err != nil || json.Unmarshal(value, &got) != nil || len(got.Moves) < 4 || got.Clicks != 1 {
		t.Fatalf("missing curved native motion: %s %v", value, err)
	}
	var deviation float64
	for _, point := range got.Moves {
		if !point.Trusted {
			t.Fatal("pointer event was not trusted")
		}
		deviation = math.Max(deviation, math.Abs(point.X*220-point.Y*450)/math.Hypot(450, 220))
	}
	last := got.Moves[len(got.Moves)-1]
	if deviation < 2 || math.Abs(last.X-450) > 1 || math.Abs(last.Y-220) > 1 {
		t.Fatalf("native path missed curve or endpoint: deviation=%f last=%+v", deviation, last)
	}
}

func TestModeledFillRolloverAndUnicodeBoundary(t *testing.T) {
	f := startChrome(t)
	b := pacedDriver(t, f.endpoint, qdriver.InputTiming{KeyHold: 300 * time.Millisecond, KeyInterval: 80 * time.Millisecond, KeyTransitions: true, KeyOverlap: 1})
	p := interactionPage(t, b, `<!doctype html><input id="text"><script>window.events=[];for(const type of ['keydown','keyup'])document.addEventListener(type,e=>events.push({type,key:e.key,shift:e.shiftKey}));</script>`)
	if receipt, err := p.Fill(t.Context(), qdriver.CSS("#text"), "FJ한!"); err != nil || !receipt.InputCompleted {
		t.Fatalf("modeled fill: %+v %v", receipt, err)
	}
	var result struct {
		Value  string
		Events []struct {
			Type, Key string
			Shift     bool
		}
	}
	data, err := p.Evaluate(t.Context(), "({value:document.querySelector('input').value,events})")
	if err != nil || json.Unmarshal(data, &result) != nil || result.Value != "FJ한!" {
		t.Fatalf("rollover changed text: %s %v", data, err)
	}
	index := func(kind, key string) int {
		for i, e := range result.Events {
			if e.Type == kind && e.Key == key {
				return i
			}
		}
		return -1
	}
	fd, jd, fu, ju := index("keydown", "F"), index("keydown", "J"), index("keyup", "F"), index("keyup", "J")
	if fd < 0 || jd <= fd || fu <= jd || ju <= fu {
		t.Fatalf("keys did not overlap on the timeline: %s", data)
	}
	if u := index("keydown", "Unidentified"); u <= ju {
		t.Fatalf("Unicode fallback started with physical keys still held: %s", data)
	}
}

func TestModeledFillCancellationReleasesOverlappingKeysAndShift(t *testing.T) {
	f := startChrome(t)
	b := pacedDriver(t, f.endpoint, qdriver.InputTiming{KeyHold: 600 * time.Millisecond, KeyInterval: 80 * time.Millisecond, KeyTransitions: true, KeyOverlap: 1})
	pressed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/second" {
			select {
			case pressed <- struct{}{}:
			default:
			}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><input id="text"><script>window.downs=[];window.ups=[];document.addEventListener('keydown',e=>{downs.push(e.key);if(e.key==='J')fetch('/second')});document.addEventListener('keyup',e=>ups.push(e.key));</script>`)
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
	done := make(chan outcome, 1)
	go func() { receipt, err := p.Fill(ctx, qdriver.CSS("#text"), "FJK"); done <- outcome{receipt, err} }()
	select {
	case <-pressed:
		cancel()
	case <-time.After(4 * time.Second):
		t.Fatal("second key was not pressed")
	}
	got := <-done
	if !errors.Is(got.err, context.Canceled) || !got.receipt.InputStarted || got.receipt.InputCompleted {
		t.Fatalf("cancelled rollover: %+v %v", got.receipt, got.err)
	}
	evaluateJSON(t, p, "({value:document.querySelector('input').value,downs,ups})", `{"value":"FJ","downs":["Shift","F","J"],"ups":["J","F","Shift"]}`)
}

func TestPacedFillCancellationReleasesKeyWithoutReplay(t *testing.T) {
	f := startChrome(t)
	b := pacedDriver(t, f.endpoint, qdriver.InputTiming{KeyHold: 400 * time.Millisecond, KeyInterval: 100 * time.Millisecond})
	pressed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pressed" {
			select {
			case pressed <- struct{}{}:
			default:
			}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><input id="text"><script>window.downs=0;window.ups=0;document.addEventListener('keydown',()=>{downs++;fetch('/pressed')});document.addEventListener('keyup',()=>ups++);</script>`)
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
	result := make(chan outcome, 1)
	go func() { r, e := p.Fill(ctx, qdriver.CSS("#text"), "abc"); result <- outcome{r, e} }()
	select {
	case <-pressed:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("key did not go down")
	}
	got := <-result
	if !errors.Is(got.err, context.Canceled) || !got.receipt.InputStarted || got.receipt.InputCompleted {
		t.Fatalf("cancelled typing: %+v %v", got.receipt, got.err)
	}
	evaluateJSON(t, p, "({value:document.querySelector('input').value,downs,ups})", `{"value":"a","downs":1,"ups":1}`)
}
