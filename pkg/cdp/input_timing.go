package qdriver

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

// InputTiming controls pointer interpolation and keyboard pacing. Nil in Options
// selects the documented defaults; an explicit zero value selects immediate
// input. Jitter varies each hold/interval and the total pointer duration by at
// most this amount, clamped at zero. These settings do not guarantee a site's
// bot verdict. They are fixed for the lifetime of an attached connection.
type InputTiming struct {
	MouseSteps     int
	MouseDuration  time.Duration
	ButtonHold     time.Duration
	KeyHold        time.Duration
	KeyInterval    time.Duration
	Jitter         time.Duration
	MouseCurve     bool    // Curve geometry and distance/target-size timing.
	KeyTransitions bool    // QWERTY hand/finger/distance model, using onset intervals.
	KeyOverlap     float64 // Probability of eligible overlapping keys, from 0 to 1.
}

func inputTiming(requested *InputTiming) (InputTiming, error) {
	if requested == nil {
		return InputTiming{MouseSteps: 12, MouseDuration: 180 * time.Millisecond, ButtonHold: 40 * time.Millisecond, KeyHold: 35 * time.Millisecond, KeyInterval: 65 * time.Millisecond, Jitter: 15 * time.Millisecond, MouseCurve: true, KeyTransitions: true, KeyOverlap: 0.15}, nil
	}
	value := *requested
	if value.MouseSteps < 0 || value.MouseSteps > 120 {
		return value, errors.New("qdriver: mouse steps must be between 0 and 120")
	}
	if !(value.KeyOverlap >= 0 && value.KeyOverlap <= 1) {
		return value, errors.New("qdriver: key overlap probability must be between 0 and 1")
	}
	for _, delay := range []time.Duration{value.MouseDuration, value.ButtonHold, value.KeyHold, value.KeyInterval, value.Jitter} {
		if delay < 0 || delay > 2*time.Second {
			return value, errors.New("qdriver: input timing durations must be between 0 and 2 seconds")
		}
	}
	return value, nil
}

func (p *Page) timing() InputTiming {
	timing, _ := inputTiming(p.task.browser.options.InputTiming)
	return timing
}

func jitterDelay(base, jitter time.Duration) time.Duration {
	if jitter > 0 {
		base += time.Duration(rand.Int64N(int64(2*jitter)+1)) - jitter
	}
	if base < 0 {
		return 0
	}
	return base
}

func waitInput(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func pointerPoint(fromX, fromY, x, y float64, step, steps int) (float64, float64) {
	t := float64(step) / float64(steps)
	t = t * t * (3 - 2*t) // Ease in/out along the line; end at the exact target.
	return fromX + (x-fromX)*t, fromY + (y-fromY)*t
}

func (p *Page) movePointer(ctx context.Context, node resolvedElement, state elementState) error {
	timing := p.timing()
	x, y := state.X, state.Y
	session := p.frames[node.frame].doc.sessionKey
	fromX, fromY := p.pointerX, p.pointerY
	if p.pointerSession != session {
		// CDP cannot read the OS pointer. Never interpolate using another
		// renderer's coordinate system; start at its viewport origin instead.
		fromX, fromY = 0, 0
	}
	frame := p.frames[node.frame]
	for frame.info.ParentID != "" {
		parent := p.frames[cdp.FrameID(frame.info.ParentID)]
		if parent == nil || parent.doc.sessionKey != session {
			break
		}
		frame = parent
	}
	path := planPointer(timing, fromX, fromY, x, y, state.Width, state.Height, frame.width, frame.height)
	for i, point := range path {
		if i > 0 {
			if err := waitInput(ctx, point.delay); err != nil {
				return err
			}
		}
		if _, err := cdp.Call(ctx, node.session, input.DispatchMouseEvent, input.DispatchMouseEventParams{Type: input.DispatchMouseEventTypeMouseMoved, X: point.x, Y: point.y}); err != nil {
			p.pointerSession = "" // The last acknowledged position is now uncertain.
			return err
		}
		p.pointerSession, p.pointerX, p.pointerY = session, point.x, point.y
	}
	return nil
}

func (p *Page) checkTypingTarget(ctx context.Context, t *chromedp.Target, node resolvedElement) error {
	if err := p.sameDocument(ctx, t, node); err != nil {
		return err
	}
	state, err := p.inspectElement(ctx, t, node, "inspect")
	if err != nil {
		return err
	}
	if err := actionable(state, true, true); err != nil {
		return err
	}
	if !state.Focused {
		return ErrNotActionable
	}
	return nil
}

// Fill keeps replacement semantics while emitting a key sequence for each
// character. Controls/Unicode that have no US physical-key mapping use native
// text insertion between Unidentified keydown/up; no IME is synthesized.
func (p *Page) typeReplacement(ctx context.Context, t *chromedp.Target, node resolvedElement, before, text string) error {
	if before != "" {
		if err := p.dispatchKey(ctx, node.session, keyStroke{key: "Backspace", code: "Backspace", virtual: 8}); err != nil {
			return err
		}
	}
	timing := p.timing()
	if timing.KeyTransitions || timing.KeyOverlap > 0 {
		return p.typeModeled(ctx, t, node, text)
	}
	first := true
	for _, r := range text {
		if !first {
			if err := waitInput(ctx, jitterDelay(timing.KeyInterval, timing.Jitter)); err != nil {
				return err
			}
		}
		first = false
		if err := p.checkTypingTarget(ctx, t, node); err != nil {
			return err
		}
		key, physical := characterKey(r)
		if physical {
			if err := p.dispatchKey(ctx, node.session, key); err != nil {
				return err
			}
		} else if err := p.insertCharacter(ctx, t, node, string(r)); err != nil {
			return err
		}
	}
	return nil
}

func characterKey(r rune) (keyStroke, bool) {
	key := keyStroke{key: string(r), text: string(r)}
	switch {
	case r >= 'a' && r <= 'z':
		key.code, key.virtual = "Key"+string(r-'a'+'A'), int64(r-'a'+'A')
	case r >= 'A' && r <= 'Z':
		key.code, key.virtual, key.modifiers = "Key"+string(r), int64(r), 8
	case r >= '0' && r <= '9':
		key.code, key.virtual = "Digit"+string(r), int64(r)
	case r == ' ':
		key.code, key.virtual = "Space", 32
	default:
		shiftedDigits := ")!@#$%^&*("
		for i, shifted := range shiftedDigits {
			if r == shifted {
				key.code, key.virtual, key.modifiers = "Digit"+string(rune('0'+i)), int64('0'+i), 8
				return key, true
			}
		}
		for _, entry := range []struct {
			plain, shifted rune
			code           string
			virtual        int64
		}{{'-', '_', "Minus", 189}, {'=', '+', "Equal", 187}, {'[', '{', "BracketLeft", 219}, {']', '}', "BracketRight", 221}, {'\\', '|', "Backslash", 220}, {';', ':', "Semicolon", 186}, {'\'', '"', "Quote", 222}, {',', '<', "Comma", 188}, {'.', '>', "Period", 190}, {'/', '?', "Slash", 191}, {'`', '~', "Backquote", 192}} {
			if r == entry.plain || r == entry.shifted {
				key.code, key.virtual = entry.code, entry.virtual
				if r == entry.shifted {
					key.modifiers = 8
				}
				return key, true
			}
		}
		return keyStroke{}, false
	}
	return key, true
}

func (p *Page) insertCharacter(ctx context.Context, t *chromedp.Target, node resolvedElement, text string) error {
	key := input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeRawKeyDown, Key: "Unidentified"}
	_, err := cdp.Call(ctx, node.session, input.DispatchKeyEvent, key)
	if err == nil {
		err = p.checkTypingTarget(ctx, t, node)
	}
	if err == nil {
		_, err = cdp.Call(ctx, node.session, input.InsertText, input.InsertTextParams{Text: text})
	}
	if err == nil {
		timing := p.timing()
		err = waitInput(ctx, jitterDelay(timing.KeyHold, timing.Jitter))
	}
	key.Type = input.DispatchKeyEventTypeKeyUp
	if err != nil {
		p.releaseKey(ctx, node.session, key)
		return err
	}
	_, err = cdp.Call(ctx, node.session, input.DispatchKeyEvent, key)
	return err
}
