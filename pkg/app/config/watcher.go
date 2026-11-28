package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher observes config.json via fsnotify plus a 1s fallback poll and invokes
// onChange only when a complete candidate parses and validates cleanly.
type Watcher struct {
	path     string
	mu       sync.RWMutex
	current  Config
	lastStat modStamp
	onChange func(prev, next Config)
	onError  func(err error)
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

type modStamp struct {
	exists  bool
	modUnix int64
	size    int64
}

func StartWatcher(ctx context.Context, path string, initial Config, onChange func(prev, next Config), onError func(err error)) (*Watcher, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		_ = fsWatcher.Close()
		return nil, err
	}
	if err := fsWatcher.Add(dir); err != nil {
		_ = fsWatcher.Close()
		return nil, err
	}

	watchCtx, cancel := context.WithCancel(ctx)
	w := &Watcher{
		path:     abs,
		current:  initial,
		lastStat: readStamp(abs),
		onChange: onChange,
		onError:  onError,
		cancel:   cancel,
	}
	w.wg.Add(1)
	go w.loop(watchCtx, fsWatcher)
	return w, nil
}

func (w *Watcher) Current() Config {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.current
}

// Update replaces the active snapshot after a programmatic config save.
func (w *Watcher) Update(next Config) {
	w.mu.Lock()
	w.current = next
	w.lastStat = readStamp(w.path)
	w.mu.Unlock()
}

func (w *Watcher) Close() error {
	if w == nil {
		return nil
	}
	w.cancel()
	w.wg.Wait()
	return nil
}

func (w *Watcher) loop(ctx context.Context, fsWatcher *fsnotify.Watcher) {
	defer w.wg.Done()
	defer func() { _ = fsWatcher.Close() }()

	const debounceDelay = 150 * time.Millisecond
	var debounce *time.Timer
	var debounceCh <-chan time.Time
	poll := time.NewTicker(time.Second)
	defer poll.Stop()

	targetBase := filepath.Base(w.path)
	triggerDebounce := func() {
		if debounce == nil {
			debounce = time.NewTimer(debounceDelay)
			debounceCh = debounce.C
		} else {
			if !debounce.Stop() {
				select {
				case <-debounce.C:
				default:
				}
			}
			debounce.Reset(debounceDelay)
		}
	}

	for {
		select {
		case <-ctx.Done():
			if debounce != nil {
				debounce.Stop()
			}
			return
		case ev, ok := <-fsWatcher.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) == targetBase && (ev.Has(fsnotify.Write) || ev.Has(fsnotify.Create) || ev.Has(fsnotify.Rename) || ev.Has(fsnotify.Remove)) {
				triggerDebounce()
			}
		case err, ok := <-fsWatcher.Errors:
			if !ok {
				return
			}
			if w.onError != nil {
				w.onError(err)
			}
		case <-debounceCh:
			debounceCh = nil
			debounce = nil
			w.reloadIfChanged()
		case <-poll.C:
			stamp := readStamp(w.path)
			w.mu.RLock()
			changed := stamp != w.lastStat
			w.mu.RUnlock()
			if changed {
				w.reloadIfChanged()
			}
		}
	}
}

func (w *Watcher) reloadIfChanged() {
	stamp := readStamp(w.path)
	next, err := Load(w.path)
	w.mu.Lock()
	w.lastStat = stamp
	if err != nil {
		w.mu.Unlock()
		if w.onError != nil {
			w.onError(err)
		}
		return
	}
	prev := w.current
	w.current = next
	w.mu.Unlock()
	if w.onChange != nil {
		w.onChange(prev, next)
	}
}

func readStamp(path string) modStamp {
	info, err := os.Stat(path)
	if err != nil {
		return modStamp{exists: false}
	}
	return modStamp{
		exists:  true,
		modUnix: info.ModTime().UnixNano(),
		size:    info.Size(),
	}
}
