package studio

import (
	"os"
	"sync"

	"github.com/snowmerak/q/workspace"
)

const maximumSessionSummaryCacheEntries = 2048

type sessionSummaryCacheEntry struct {
	size    int64
	modTime int64
	summary workspace.SessionSummary
}

type sessionSummaryCache struct {
	mu      sync.RWMutex
	entries map[string]sessionSummaryCacheEntry
}

func newSessionSummaryCache() *sessionSummaryCache {
	return &sessionSummaryCache{entries: make(map[string]sessionSummaryCacheEntry)}
}

func (c *sessionSummaryCache) load(store workspace.Store) (workspace.SessionSummary, error) {
	path := store.Path()
	info, err := os.Stat(path)
	if err != nil {
		c.evict(path)
		if os.IsNotExist(err) {
			return workspace.SessionSummary{}, workspace.ErrNotFound
		}
		return store.LoadSummary()
	}

	size := info.Size()
	modTime := info.ModTime().UnixNano()
	c.mu.RLock()
	cached, ok := c.entries[path]
	c.mu.RUnlock()
	if ok && cached.size == size && cached.modTime == modTime {
		return cached.summary, nil
	}

	summary, err := store.LoadSummary()
	if err != nil {
		c.evict(path)
		return workspace.SessionSummary{}, err
	}
	c.mu.Lock()
	if len(c.entries) >= maximumSessionSummaryCacheEntries {
		clear(c.entries)
	}
	c.entries[path] = sessionSummaryCacheEntry{size: size, modTime: modTime, summary: summary}
	c.mu.Unlock()
	return summary, nil
}

func (c *sessionSummaryCache) evict(path string) {
	c.mu.Lock()
	delete(c.entries, path)
	c.mu.Unlock()
}
