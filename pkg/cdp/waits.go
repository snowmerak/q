package qdriver

import (
	"context"
	"errors"
	"time"

	"github.com/chromedp/chromedp"
)

// WaitForSelector waits for one visible element in the locator's frame scope.
// With no explicit scope, top-document matches take precedence. Zero matches
// are retried; ambiguity and stale refs fail immediately. It does not dispatch
// input, focus, or scroll. The operation context bounds the entire wait.
func (p *Page) WaitForSelector(ctx context.Context, locator Locator) error {
	_, err := runPage(ctx, p, "wait for selector", false, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		return poll(ctx, func() (bool, error) {
			node, err := p.resolve(ctx, t, locator)
			if errors.Is(err, ErrElementNotFound) || errors.Is(err, ErrDocumentChanged) || errors.Is(err, ErrFrameNotFound) {
				return false, nil
			}
			if err != nil {
				// A frame can disappear between discovery and the AX/DOM query.
				// Retry only this observation for selector-based waits. Ref waits
				// keep their exact-node failure semantics; no input is dispatched.
				if locator.ref.page == nil && frameLifecycleError(err) {
					return false, nil
				}
				return false, err
			}
			defer p.releaseObject(ctx, node.session, node.object)
			state, err := p.inspectElement(ctx, t, node, "inspect")
			if err != nil && locator.ref.page == nil && frameLifecycleError(err) {
				return false, nil
			}
			return state.Visible, err
		})
	}))
	return err
}

// WaitForFunction polls a JavaScript expression until it is truthy. Predicates
// should be synchronous and side-effect-free; arbitrary JavaScript may still
// mutate the page, so this operation invalidates refs and reports possible
// effects on failure just like Evaluate. It does not await returned Promises.
func (p *Page) WaitForFunction(ctx context.Context, expression string) error {
	_, err := runPage(ctx, p, "wait for function", true, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		p.invalidateRefs()
		return poll(ctx, func() (bool, error) {
			return chromedp.Evaluate[bool]("Boolean("+expression+")")(ctx, t)
		})
	}))
	return err
}

func poll(ctx context.Context, check func() (bool, error)) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, err := check()
		if err != nil || ready {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
