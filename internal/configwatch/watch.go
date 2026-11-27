// Package configwatch reports content changes to Q configuration files.
//
// Atomic replacement can preserve a file's modification time and size, so
// the watcher compares file contents instead of metadata. It watches each
// file's parent directory, which keeps replacement and creation visible, and
// coalesces bursts of events before rereading the affected files.
package configwatch

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/snowmerak/q/internal/fsopen"
)

const (
	defaultDebounce     = 100 * time.Millisecond
	defaultPollInterval = 2 * time.Second
	readRetries         = 3
)

// Options tunes a Watcher. The zero value uses the defaults.
type Options struct {
	// Debounce is how long the watcher waits after the last event before it
	// rereads the affected files.
	Debounce time.Duration
	// PollInterval is how often the watcher rescans when the operating system
	// cannot watch a parent directory, such as when it does not exist yet.
	PollInterval time.Duration
}

// Change lists the watched paths whose contents differ from the previous
// report, in the order they were given to New.
type Change struct {
	Paths []string
}

// Watcher reports content changes to a fixed set of files.
type Watcher struct {
	paths   []string
	options Options
	changes chan Change
	done    chan struct{}
	digests map[string]digest

	notifier  *fsnotify.Watcher
	unwatched bool
}

type digest struct {
	exists bool
	sum    [sha256.Size]byte
}

// New records the current contents of paths and reports later changes on
// Changes until ctx ends.
func New(ctx context.Context, paths []string, options Options) *Watcher {
	if options.Debounce <= 0 {
		options.Debounce = defaultDebounce
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultPollInterval
	}
	w := &Watcher{
		paths:   make([]string, 0, len(paths)),
		options: options,
		changes: make(chan Change, 1),
		done:    make(chan struct{}),
		digests: make(map[string]digest, len(paths)),
	}
	for _, path := range paths {
		path = filepath.Clean(path)
		if slices.Contains(w.paths, path) {
			continue
		}
		w.paths = append(w.paths, path)
		if value, ok := read(path); ok {
			w.digests[path] = value
		}
	}
	// Register the watches before returning so that a write made right after
	// New is observed.
	if notifier, err := fsnotify.NewWatcher(); err == nil {
		w.notifier = notifier
	}
	w.unwatched = w.addDirectories(w.notifier)
	go w.run(ctx)
	return w
}

// Changes delivers content changes. A pending change that has not been
// received is merged with the next one.
func (w *Watcher) Changes() <-chan Change { return w.changes }

// Done is closed after the watcher stops.
func (w *Watcher) Done() <-chan struct{} { return w.done }

func (w *Watcher) run(ctx context.Context) {
	defer close(w.done)
	notifier, unwatched := w.notifier, w.unwatched
	var events <-chan fsnotify.Event
	var failures <-chan error
	if notifier != nil {
		defer func() { _ = notifier.Close() }()
		events, failures = notifier.Events, notifier.Errors
	}

	poll := time.NewTicker(w.options.PollInterval)
	defer poll.Stop()
	debounce := time.NewTimer(time.Hour)
	stopTimer(debounce)
	pending := make(map[string]bool)
	schedule := func(paths ...string) {
		for _, path := range paths {
			pending[path] = true
		}
		stopTimer(debounce)
		debounce.Reset(w.options.Debounce)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				events, unwatched = nil, true
				continue
			}
			if path := filepath.Clean(event.Name); slices.Contains(w.paths, path) {
				schedule(path)
			}
		case err, ok := <-failures:
			if !ok {
				failures = nil
				continue
			}
			// An overflow drops events, so every file must be rescanned.
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				schedule(w.paths...)
			}
		case <-poll.C:
			// Directories that could not be watched are polled, and newly
			// created ones are picked up so later events arrive promptly.
			if unwatched || notifier == nil {
				if notifier != nil {
					unwatched = w.addDirectories(notifier)
				}
				schedule(w.paths...)
			}
		case <-debounce.C:
			changed, failed := w.rescan(pending)
			clear(pending)
			if len(failed) > 0 {
				// A writer may still hold the file; reread it shortly.
				schedule(failed...)
			}
			if len(changed) > 0 {
				w.publish(ctx, changed)
			}
		}
	}
}

// addDirectories watches each parent directory and reports whether any of
// them could not be watched.
func (w *Watcher) addDirectories(notifier *fsnotify.Watcher) bool {
	if notifier == nil {
		return true
	}
	watched := notifier.WatchList()
	unwatched := false
	for _, path := range w.paths {
		directory := filepath.Dir(path)
		if slices.Contains(watched, directory) {
			continue
		}
		if err := notifier.Add(directory); err != nil {
			unwatched = true
			continue
		}
		watched = append(watched, directory)
	}
	return unwatched
}

func (w *Watcher) rescan(pending map[string]bool) (changed, failed []string) {
	for _, path := range w.paths {
		if !pending[path] {
			continue
		}
		value, ok := read(path)
		if !ok {
			failed = append(failed, path)
			continue
		}
		if value == w.digests[path] {
			continue
		}
		w.digests[path] = value
		changed = append(changed, path)
	}
	return changed, failed
}

func (w *Watcher) publish(ctx context.Context, changed []string) {
	select {
	case previous := <-w.changes:
		for _, path := range previous.Paths {
			if !slices.Contains(changed, path) {
				changed = append(changed, path)
			}
		}
		slices.SortFunc(changed, func(left, right string) int {
			return slices.Index(w.paths, left) - slices.Index(w.paths, right)
		})
	default:
	}
	select {
	case w.changes <- Change{Paths: changed}:
	case <-ctx.Done():
	}
}

// read returns the file's digest. A missing file has a digest of its own.
// It reports false for other errors, which leave the previous digest in place.
func read(path string) (digest, bool) {
	for attempt := 0; ; attempt++ {
		body, err := fsopen.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return digest{}, true
		}
		if err == nil {
			return digest{exists: true, sum: sha256.Sum256(body)}, true
		}
		if attempt+1 == readRetries {
			return digest{}, false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
