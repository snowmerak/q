package app

import (
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/snowmerak/q/internal/embeddingbridge"
)

func (host *SessionHost) workEmbeddingConnection() (string, error) {
	host.providerMu.Lock()
	defer host.providerMu.Unlock()
	if host.manager == nil {
		return "", nil
	}
	if host.embeddingBridgePath == "" {
		host.embeddingBridgePath = filepath.Join(host.store.Dir, "runtime", "work-query-"+uuid.NewString()+".json")
	}
	return host.embeddingBridgePath, host.publishEmbeddingBridge()
}

// providerMu owns the per-host connection file. The MCP child rereads it on
// each query, so replacing Gateway never pins the bridge to a dead process.
func (host *SessionHost) publishEmbeddingBridge() error {
	if host.embeddingBridgePath == "" || host.manager == nil {
		return nil
	}
	endpoint, key := host.manager.Connection()
	return embeddingbridge.Write(host.embeddingBridgePath, embeddingbridge.Connection{Endpoint: endpoint, APIKey: key})
}

func (host *SessionHost) removeEmbeddingBridge() {
	host.providerMu.Lock()
	defer host.providerMu.Unlock()
	if host.embeddingBridgePath != "" {
		_ = os.Remove(host.embeddingBridgePath)
		host.embeddingBridgePath = ""
	}
}
