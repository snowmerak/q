package qdriver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

func filePaths(paths []string) ([]string, error) {
	result := make([]string, len(paths))
	for i, path := range paths {
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("file path must be absolute: %q", path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("file is not regular: %q", path)
		}
		result[i] = filepath.Clean(path)
	}
	return result, nil
}

// SetInputFiles sets a file input in any supported frame/open shadow root.
// Hidden inputs are allowed. Paths must be absolute existing regular files
// accessible at the same paths to Chrome; this API does not transfer bytes to
// a remote browser. An empty list clears the input. It never retries a set.
func (p *Page) SetInputFiles(ctx context.Context, locator Locator, paths ...string) (ActionReceipt, error) {
	files, err := filePaths(paths)
	if err != nil {
		return ActionReceipt{}, err
	}
	return runPageOperation(ctx, p, "set input files", func(ctx context.Context, t *chromedp.Target, mark func()) (ActionReceipt, error) {
		node, err := p.resolve(ctx, t, locator)
		if err != nil {
			return ActionReceipt{}, err
		}
		defer p.releaseObject(ctx, node.session, node.object)
		return p.setFiles(ctx, t, node, files, mark)
	})
}

func (p *Page) setFiles(ctx context.Context, t *chromedp.Target, node resolvedElement, files []string, mark func()) (ActionReceipt, error) {
	var receipt ActionReceipt
	state, err := nodeCall[struct{ File, Multiple bool }](ctx, node.session, node.object, `function(){return {File:this instanceof HTMLInputElement && this.type==='file',Multiple:!!this.multiple}}`)
	if err != nil {
		return receipt, err
	}
	if !state.File {
		return receipt, ErrNotEditable
	}
	if len(files) > 1 && !state.Multiple {
		return receipt, errors.New("file input does not allow multiple files")
	}
	if err := p.sameDocument(ctx, t, node); err != nil {
		return receipt, err
	}
	mark()
	receipt.InputStarted = true
	if len(files) == 0 {
		// Chromium can acknowledge an empty DOM.setFileInputFiles without
		// clearing an existing selection. The standard value setter clears it;
		// dispatch input/change once to match a changed file selection.
		_, err = nodeCall[bool](ctx, node.session, node.object, `function(){const changed=this.files.length>0;this.value='';if(changed){this.dispatchEvent(new Event('input',{bubbles:true,composed:true}));this.dispatchEvent(new Event('change',{bubbles:true}));}return this.files.length===0}`)
	} else {
		_, err = cdp.Call(ctx, node.session, dom.SetFileInputFiles, dom.SetFileInputFilesParams{Files: files, ObjectID: node.object})
	}
	receipt.InputCompleted = err == nil
	return receipt, err
}

// SetFileChooserInterception explicitly controls native chooser interception
// for this page and its attached OOPIF sessions. Prepare Subscribe with
// FileChooserOpened before clicking. New OOPIFs inherit the setting when
// discovered. Release/Finish disable it on borrowed sessions before detach.
func (p *Page) SetFileChooserInterception(ctx context.Context, enabled bool) error {
	_, err := runPage(ctx, p, "set file chooser interception", true, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		if _, err := p.refreshFrames(ctx, t); err != nil {
			return err
		}
		return p.setFileInterceptionLocked(ctx, enabled)
	}))
	return err
}
func (p *Page) setFileInterceptionLocked(ctx context.Context, enabled bool) error {
	// Retain true on a partial enable so cleanup also resets sessions whose
	// commands may have taken effect before the error.
	if enabled {
		p.interceptFiles = true
	}
	var result error
	if p.initialized {
		_, err := cdp.Call(ctx, chromedp.FromContext(p.ctx).Target, cdppage.SetInterceptFileChooserDialog, cdppage.SetInterceptFileChooserDialogParams{Enabled: enabled})
		result = errors.Join(result, err)
	}
	for _, c := range p.frameSessions {
		if c.target == nil || !c.alive() {
			continue
		}
		_, err := cdp.Call(ctx, c.target, cdppage.SetInterceptFileChooserDialog, cdppage.SetInterceptFileChooserDialogParams{Enabled: enabled})
		result = errors.Join(result, err)
	}
	if !enabled && result == nil {
		p.interceptFiles = false
	}
	return result
}

// FileChooser identifies the input that opened one intercepted chooser.
// SetFiles may be used once; Cancel supplies no files. Navigation invalidates
// old chooser handles, even if a replacement input has the same selector.
type FileChooser struct {
	FrameID  string
	Multiple bool
	page     *Page
	session  cdp.Session
	backend  cdp.BackendNodeID
	frame    cdp.FrameID
	document string
	epoch    uint64
	used     bool // Protected by the browser operation gate.
}

func (f *FileChooser) SetFiles(ctx context.Context, paths ...string) (ActionReceipt, error) {
	files, err := filePaths(paths)
	if err != nil {
		return ActionReceipt{}, err
	}
	p := f.page
	return runPageOperation(ctx, p, "set chooser files", func(ctx context.Context, t *chromedp.Target, mark func()) (ActionReceipt, error) {
		if f.used {
			return ActionReceipt{}, ErrStaleRef
		}
		p.events.mu.Lock()
		stale := f.epoch != p.events.documentEpoch
		p.events.mu.Unlock()
		if stale || f.backend == 0 || !p.interceptFiles {
			return ActionReceipt{}, ErrStaleRef
		}
		if _, err := p.refreshFrames(ctx, t); err != nil {
			return ActionReceipt{}, err
		}
		frame := p.frames[f.frame]
		if frame == nil || frame.doc.session != f.session || frame.doc.key != f.document {
			return ActionReceipt{}, ErrStaleRef
		}
		node, err := p.resolveBackend(ctx, frame.doc, f.backend)
		if err != nil {
			return ActionReceipt{}, err
		}
		defer p.releaseObject(ctx, node.session, node.object)
		return p.setFiles(ctx, t, node, files, func() { f.used = true; mark() })
	})
}
func (f *FileChooser) Cancel(ctx context.Context) error { _, err := f.SetFiles(ctx); return err }
