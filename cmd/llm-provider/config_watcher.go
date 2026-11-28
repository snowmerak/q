package main

import (
	"context"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

const configReloadDebounce = 200 * time.Millisecond

type configWatcher struct {
	watcher    *fsnotify.Watcher
	configPath string
}

func newConfigWatcher(path string) (*configWatcher, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	watcher, err := fsnotify.NewBufferedWatcher(128)
	if err != nil {
		return nil, err
	}
	if err := watcher.Add(filepath.Dir(absolutePath)); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	return &configWatcher{watcher: watcher, configPath: filepath.Clean(absolutePath)}, nil
}

func (w *configWatcher) Run(ctx context.Context, reload func(context.Context)) {
	workerContext, cancelWorker := context.WithCancel(ctx)
	reloadRequests := make(chan struct{}, 1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-workerContext.Done():
				return
			case <-reloadRequests:
				reload(workerContext)
			}
		}
	}()
	defer func() {
		cancelWorker()
		<-workerDone
	}()

	var timer *time.Timer
	var timerChannel <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if !w.matches(event.Name) || !isConfigChange(event) {
				continue
			}
			if timer == nil {
				timer = time.NewTimer(configReloadDebounce)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(configReloadDebounce)
			}
			timerChannel = timer.C
		case <-timerChannel:
			timerChannel = nil
			select {
			case reloadRequests <- struct{}{}:
			default:
			}
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("configuration watcher: %v", err)
		}
	}
}

func (w *configWatcher) matches(path string) bool {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absolutePath = filepath.Clean(absolutePath)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(absolutePath, w.configPath)
	}
	return absolutePath == w.configPath
}

func (w *configWatcher) Close() error {
	return w.watcher.Close()
}

func isConfigChange(event fsnotify.Event) bool {
	return event.Has(fsnotify.Write) || event.Has(fsnotify.Create) ||
		event.Has(fsnotify.Rename) || event.Has(fsnotify.Remove)
}
