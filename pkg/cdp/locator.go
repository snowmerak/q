package qdriver

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Ref is an opaque, page-scoped node handle. Its zero value is invalid.
// A displayed @N number is meaningful only in its originating snapshot/page.
type Ref struct {
	page *Page
	id   uint64
}

func (r Ref) String() string {
	if r.id == 0 {
		return ""
	}
	return fmt.Sprintf("@%d", r.id)
}

// MarshalText preserves the display label when encoding a snapshot as JSON.
func (r Ref) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// Locator selects a node by CSS, exact normalized text, AX role/name, or Ref.
type Locator struct {
	css      string
	ref      Ref
	kind     string
	text     string
	role     string
	frame    string
	frameSet bool
}

// CSS pierces nested open shadow roots. The top document takes precedence;
// only zero matches there enables a unique-match search across child frames.
func CSS(selector string) Locator { return Locator{css: selector, kind: "css"} }

// Text matches exact whitespace-normalized text on the smallest matching DOM
// elements, including open shadow roots. It is not an accessible-name matcher.
func Text(text string) Locator { return Locator{kind: "text", text: text} }

// Role matches an exact accessibility role and accessible name.
func Role(role, name string) Locator { return Locator{kind: "role", role: role, text: name} }

// InFrame restricts a non-ref locator to one current frame ID, without falling
// back to other frames. A Ref already carries its own document provenance.
func (l Locator) InFrame(id string) Locator { l.frame = id; l.frameSet = true; return l }

// ByRef selects the original observed node, never a role/name substitute.
func ByRef(ref Ref) Locator { return Locator{ref: ref} }

type refEntry struct {
	backend  cdp.BackendNodeID
	document string
	frame    cdp.FrameID
}

type documentState struct {
	key        string
	frame      cdp.FrameID
	session    cdp.Session
	context    runtime.ExecutionContextID
	sessionKey string
}

func (p *Page) invalidateRefs() { p.refs = nil }

type resolvedElement struct {
	object   runtime.RemoteObjectID
	document string
	frame    cdp.FrameID
	session  cdp.Session
}

//go:embed scripts/select.js
var selectFunction string

func (p *Page) resolve(ctx context.Context, t *chromedp.Target, locator Locator) (resolvedElement, error) {
	if locator.frameSet && locator.frame == "" {
		return resolvedElement{}, ErrFrameNotFound
	}
	if locator.ref.page != nil && locator.ref.page != p {
		return resolvedElement{}, ErrWrongPage
	}
	frames, err := p.refreshFrames(ctx, t)
	if err != nil {
		return resolvedElement{}, err
	}
	if locator.ref.id != 0 {
		entry, ok := p.refs[locator.ref.id]
		if !ok {
			return resolvedElement{}, ErrStaleRef
		}
		frame := p.frames[entry.frame]
		if frame == nil || frame.doc.key != entry.document || locator.frame != "" && locator.frame != string(entry.frame) {
			return resolvedElement{}, ErrStaleRef
		}
		return p.resolveBackend(ctx, frame.doc, entry.backend)
	}
	if locator.kind == "" {
		return resolvedElement{}, ErrStaleRef
	}
	if locator.frame != "" {
		frame := p.frames[cdp.FrameID(locator.frame)]
		if frame == nil {
			return resolvedElement{}, ErrFrameNotFound
		}
		return p.resolveInFrame(ctx, frame.doc, locator)
	}
	var found *resolvedElement
	for i, frame := range frames {
		node, err := p.resolveInFrame(ctx, frame.doc, locator)
		if errors.Is(err, ErrElementNotFound) {
			continue
		}
		if err != nil {
			if found != nil {
				p.releaseObject(ctx, found.session, found.object)
			}
			return resolvedElement{}, err
		}
		if i == 0 {
			return node, nil
		}
		if found != nil {
			p.releaseObject(ctx, found.session, found.object)
			p.releaseObject(ctx, node.session, node.object)
			return resolvedElement{}, ErrAmbiguous
		}
		found = &node
	}
	if found != nil {
		return *found, nil
	}
	return resolvedElement{}, ErrElementNotFound
}

func (p *Page) resolveBackend(ctx context.Context, doc documentState, backend cdp.BackendNodeID) (resolvedElement, error) {
	result, err := cdp.Call(ctx, doc.session, dom.ResolveNode, dom.ResolveNodeParams{BackendNodeID: backend, ExecutionContextID: doc.context})
	if err != nil {
		if ctx.Err() == nil {
			err = errors.Join(ErrStaleRef, err)
		}
		return resolvedElement{}, err
	}
	if result.Object == nil || result.Object.ObjectID == "" {
		return resolvedElement{}, ErrElementNotFound
	}
	return resolvedElement{object: result.Object.ObjectID, document: doc.key, frame: doc.frame, session: doc.session}, nil
}

func (p *Page) resolveInFrame(ctx context.Context, doc documentState, locator Locator) (resolvedElement, error) {
	if locator.kind == "role" {
		ax, err := cdp.Call(ctx, doc.session, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{FrameID: doc.frame})
		if err != nil {
			return resolvedElement{}, err
		}
		var found *resolvedElement
		for _, candidate := range ax.Nodes {
			if candidate.Ignored || axValue(candidate.Role) != locator.role || axValue(candidate.Name) != locator.text || candidate.BackendDOMNodeID == 0 {
				continue
			}
			node, err := p.resolveBackend(ctx, doc, candidate.BackendDOMNodeID)
			if err != nil {
				if found != nil {
					p.releaseObject(ctx, found.session, found.object)
				}
				return resolvedElement{}, err
			}
			allowed, err := nodeCall[bool](ctx, doc.session, node.object, openNodeFunction)
			if err != nil || !allowed {
				p.releaseObject(ctx, doc.session, node.object)
				if err != nil {
					if found != nil {
						p.releaseObject(ctx, found.session, found.object)
					}
					return resolvedElement{}, err
				}
				continue
			}
			if found != nil {
				p.releaseObject(ctx, doc.session, found.object)
				p.releaseObject(ctx, doc.session, node.object)
				return resolvedElement{}, ErrAmbiguous
			}
			found = &node
		}
		if found == nil {
			return resolvedElement{}, ErrElementNotFound
		}
		return *found, nil
	}
	argument, err := json.Marshal(map[string]string{"kind": locator.kind, "selector": locator.css, "text": strings.Join(strings.Fields(locator.text), " ")})
	if err != nil {
		return resolvedElement{}, err
	}
	result, err := cdp.Call(ctx, doc.session, runtime.Evaluate, runtime.EvaluateParams{ContextID: doc.context, Expression: "(" + selectFunction + ")(" + string(argument) + ")"})
	if err != nil {
		return resolvedElement{}, err
	}
	if result.ExceptionDetails != nil {
		return resolvedElement{}, ErrInvalidLocator
	}
	if result.Result == nil {
		return resolvedElement{}, ErrElementNotFound
	}
	if result.Result.ObjectID != "" {
		return resolvedElement{object: result.Result.ObjectID, document: doc.key, frame: doc.frame, session: doc.session}, nil
	}
	var count int
	if err := json.Unmarshal(result.Result.Value, &count); err != nil {
		return resolvedElement{}, err
	}
	if count > 1 {
		return resolvedElement{}, ErrAmbiguous
	}
	return resolvedElement{}, ErrElementNotFound
}

const openNodeFunction = `function(){if(!this.isConnected||this.ownerDocument!==document||this.nodeType!==1)return false;let n=this;for(;;){const r=n.getRootNode();if(r===document)return true;if(r.mode!=='open')return false;n=r.host;}}`

func (p *Page) releaseObject(ctx context.Context, t cdp.Session, object runtime.RemoteObjectID) {
	if p.deferForDialog(func(cleanup context.Context) {
		_, _ = cdp.Call(cleanup, t, runtime.ReleaseObject, runtime.ReleaseObjectParams{ObjectID: object})
	}) {
		return
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.task.browser.options.CleanupTimeout)
	defer cancel()
	_, _ = cdp.Call(cleanup, t, runtime.ReleaseObject, runtime.ReleaseObjectParams{ObjectID: object})
}

func nodeCall[T any](ctx context.Context, t cdp.Session, object runtime.RemoteObjectID, function string, args ...any) (T, error) {
	var zero T
	params := runtime.CallFunctionOnParams{ObjectID: object, FunctionDeclaration: function, ReturnByValue: ptr(true)}
	for _, arg := range args {
		value, err := json.Marshal(arg)
		if err != nil {
			return zero, err
		}
		params.Arguments = append(params.Arguments, &runtime.CallArgument{Value: value})
	}
	result, err := cdp.Call(ctx, t, runtime.CallFunctionOn, params)
	if err != nil {
		return zero, err
	}
	if result.ExceptionDetails != nil {
		return zero, errors.New("page helper raised a JavaScript exception")
	}
	if result.Result == nil {
		return zero, errors.New("page helper returned no result")
	}
	var value T
	if err := json.Unmarshal(result.Result.Value, &value); err != nil {
		return zero, err
	}
	return value, nil
}

func ptr[T any](v T) *T { return &v }
