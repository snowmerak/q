package qdriver

import (
	"context"
	_ "embed"
	"math"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
)

//go:embed scripts/frame.js
var frameFunction string

type ownerGeometry struct {
	Closed                                                bool
	OriginX, OriginY, ScaleX, ScaleY, Width, Height, X, Y float64
	Visible, Supported, Hit                               bool
}

func (p *Page) ownerGeometry(ctx context.Context, child *frameState, action string, x, y float64) (ownerGeometry, error) {
	parent := p.frames[cdp.FrameID(child.info.ParentID)]
	if parent == nil {
		return ownerGeometry{}, ErrFrameNotFound
	}
	owner, err := cdp.Call(ctx, parent.doc.session, dom.ResolveNode, dom.ResolveNodeParams{BackendNodeID: child.owner, ExecutionContextID: parent.doc.context})
	if err != nil {
		return ownerGeometry{}, err
	}
	if owner.Object == nil || owner.Object.ObjectID == "" {
		return ownerGeometry{}, ErrFrameNotFound
	}
	defer p.releaseObject(ctx, parent.doc.session, owner.Object.ObjectID)
	return nodeCall[ownerGeometry](ctx, parent.doc.session, owner.Object.ObjectID, frameFunction, action, x, y)
}

// Input uses the renderer session's root coordinates, but interception checks
// traverse every ancestor up to the page root using the same geometry reads.
type inputGeometry struct {
	X, Y, ScaleX, ScaleY float64
	Visible, Hit         bool
}

func (p *Page) inputPoint(ctx context.Context, id cdp.FrameID, x, y float64) (inputGeometry, error) {
	initial := p.frames[id]
	if initial == nil {
		return inputGeometry{}, ErrFrameNotFound
	}
	result := inputGeometry{X: x, Y: y, ScaleX: 1, ScaleY: 1, Visible: true, Hit: true}
	withinSession := true
	for {
		frame := p.frames[id]
		if frame == nil {
			return inputGeometry{}, ErrFrameNotFound
		}
		if frame.info.ParentID == "" {
			return result, nil
		}
		geometry, err := p.ownerGeometry(ctx, frame, "inspect", x, y)
		if err != nil {
			return inputGeometry{}, err
		}
		if !geometry.Supported {
			return inputGeometry{}, ErrUnsupported
		}
		result.Visible, result.Hit = result.Visible && geometry.Visible, result.Hit && geometry.Hit
		x, y, id = geometry.X, geometry.Y, cdp.FrameID(frame.info.ParentID)
		parent := p.frames[id]
		if parent == nil {
			return inputGeometry{}, ErrFrameNotFound
		}
		withinSession = withinSession && parent.doc.sessionKey == initial.doc.sessionKey
		if withinSession {
			result.X, result.Y = x, y
			result.ScaleX *= geometry.ScaleX
			result.ScaleY *= geometry.ScaleY
		}
	}
}

func (p *Page) scrollFrameOwners(ctx context.Context, id cdp.FrameID) error {
	var chain []*frameState
	for {
		frame := p.frames[id]
		if frame == nil {
			return ErrFrameNotFound
		}
		if frame.info.ParentID == "" {
			break
		}
		chain = append(chain, frame)
		id = cdp.FrameID(frame.info.ParentID)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		geometry, err := p.ownerGeometry(ctx, chain[i], "scroll", 0, 0)
		if err != nil {
			return err
		}
		if !geometry.Supported {
			return ErrUnsupported
		}
		if !geometry.Visible {
			return ErrNotActionable
		}
	}
	return nil
}

type frameView struct {
	closed                                 bool
	x, y, sx, sy, left, top, right, bottom float64
	visible, supported                     bool
}

func (p *Page) frameViews(ctx context.Context, frames []*frameState) (map[cdp.FrameID]frameView, error) {
	views := make(map[cdp.FrameID]frameView, len(frames))
	for _, frame := range frames {
		if frame.info.ParentID == "" {
			views[frame.doc.frame] = frameView{sx: 1, sy: 1, right: frame.width, bottom: frame.height, visible: true, supported: true}
			continue
		}
		parent := views[cdp.FrameID(frame.info.ParentID)]
		owner, err := p.ownerGeometry(ctx, frame, "inspect", 0, 0)
		if err != nil {
			return nil, err
		}
		view := frameView{x: parent.x + owner.OriginX*parent.sx, y: parent.y + owner.OriginY*parent.sy, sx: parent.sx * owner.ScaleX, sy: parent.sy * owner.ScaleY, visible: parent.visible && owner.Visible, supported: parent.supported && owner.Supported}
		view.closed = parent.closed || owner.Closed
		view.left, view.top = math.Max(parent.left, view.x), math.Max(parent.top, view.y)
		view.right, view.bottom = math.Min(parent.right, view.x+owner.Width*parent.sx), math.Min(parent.bottom, view.y+owner.Height*parent.sy)
		views[frame.doc.frame] = view
	}
	return views, nil
}
