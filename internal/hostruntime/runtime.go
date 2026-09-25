// Package hostruntime owns the process-wide services shared by q's interactive
// and headless application hosts.
package hostruntime

import (
	"context"
	"errors"
	"io"
	"sync"

	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/usagelog"
	"github.com/snowmerak/q/workspacememory"
)

// Options configures one application host runtime.
type Options struct {
	Directory     string
	ServiceOutput io.Writer
}

type dependencies struct {
	runLibrary  func(context.Context, string, io.Writer) error
	runMemory   func(context.Context, string, io.Writer) error
	newManager  func(context.Context, providerhost.Store) (*providerhost.Manager, error)
	newRecorder func(string) *usagelog.Recorder
}

var defaultDependencies = dependencies{
	runLibrary:  qlibrary.Run,
	runMemory:   workspacememory.Run,
	newManager:  providerhost.NewManager,
	newRecorder: usagelog.New,
}

// Runtime owns Library, Workspace Memory, the managed provider process, and
// the usage recorder. Workspace-bound clients and tools remain caller-owned.
type Runtime struct {
	ctx            context.Context
	memoryCtx      context.Context
	cancelRuntime  context.CancelFunc
	cancelProvider context.CancelFunc
	cancelMemory   context.CancelFunc
	manager        *providerhost.Manager
	recorder       *usagelog.Recorder
	memoryDone     <-chan error
	libraryDone    <-chan error
	closeOnce      sync.Once
	closeErr       error
}

// Open starts the shared services. Close releases every resource created by a
// successful call and is safe to call more than once.
func Open(parent context.Context, options Options) (*Runtime, error) {
	return open(parent, options, defaultDependencies)
}

func open(parent context.Context, options Options, dependencies dependencies) (*Runtime, error) {
	if parent == nil {
		parent = context.Background()
	}
	output := options.ServiceOutput
	if output == nil {
		output = io.Discard
	}
	runtimeContext, cancelRuntime := context.WithCancel(parent)
	providerContext, cancelProvider := context.WithCancel(context.WithoutCancel(parent))
	memoryContext, cancelMemory := context.WithCancel(context.WithoutCancel(parent))
	runtime := &Runtime{
		ctx: runtimeContext, memoryCtx: memoryContext,
		cancelRuntime: cancelRuntime, cancelProvider: cancelProvider, cancelMemory: cancelMemory,
	}
	memoryDone := make(chan error, 1)
	runtime.memoryDone = memoryDone
	go func() { memoryDone <- dependencies.runMemory(memoryContext, options.Directory, output) }()
	libraryDone := make(chan error, 1)
	runtime.libraryDone = libraryDone
	go func() { libraryDone <- dependencies.runLibrary(runtimeContext, options.Directory, output) }()

	manager, err := dependencies.newManager(providerContext, providerhost.Store{Dir: options.Directory})
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	runtime.manager = manager
	runtime.recorder = dependencies.newRecorder(options.Directory)
	return runtime, nil
}

// Context is canceled with the parent or by Cancel.
func (r *Runtime) Context() context.Context { return r.ctx }

// MemoryContext remains active during ordinary parent cancellation so callers
// can flush workspace archives before Close stops Workspace Memory.
func (r *Runtime) MemoryContext() context.Context { return r.memoryCtx }

func (r *Runtime) Manager() *providerhost.Manager { return r.manager }

func (r *Runtime) Recorder() *usagelog.Recorder { return r.recorder }

// Cancel stops application work and Library while leaving provider and memory
// services available for caller-owned resource cleanup.
func (r *Runtime) Cancel() {
	if r != nil && r.cancelRuntime != nil {
		r.cancelRuntime()
	}
}

// Close stops shared services and waits for their goroutines to finish.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.Cancel()
		var managerErr, recorderErr error
		if r.manager != nil {
			managerErr = r.manager.Close()
		}
		if r.recorder != nil {
			recorderErr = r.recorder.Close()
		}
		if r.cancelProvider != nil {
			r.cancelProvider()
		}
		if r.cancelMemory != nil {
			r.cancelMemory()
		}
		var memoryErr, libraryErr error
		if r.memoryDone != nil {
			memoryErr = ignoreCancellation(<-r.memoryDone)
		}
		if r.libraryDone != nil {
			libraryErr = ignoreCancellation(<-r.libraryDone)
		}
		r.closeErr = errors.Join(managerErr, recorderErr, memoryErr, libraryErr)
	})
	return r.closeErr
}

func ignoreCancellation(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
