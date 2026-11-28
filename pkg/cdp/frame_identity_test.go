package qdriver

import (
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/domsnapshot"
)

func TestRendererLocalBackendIDCollisions(t *testing.T) {
	p := &Page{task: &Task{browser: &Browser{options: Options{MaxRefs: 10}}}}
	dom := &domsnapshot.DocumentSnapshot{
		Nodes:  &domsnapshot.NodeTreeSnapshot{ParentIndex: []int64{-1}, NodeType: []int64{1}, NodeName: []domsnapshot.StringIndex{0}, BackendNodeID: []cdp.BackendNodeID{7}},
		Layout: &domsnapshot.LayoutTreeSnapshot{NodeIndex: []int64{0}, Styles: []domsnapshot.ArrayOfStrings{{}}, Bounds: []domsnapshot.Rectangle{{0, 0, 80, 20}}},
	}
	ax := []*accessibility.Node{{BackendDOMNodeID: 7, Role: &accessibility.Value{Value: []byte(`"button"`)}, Name: &accessibility.Value{Value: []byte(`"Repeated name"`)}}}
	view := frameView{sx: 1, sy: 1, right: 800, bottom: 600, visible: true, supported: true}
	a := documentState{key: "renderer-a/frame-a/document-1", frame: "frame-a"}
	b := documentState{key: "renderer-b/frame-b/document-1", frame: "frame-b"}
	options := SnapshotOptions{Scope: FullPage, MaxNodes: 10, MaxBytes: 1024}
	builder := newSnapshotBuilder(p, options, a.key)
	builder.addDocument(a, dom, []string{"BUTTON"}, ax, view, 0, 1)
	builder.addDocument(b, dom, []string{"BUTTON"}, ax, view, 0, 1)
	s := builder.finish()
	if len(s.Nodes) != 2 || s.Nodes[0].Ref.String() == s.Nodes[1].Ref.String() {
		t.Fatalf("renderer-local backend IDs aliased: %+v", s.Nodes)
	}
	oldA, oldB := s.Nodes[0].Ref, s.Nodes[1].Ref
	if p.refs[oldA.id].frame != a.frame || p.refs[oldB.id].frame != b.frame {
		t.Fatal("ref lost frame provenance")
	}
	a.key = "renderer-c/frame-a/document-2" // Same frame/backend number, replaced renderer/document.
	builder = newSnapshotBuilder(p, options, a.key)
	builder.addDocument(a, dom, []string{"BUTTON"}, ax, view, 0, 1)
	builder.addDocument(b, dom, []string{"BUTTON"}, ax, view, 0, 1)
	s = builder.finish()
	if s.Nodes[0].Ref.String() == oldA.String() || s.Nodes[1].Ref.String() != oldB.String() {
		t.Fatalf("document replacement affected unrelated frame identity: %+v", s.Nodes)
	}
	if _, ok := p.refs[oldA.id]; ok {
		t.Fatal("full capture retained the old renderer's ref")
	}
}
