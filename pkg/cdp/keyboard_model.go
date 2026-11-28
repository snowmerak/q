package qdriver

import (
	"context"
	"math"
	"math/rand/v2"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

type keyLocation struct {
	x, y         float64
	hand, finger int
}

func qwertyLocation(code string) (keyLocation, bool) {
	rows := []struct {
		codes   []string
		fingers []int
		offset  float64
	}{
		{[]string{"Backquote", "Digit1", "Digit2", "Digit3", "Digit4", "Digit5", "Digit6", "Digit7", "Digit8", "Digit9", "Digit0", "Minus", "Equal"}, []int{-4, -4, -3, -2, -1, -1, 1, 1, 2, 3, 4, 4, 4}, 0},
		{[]string{"KeyQ", "KeyW", "KeyE", "KeyR", "KeyT", "KeyY", "KeyU", "KeyI", "KeyO", "KeyP", "BracketLeft", "BracketRight", "Backslash"}, []int{-4, -3, -2, -1, -1, 1, 1, 2, 3, 4, 4, 4, 4}, 1.5},
		{[]string{"KeyA", "KeyS", "KeyD", "KeyF", "KeyG", "KeyH", "KeyJ", "KeyK", "KeyL", "Semicolon", "Quote"}, []int{-4, -3, -2, -1, -1, 1, 1, 2, 3, 4, 4}, 1.75},
		{[]string{"KeyZ", "KeyX", "KeyC", "KeyV", "KeyB", "KeyN", "KeyM", "Comma", "Period", "Slash"}, []int{-4, -3, -2, -1, -1, 1, 1, 2, 3, 4}, 2.25},
	}
	if code == "Space" {
		return keyLocation{6.25, 4, 0, 0}, true
	}
	for row, values := range rows {
		for column, key := range values.codes {
			if code == key {
				finger := values.fingers[column]
				hand := 1
				if finger < 0 {
					hand = -1
				}
				return keyLocation{float64(column) + values.offset, float64(row), hand, finger}, true
			}
		}
	}
	return keyLocation{}, false
}

// Heuristic onset-to-onset timing, not a fitted population model. Geometry is
// expressed in key widths. Text production remains characterKey's job.
func transitionInterval(previous, next keyStroke, base time.Duration) time.Duration {
	a, aok := qwertyLocation(previous.code)
	b, bok := qwertyLocation(next.code)
	if !aok || !bok {
		return base
	}
	ms := float64(base) / float64(time.Millisecond)
	ms += 6*math.Hypot(a.x-b.x, a.y-b.y) + 8*math.Abs(a.y-b.y)
	switch {
	case previous.code == next.code:
		ms += 20
	case a.finger != 0 && a.finger == b.finger:
		ms += 40
	case a.hand != 0 && a.hand == b.hand:
		ms += 10
	case a.hand != 0 && b.hand != 0:
		ms -= 8
	}
	if next.modifiers&8 != 0 && previous.modifiers&8 == 0 {
		ms += 18
	}
	return time.Duration(math.Max(0, ms) * float64(time.Millisecond))
}

func overlapEligible(previous, next keyStroke) bool {
	a, aok := qwertyLocation(previous.code)
	b, bok := qwertyLocation(next.code)
	return aok && bok && a.hand != 0 && b.hand != 0 && a.hand != b.hand && previous.code != next.code && previous.modifiers == next.modifiers
}

type heldKey struct {
	key       keyStroke
	releaseAt time.Time
}
type keyboardStream struct {
	page    *Page
	session cdp.Session
	held    []heldKey
	shift   bool
}

func shiftEvent(down bool) input.DispatchKeyEventParams {
	e := input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: "Shift", Code: "ShiftLeft", WindowsVirtualKeyCode: 16}
	if down {
		e.Type = input.DispatchKeyEventTypeRawKeyDown
		e.Modifiers = 8
	}
	return e
}

func (k *keyboardStream) down(ctx context.Context, key keyStroke, hold time.Duration) error {
	if key.modifiers&8 != 0 && !k.shift {
		k.shift = true // A failed send may still have pressed the modifier.
		if _, err := cdp.Call(ctx, k.session, input.DispatchKeyEvent, shiftEvent(true)); err != nil {
			return err
		}
	}
	k.held = append(k.held, heldKey{key, time.Now().Add(hold)})
	params := input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyDown, Key: key.key, Code: key.code, Text: key.text, UnmodifiedText: key.text, WindowsVirtualKeyCode: key.virtual, Modifiers: key.modifiers}
	if _, err := cdp.Call(ctx, k.session, input.DispatchKeyEvent, params); err != nil {
		return err
	}
	k.held[len(k.held)-1].releaseAt = time.Now().Add(hold)
	return nil
}

func (k *keyboardStream) up(ctx context.Context, index int) error {
	key := k.held[index].key
	params := input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: key.key, Code: key.code, WindowsVirtualKeyCode: key.virtual}
	if k.shift {
		params.Modifiers = 8
	}
	if _, err := cdp.Call(ctx, k.session, input.DispatchKeyEvent, params); err != nil {
		return err
	}
	k.held = append(k.held[:index], k.held[index+1:]...)
	if len(k.held) == 0 && k.shift {
		if _, err := cdp.Call(ctx, k.session, input.DispatchKeyEvent, shiftEvent(false)); err != nil {
			return err
		}
		k.shift = false
	}
	return nil
}

func (k *keyboardStream) earliest() int {
	index := 0
	for i := 1; i < len(k.held); i++ {
		if k.held[i].releaseAt.Before(k.held[index].releaseAt) {
			index = i
		}
	}
	return index
}

// Drive releases and the next onset on one timeline, without detached timers.
func (k *keyboardStream) advance(ctx context.Context, deadline time.Time) error {
	for len(k.held) > 0 {
		i := k.earliest()
		releaseAt := k.held[i].releaseAt
		if releaseAt.After(deadline) {
			break
		}
		if err := waitInput(ctx, time.Until(releaseAt)); err != nil {
			return err
		}
		if err := k.up(ctx, i); err != nil {
			return err
		}
	}
	return waitInput(ctx, time.Until(deadline))
}

func (k *keyboardStream) finish(ctx context.Context) error {
	for len(k.held) > 0 {
		if err := k.advance(ctx, k.held[k.earliest()].releaseAt); err != nil {
			return err
		}
	}
	return nil
}

func (k *keyboardStream) cleanup(ctx context.Context) {
	keys := make([]input.DispatchKeyEventParams, 0, len(k.held)+1)
	for i := len(k.held) - 1; i >= 0; i-- {
		key := k.held[i].key
		up := input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: key.key, Code: key.code, WindowsVirtualKeyCode: key.virtual}
		if k.shift {
			up.Modifiers = 8
		}
		keys = append(keys, up)
	}
	if k.shift {
		keys = append(keys, shiftEvent(false))
	}
	k.page.releaseKeys(ctx, k.session, keys)
}

func (p *Page) typeModeled(ctx context.Context, t *chromedp.Target, node resolvedElement, text string) (resultErr error) {
	timing := p.timing()
	stream := keyboardStream{page: p, session: node.session}
	defer func() {
		if resultErr != nil {
			stream.cleanup(ctx)
		}
	}()
	var previous keyStroke
	var onset time.Time
	var previousHold time.Duration
	for _, r := range text {
		key, physical := characterKey(r)
		if !onset.IsZero() {
			interval := timing.KeyInterval
			if timing.KeyTransitions && physical {
				interval = transitionInterval(previous, key, interval)
			}
			interval = jitterDelay(interval, timing.Jitter)
			if physical && overlapEligible(previous, key) && len(stream.held) > 0 && rand.Float64() < timing.KeyOverlap {
				interval = previousHold * 2 / 3
			}
			if err := stream.advance(ctx, onset.Add(interval)); err != nil {
				return err
			}
		}
		// Keep at most two main keys held, and flush before changing modifier
		// state, repeating a physical key, or entering the Unicode/IME fallback.
		flush := !physical || len(stream.held) >= 2 || (stream.shift != (key.modifiers&8 != 0))
		for _, held := range stream.held {
			flush = flush || held.key.code == key.code
		}
		if flush {
			if err := stream.finish(ctx); err != nil {
				return err
			}
		}
		if err := p.checkTypingTarget(ctx, t, node); err != nil {
			return err
		}
		if err := stream.advance(ctx, time.Now()); err != nil {
			return err
		}
		if physical {
			hold := jitterDelay(timing.KeyHold, timing.Jitter)
			if err := stream.down(ctx, key, hold); err != nil {
				return err
			}
			previous, previousHold = key, hold
		} else {
			if err := p.insertCharacter(ctx, t, node, string(r)); err != nil {
				return err
			}
			previous = keyStroke{}
		}
		onset = time.Now()
	}
	return stream.finish(ctx)
}
