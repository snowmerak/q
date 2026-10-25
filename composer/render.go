package composer

import (
	"context"
	"fmt"
	"strings"
	"testing/fstest"

	"github.com/d2lang/d2/d2graph"
	"github.com/d2lang/d2/d2layouts/d2dagrelayout"
	"github.com/d2lang/d2/d2lib"
	"github.com/d2lang/d2/d2renderers/d2svg"
	d2log "github.com/d2lang/d2/lib/log"
	"github.com/d2lang/d2/lib/textmeasure"
	"github.com/snowmerak/q/diagnostics"
)

// RenderD2 supplies only managed diagram sources as a virtual filesystem.
// D2 imports cannot read the host filesystem or another Composer's files.
func (a *Artifacts) RenderD2(ctx context.Context, value Artifact) ([]byte, error) {
	ctx = d2log.With(ctx, diagnostics.Logger(ctx))
	files, err := a.List()
	if err != nil {
		return nil, err
	}
	sources := fstest.MapFS{}
	for _, file := range files {
		if file.Kind != "d2" {
			continue
		}
		source, err := a.Read(file.Path)
		if err != nil {
			return nil, err
		}
		sources[strings.TrimPrefix(file.Path, "docs/diagram/")] = &fstest.MapFile{Data: []byte(source.Content)}
	}
	ruler, err := textmeasure.NewRuler()
	if err != nil {
		return nil, err
	}
	options := &d2lib.CompileOptions{FS: sources, InputPath: strings.TrimPrefix(value.Path, "docs/diagram/"), Ruler: ruler,
		LayoutResolver: func(engine string) (d2graph.LayoutGraph, error) {
			if engine != "dagre" {
				return nil, fmt.Errorf("composer supports the dagre layout, got %q", engine)
			}
			return d2dagrelayout.DefaultLayout, nil
		},
	}
	render := &d2svg.RenderOpts{}
	diagram, _, err := d2lib.Compile(ctx, value.Content, options, render)
	if err != nil {
		return nil, err
	}
	return d2svg.Render(diagram, render)
}
