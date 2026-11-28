package qdriver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/domsnapshot"
	"github.com/chromedp/chromedp"
)

// SnapshotScope selects the visible viewport, full page, or a ref-rooted subtree.
type SnapshotScope string

const (
	Viewport SnapshotScope = "viewport"
	FullPage SnapshotScope = "full_page"
	Subtree  SnapshotScope = "subtree"
)

// SnapshotOptions limits emitted output, not Chromium's underlying collection.
type SnapshotOptions struct {
	Scope    SnapshotScope // Default: Viewport.
	MaxNodes int           // Default: 1,000.
	MaxBytes int           // Default: 64 KiB of Content, including the root line.
	Root     Ref           // Required only for Subtree; may identify an iframe or open shadow host.
}

// SnapshotNode is a rendered semantic element or non-empty text node.
type SnapshotNode struct {
	Role    string
	Name    string
	Value   string
	States  map[string]string
	Ref     Ref
	Depth   int
	FrameID string
}

// Snapshot contains a bounded, frame-aware observation and its opaque refs.
// Unsupported lists omitted features; Truncated indicates an output budget was
// reached. FrameID and Frames retain the provenance of each delivered node.
type Snapshot struct {
	Content     string
	Nodes       []SnapshotNode
	Scope       SnapshotScope
	DocumentID  string
	Truncated   bool
	Unsupported []string
	Frames      []FrameInfo
}

// Ref looks up an @N label only among refs actually delivered in this snapshot.
func (s Snapshot) Ref(label string) (Ref, bool) {
	for _, node := range s.Nodes {
		if node.Ref.id != 0 && node.Ref.String() == label {
			return node.Ref, true
		}
	}
	return Ref{}, false
}

// Snapshot combines accessibility semantics with DOM/layout information. It
// does not scroll or expose password values. Reachable frames and open shadow
// roots are included. Full-page captures replace the active ref set; partial
// viewport/subtree captures retain omitted refs up to MaxRefs.
func (p *Page) Snapshot(ctx context.Context, options SnapshotOptions) (Snapshot, error) {
	if options.Scope == "" {
		options.Scope = Viewport
	}
	if options.MaxNodes == 0 {
		options.MaxNodes = 1_000
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = 64 << 10
	}
	if (options.Scope != Viewport && options.Scope != FullPage && options.Scope != Subtree) || options.MaxNodes < 1 || options.MaxBytes < len("root\n") || (options.Scope == Subtree) != (options.Root.id != 0) {
		return Snapshot{}, errors.New("qdriver snapshot: invalid scope or output budget")
	}
	return runPage(ctx, p, "snapshot", false, func(ctx context.Context, t *chromedp.Target) (Snapshot, error) {
		for range 3 {
			before, err := p.refreshFrames(ctx, t)
			if err != nil {
				return Snapshot{}, err
			}
			var root refEntry
			if options.Scope == Subtree {
				if options.Root.page != p {
					return Snapshot{}, ErrWrongPage
				}
				var ok bool
				root, ok = p.refs[options.Root.id]
				if !ok {
					return Snapshot{}, ErrStaleRef
				}
			}
			captures := make(map[string]domsnapshot.CaptureSnapshotResult)
			axTrees := make(map[cdp.FrameID][]*accessibility.Node)
			for _, frame := range before {
				if _, ok := captures[frame.doc.sessionKey]; !ok {
					capture, err := cdp.Call(ctx, frame.doc.session, domsnapshot.CaptureSnapshot, domsnapshot.CaptureSnapshotParams{ComputedStyles: []string{"display", "visibility", "opacity"}})
					if err != nil {
						return Snapshot{}, err
					}
					captures[frame.doc.sessionKey] = capture
				}
				ax, err := cdp.Call(ctx, frame.doc.session, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{FrameID: frame.doc.frame})
				if err != nil {
					return Snapshot{}, err
				}
				axTrees[frame.doc.frame] = ax.Nodes
			}
			views, err := p.frameViews(ctx, before)
			if err != nil {
				return Snapshot{}, err
			}
			after, err := p.refreshFrames(ctx, t)
			if err != nil {
				return Snapshot{}, err
			}
			if !sameFrameDocuments(before, after) {
				continue
			}
			builder := newSnapshotBuilder(p, options, before[0].doc.key)
			includedFrames := make(map[cdp.FrameID]bool)
			for _, frame := range before {
				if options.Scope == Subtree && frame.doc.frame != root.frame {
					parent := cdp.FrameID(frame.info.ParentID)
					if (parent != root.frame || frame.owner != root.backend) && !includedFrames[parent] {
						continue
					}
					includedFrames[frame.doc.frame] = true
				}
				view := views[frame.doc.frame]
				if !view.supported {
					if view.closed {
						builder.unsupported("closed shadow DOM")
					} else {
						builder.unsupported("rotated or skewed frames")
					}
					continue
				}
				if !view.visible || options.Scope == Viewport && (view.right <= view.left || view.bottom <= view.top) {
					continue
				}
				builder.result.Frames = append(builder.result.Frames, frame.info)
				capture := captures[frame.doc.sessionKey]
				found := false
				for _, doc := range capture.Documents {
					if stringAt(capture.Strings, doc.FrameID) != frame.info.ID {
						continue
					}
					found = true
					var backend cdp.BackendNodeID
					if options.Scope == Subtree && frame.doc.frame == root.frame {
						backend = root.backend
					}
					depth := 1
					if frame.info.ParentID != "" && backend == 0 {
						name := frame.info.Name
						if name == "" {
							name = frame.info.ID
						}
						builder.emit(SnapshotNode{Role: "frame", Name: name, FrameID: frame.info.ID, Depth: 1}, 0, frame.doc)
						depth = 2
					}
					builder.addDocument(frame.doc, doc, capture.Strings, axTrees[frame.doc.frame], view, backend, depth)
					break
				}
				if !found {
					builder.unsupported("frame layout unavailable")
				}
			}
			if options.Scope == Subtree && !builder.rootFound {
				return Snapshot{}, ErrStaleRef
			}
			return builder.finish(), nil
		}
		return Snapshot{}, ErrDocumentChanged
	})
}

func stringAt[T ~int64](values []string, index T) string {
	if int(index) < 0 || int(index) >= len(values) {
		return ""
	}
	return values[index]
}

func axValue(value *accessibility.Value) string {
	if value == nil {
		return ""
	}
	var v any
	if json.Unmarshal(value.Value, &v) != nil || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

type snapshotBuilder struct {
	page       *Page
	options    SnapshotOptions
	result     Snapshot
	refs       map[uint64]refEntry
	delivered  map[uint64]bool
	byIdentity map[string]uint64
	rootFound  bool
}

func refIdentity(document string, backend cdp.BackendNodeID) string {
	return fmt.Sprintf("%s/%d", document, backend)
}

func newSnapshotBuilder(p *Page, options SnapshotOptions, document string) *snapshotBuilder {
	b := &snapshotBuilder{page: p, options: options, result: Snapshot{Content: "root\n", Scope: options.Scope, DocumentID: document}, refs: make(map[uint64]refEntry), delivered: make(map[uint64]bool), byIdentity: make(map[string]uint64)}
	for id, entry := range p.refs {
		b.byIdentity[refIdentity(entry.document, entry.backend)] = id
		if options.Scope != FullPage {
			b.refs[id] = entry
		}
	}
	return b
}

func (b *snapshotBuilder) unsupported(feature string) {
	if !slices.Contains(b.result.Unsupported, feature) {
		b.result.Unsupported = append(b.result.Unsupported, feature)
	}
}

func (b *snapshotBuilder) addDocument(docState documentState, doc *domsnapshot.DocumentSnapshot, stringsTable []string, axNodes []*accessibility.Node, view frameView, root cdp.BackendNodeID, baseDepth int) {
	if doc.Nodes == nil || doc.Layout == nil {
		b.unsupported("missing DOM layout")
		return
	}
	ax := make(map[cdp.BackendNodeID]*accessibility.Node, len(axNodes))
	for _, node := range axNodes {
		ax[node.BackendDOMNodeID] = node
	}
	nodes := doc.Nodes
	layout := make(map[int]int, len(doc.Layout.NodeIndex))
	for i, node := range doc.Layout.NodeIndex {
		layout[int(node)] = i
	}
	blocked := make([]bool, len(nodes.BackendNodeID))
	depths := make([]int, len(blocked))
	included := make([]bool, len(blocked))
	hosts := make(map[int]bool)
	if nodes.ShadowRootType != nil {
		for j, i := range nodes.ShadowRootType.Index {
			typeName := stringAt(stringsTable, nodes.ShadowRootType.Value[j])
			if typeName == "open" {
				if parent := int(nodes.ParentIndex[i]); parent >= 0 {
					hosts[parent] = true
				}
			} else {
				blocked[i] = true
				if typeName == "closed" {
					b.unsupported("closed shadow DOM")
				}
			}
		}
	}
	if nodes.PseudoType != nil {
		for _, i := range nodes.PseudoType.Index {
			blocked[i] = true
		}
		if len(nodes.PseudoType.Index) > 0 {
			b.unsupported("pseudo elements")
		}
	}
	clickable := make(map[int]bool)
	if nodes.IsClickable != nil {
		for _, i := range nodes.IsClickable.Index {
			clickable[int(i)] = true
		}
	}
	for i, backend := range nodes.BackendNodeID {
		depth := baseDepth
		if parent := int(nodes.ParentIndex[i]); parent >= 0 {
			blocked[i] = blocked[i] || blocked[parent]
			depth = depths[parent]
			included[i] = included[parent]
		}
		if root == 0 || backend == root {
			included[i] = true
		}
		if root != 0 && backend == root {
			b.rootFound = true
		}
		depths[i] = depth
		if blocked[i] {
			continue
		}
		name := strings.ToLower(stringAt(stringsTable, nodes.NodeName[i]))
		if name == "canvas" {
			b.unsupported("canvas content")
		}
		li, hasLayout := layout[i]
		if !hasLayout {
			continue
		}
		styles := doc.Layout.Styles[li]
		if len(styles) >= 3 && (stringAt(stringsTable, styles[0]) == "none" || stringAt(stringsTable, styles[1]) == "hidden" || stringAt(stringsTable, styles[1]) == "collapse" || stringAt(stringsTable, styles[2]) == "0") {
			blocked[i] = true
			continue
		}
		if !included[i] {
			continue
		}
		bounds := doc.Layout.Bounds[li]
		if len(bounds) < 4 || bounds[2] <= 0 || bounds[3] <= 0 {
			continue
		}
		if b.options.Scope == Viewport {
			x, y := view.x+(bounds[0]-doc.ScrollOffsetX)*view.sx, view.y+(bounds[1]-doc.ScrollOffsetY)*view.sy
			if x+bounds[2]*view.sx <= view.left || y+bounds[3]*view.sy <= view.top || x >= view.right || y >= view.bottom {
				continue
			}
		}
		node := SnapshotNode{Depth: min(depth, 20), FrameID: string(docState.frame)}
		attributes := make(map[string]string)
		if i < len(nodes.Attributes) {
			for j := 0; j+1 < len(nodes.Attributes[i]); j += 2 {
				attributes[stringAt(stringsTable, nodes.Attributes[i][j])] = stringAt(stringsTable, nodes.Attributes[i][j+1])
			}
		}
		if nodes.NodeType[i] == 3 {
			node.Role, node.Name = "text", strings.Join(strings.Fields(stringAt(stringsTable, nodes.NodeValue[i])), " ")
			if node.Name == "" {
				continue
			}
		} else {
			if a := ax[backend]; a != nil && !a.Ignored {
				node.Role, node.Name, node.Value = axValue(a.Role), axValue(a.Name), axValue(a.Value)
				for _, property := range a.Properties {
					if slices.Contains([]string{"disabled", "readonly", "checked", "expanded", "selected", "required"}, string(property.Name)) {
						if node.States == nil {
							node.States = make(map[string]string)
						}
						node.States[string(property.Name)] = axValue(property.Value)
					}
				}
			}
			if strings.EqualFold(attributes["type"], "password") {
				node.Value = ""
			}
			if node.Role == "generic" || node.Role == "none" || node.Role == "RootWebArea" {
				node.Role = ""
			}
			if node.Role == "" && slices.Contains([]string{"iframe", "canvas", "img", "svg", "table", "tr", "td", "th"}, name) {
				node.Role = name
			}
			if node.Role == "" && hosts[i] {
				node.Role = "shadow_host"
			}
			interactive := hosts[i] || name == "iframe" || clickable[i] || slices.Contains([]string{"input", "textarea", "button", "select"}, name) || (name == "a" && attributes["href"] != "") || attributes["tabindex"] != "" || attributes["contenteditable"] == "true"
			if interactive && node.Role == "" {
				node.Role = "element"
			}
			if node.Role == "" {
				continue
			}
			if interactive {
				id := b.byIdentity[refIdentity(docState.key, backend)]
				if id == 0 {
					id = b.page.nextRef + 1
				}
				node.Ref = Ref{page: b.page, id: id}
			}
		}
		if !b.emit(node, backend, docState) {
			return
		}
		depths[i] = depth + 1
	}
}

func (b *snapshotBuilder) emit(node SnapshotNode, backend cdp.BackendNodeID, doc documentState) bool {
	line := snapshotLine(node)
	if b.result.Truncated || len(b.result.Nodes) >= b.options.MaxNodes || len(b.result.Content)+len(line) > b.options.MaxBytes || node.Ref.id != 0 && len(b.delivered) >= b.page.task.browser.options.MaxRefs {
		b.result.Truncated = true
		return false
	}
	if node.Ref.id != 0 {
		b.page.nextRef = max(b.page.nextRef, node.Ref.id)
		b.refs[node.Ref.id] = refEntry{backend: backend, document: doc.key, frame: doc.frame}
		b.byIdentity[refIdentity(doc.key, backend)] = node.Ref.id
		b.delivered[node.Ref.id] = true
	}
	b.result.Nodes = append(b.result.Nodes, node)
	b.result.Content += line
	return true
}

func (b *snapshotBuilder) finish() Snapshot {
	// Partial observations retain omitted refs, with oldest-first eviction.
	if len(b.refs) > b.page.task.browser.options.MaxRefs {
		ids := make([]uint64, 0, len(b.refs))
		for id := range b.refs {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			if len(b.refs) <= b.page.task.browser.options.MaxRefs {
				break
			}
			if !b.delivered[id] {
				delete(b.refs, id)
			}
		}
	}
	b.page.refs = b.refs
	return b.result
}

func snapshotLine(node SnapshotNode) string {
	line := strings.Repeat("  ", node.Depth) + node.Role
	if node.Name != "" {
		line += " " + strconv.Quote(node.Name)
	}
	if node.Value != "" {
		line += " value=" + strconv.Quote(node.Value)
	}
	keys := make([]string, 0, len(node.States))
	for key := range node.States {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		line += " " + key + "=" + strconv.Quote(node.States[key])
	}
	if node.Ref.id != 0 {
		line += " [ref=" + node.Ref.String() + "]"
	}
	return line + "\n"
}
