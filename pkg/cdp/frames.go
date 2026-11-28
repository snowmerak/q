package qdriver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// FrameInfo describes a currently reachable frame. IDs are browser runtime
// identities, not durable handles; a frame can navigate without changing its ID.
type FrameInfo struct {
	ID, ParentID, URL, Name string
	OutOfProcess            bool
}

type frameState struct {
	info          FrameInfo
	doc           documentState
	owner         cdp.BackendNodeID // In the parent's renderer, never the child's.
	width, height float64
}

type frameConnection struct {
	ctx                context.Context
	cancel, waitCancel context.CancelFunc
	target             *chromedp.Target
	detached           <-chan struct{}
	once               sync.Once
	done               chan struct{}
}

func (c *frameConnection) alive() bool {
	select {
	case <-c.ctx.Done():
		return false
	case <-c.detached:
		return false
	default:
		return true
	}
}

func (c *frameConnection) stop(ctx context.Context) error {
	c.once.Do(func() { c.cancel(); go func() { c.waitCancel(); close(c.done) }() })
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Page) iframeSession(ctx context.Context, id cdp.FrameID) (*chromedp.Target, error) {
	if p.frameSessions == nil {
		p.frameSessions = make(map[cdp.FrameID]*frameConnection)
	}
	if c := p.frameSessions[id]; c != nil {
		if c.alive() {
			return c.target, nil
		}
		_ = c.stop(ctx)
		if c.target != nil {
			p.events.sessionRemoved(string(c.target.SessionID))
		}
		delete(p.frameSessions, id)
	}
	life, cancel := context.WithCancel(p.ctx)
	var child context.Context
	child, wait := chromedp.NewContext(life, chromedp.WithTargetID(target.ID(id)), chromedp.WithDetachOnCancel(),
		chromedp.WithListener(func(event any) { p.onTargetEvent(chromedp.FromContext(child).Target, event) }))
	c := &frameConnection{ctx: child, cancel: cancel, waitCancel: wait, done: make(chan struct{})}
	p.frameSessions[id] = c // Retain even a failed attachment for bounded cleanup.
	stop := context.AfterFunc(ctx, cancel)
	err := chromedp.Do(child) // The reader must live as long as the frame session.
	if err == nil {
		_, err = cdp.Call(ctx, chromedp.FromContext(child).Target, runtime.Disable, cdp.Empty{})
	}
	stop()
	if err = errors.Join(err, ctx.Err()); err != nil {
		cancel()
		return nil, err
	}
	c.target = chromedp.FromContext(child).Target
	p.events.sessionAdded(string(c.target.SessionID))
	if p.interceptFiles {
		if _, err := cdp.Call(ctx, c.target, cdppage.SetInterceptFileChooserDialog, cdppage.SetInterceptFileChooserDialogParams{Enabled: true}); err != nil {
			return nil, err
		}
	}
	c.detached, err = chromedp.Detached(child)
	return c.target, err
}

// Frames discovers this page's frame tree and attaches only to reachable OOPIF
// targets. It does not claim other pages or pause workers with auto-attach.
func (p *Page) Frames(ctx context.Context) ([]FrameInfo, error) {
	return runPage(ctx, p, "frames", false, func(ctx context.Context, root *chromedp.Target) ([]FrameInfo, error) {
		frames, err := p.refreshFrames(ctx, root)
		if err != nil {
			return nil, err
		}
		result := make([]FrameInfo, 0, len(frames))
		for _, frame := range frames {
			result = append(result, frame.info)
		}
		return result, nil
	})
}

func (p *Page) refreshFrames(ctx context.Context, root *chromedp.Target) ([]*frameState, error) {
	var last error
	for range 3 {
		frames, err := p.refreshFramesOnce(ctx, root)
		if err == nil {
			return frames, nil
		}
		if ctx.Err() != nil || !frameLifecycleError(err) {
			return nil, err
		}
		last = err
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, errors.Join(ErrDocumentChanged, last)
}

func frameLifecycleError(err error) bool {
	if errors.Is(err, chromedp.ErrTargetDetached) || errors.Is(err, ErrDocumentChanged) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, part := range []string{"no target with given id", "session with given id not found", "cannot find context with specified id", "cannot find default execution context", "frame with the given", "no frame with given id", "no frame for given id", "inspected target navigated"} {
		if strings.Contains(message, part) {
			return true
		}
	}
	return false
}

func (p *Page) refreshFramesOnce(ctx context.Context, root *chromedp.Target) ([]*frameState, error) {
	targets, err := cdp.Call(ctx, p.task.browser.session(), target.GetTargets, target.GetTargetsParams{})
	if err != nil {
		return nil, err
	}
	remote := make(map[cdp.FrameID]bool)
	remoteChildren := make(map[cdp.FrameID][]cdp.FrameID)
	for _, info := range targets.TargetInfos {
		if info.Type == "iframe" {
			remote[cdp.FrameID(info.TargetID)] = true
			if info.ParentFrameID != "" {
				remoteChildren[info.ParentFrameID] = append(remoteChildren[info.ParentFrameID], cdp.FrameID(info.TargetID))
			}
		}
	}
	for _, children := range remoteChildren {
		slices.Sort(children)
	}
	tree, err := cdp.Call(ctx, root, cdppage.GetFrameTree, cdp.Empty{})
	if err != nil {
		return nil, err
	}
	if tree.FrameTree == nil {
		return nil, ErrDocumentChanged
	}
	states := make(map[cdp.FrameID]*frameState)
	var ordered []*frameState
	var visit func(*cdppage.FrameTree, cdp.FrameID, *chromedp.Target) error
	visit = func(tree *cdppage.FrameTree, parent cdp.FrameID, session *chromedp.Target) error {
		id := tree.Frame.ID
		if states[id] != nil {
			return nil
		}
		if len(states) >= p.task.browser.options.MaxFrames {
			return ErrFrameLimit
		}
		isRemote := parent != "" && remote[id]
		if isRemote {
			var err error
			session, err = p.iframeSession(ctx, id)
			if err != nil {
				return err
			}
			own, err := cdp.Call(ctx, session, cdppage.GetFrameTree, cdp.Empty{})
			if err != nil {
				return err
			}
			if own.FrameTree == nil || own.FrameTree.Frame.ID != id {
				return ErrDocumentChanged
			}
			tree = own.FrameTree
		}
		world, err := cdp.Call(ctx, session, cdppage.CreateIsolatedWorld, cdppage.CreateIsolatedWorldParams{FrameID: id, WorldName: "qdriver"})
		if err != nil {
			return err
		}
		object, err := cdp.Call(ctx, session, runtime.Evaluate, runtime.EvaluateParams{Expression: "document", ContextID: world.ExecutionContextID})
		if err != nil {
			return err
		}
		if object.ExceptionDetails != nil || object.Result == nil || object.Result.ObjectID == "" {
			return ErrDocumentChanged
		}
		description, describeErr := cdp.Call(ctx, session, dom.DescribeNode, dom.DescribeNodeParams{ObjectID: object.Result.ObjectID, Depth: ptr(int64(0))})
		p.releaseObject(ctx, session, object.Result.ObjectID)
		if describeErr != nil {
			return describeErr
		}
		if description.Node == nil {
			return ErrDocumentChanged
		}
		doc := documentState{frame: id, session: session, context: world.ExecutionContextID, sessionKey: string(session.SessionID)}
		doc.key = fmt.Sprintf("%s/%s/%s/%d/%d", session.SessionID, id, tree.Frame.LoaderID, world.ExecutionContextID, description.Node.BackendNodeID)
		state := &frameState{info: FrameInfo{ID: string(id), ParentID: string(parent), URL: tree.Frame.URL, Name: tree.Frame.Name, OutOfProcess: isRemote}, doc: doc}
		states[id] = state
		ordered = append(ordered, state)
		if parent != "" {
			owner, err := cdp.Call(ctx, states[parent].doc.session, dom.GetFrameOwner, dom.GetFrameOwnerParams{FrameID: id})
			if err != nil {
				return err
			}
			state.owner = owner.BackendNodeID
		}
		viewport, err := evaluateInFrame[struct{ Width, Height float64 }](ctx, doc, "({Width:innerWidth,Height:innerHeight})")
		if err != nil {
			return err
		}
		state.width, state.height = viewport.Width, viewport.Height
		for _, child := range tree.ChildFrames {
			if err := visit(child, id, session); err != nil {
				return err
			}
		}
		// A renderer's frame tree may omit remote children entirely. Follow only
		// browser-reported parent-frame edges already rooted in this page.
		for _, childID := range remoteChildren[id] {
			if err := visit(&cdppage.FrameTree{Frame: &cdp.Frame{ID: childID}}, id, session); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(tree.FrameTree, "", root); err != nil {
		return nil, err
	}
	if p.document != ordered[0].doc.key {
		p.invalidateRefs()
		p.document = ordered[0].doc.key
	}
	for id, entry := range p.refs {
		frame := states[entry.frame]
		if frame == nil || frame.doc.key != entry.document {
			delete(p.refs, id)
		}
	}
	for id, connection := range p.frameSessions {
		if states[id] == nil || !remote[id] {
			_ = connection.stop(ctx)
			if connection.target != nil {
				p.events.sessionRemoved(string(connection.target.SessionID))
			}
			delete(p.frameSessions, id)
		}
	}
	p.frames = states
	p.updateEventFrames(states)
	return ordered, nil
}

func evaluateInFrame[T any](ctx context.Context, doc documentState, expression string) (T, error) {
	var zero T
	result, err := cdp.Call(ctx, doc.session, runtime.Evaluate, runtime.EvaluateParams{Expression: expression, ContextID: doc.context, ReturnByValue: ptr(true)})
	if err != nil {
		return zero, err
	}
	if result.ExceptionDetails != nil || result.Result == nil {
		return zero, errors.New("frame evaluation failed")
	}
	var value T
	if err := json.Unmarshal(result.Result.Value, &value); err != nil {
		return zero, err
	}
	return value, nil
}

func sameFrameDocuments(before, after []*frameState) bool {
	if len(before) != len(after) {
		return false
	}
	a, b := make([]string, len(before)), make([]string, len(after))
	for i := range before {
		a[i], b[i] = before[i].doc.key, after[i].doc.key
	}
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
