package providerhost

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
)

const modelDiscoveryTimeout = 1500 * time.Millisecond

type Manager struct {
	store      Store
	supervisor *Supervisor
	mu         sync.RWMutex
	config     gateway.Config
}

func NewManager(ctx context.Context, store Store) (*Manager, error) {
	supervisor, err := NewSupervisor(ctx, store)
	if err != nil {
		return nil, err
	}
	return &Manager{store: store, supervisor: supervisor}, nil
}

func (m *Manager) LoadAndStart(ctx context.Context) error {
	value, err := m.store.Load()
	if err != nil {
		return err
	}
	value = boundedModelDiscovery(value)
	m.mu.Lock()
	m.config = cloneConfig(value)
	m.mu.Unlock()
	prepared, err := m.supervisor.Prepare(ctx, value)
	if err != nil {
		return err
	}
	m.supervisor.Activate(prepared)
	return nil
}

// Apply starts a replacement child before persisting or activating it. Provider
// connectivity is deliberately not checked here: an unavailable upstream must
// not prevent its settings from being saved. A child startup failure still
// leaves the current Gateway untouched.
func (m *Manager) Apply(ctx context.Context, value gateway.Config) error {
	value.Listen = "127.0.0.1:0"
	value = boundedModelDiscovery(value)
	prepared, err := m.supervisor.Prepare(ctx, value)
	if err != nil {
		return err
	}
	if err := m.store.Save(value); err != nil {
		prepared.close()
		return err
	}
	m.supervisor.Activate(prepared)
	m.mu.Lock()
	m.config = cloneConfig(value)
	m.mu.Unlock()
	return nil
}

func boundedModelDiscovery(value gateway.Config) gateway.Config {
	value.ModelCacheRefreshTimeout = modelDiscoveryTimeout.String()
	return value
}

func (m *Manager) Config() gateway.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneConfig(m.config)
}

func (m *Manager) Endpoint() string { return m.supervisor.Endpoint() }

func (m *Manager) Close() error { return m.supervisor.Close() }

func cloneConfig(value gateway.Config) gateway.Config {
	result := value
	result.Providers = append([]gateway.ProviderConfig(nil), value.Providers...)
	return result
}

func LegacyProvider(id, baseURL, apiKeyEnv, apiKey string) (gateway.ProviderConfig, error) {
	if id == "" {
		id = "default"
	}
	if baseURL == "" {
		return gateway.ProviderConfig{}, fmt.Errorf("providerhost: legacy provider has no base URL")
	}
	return gateway.ProviderConfig{
		ID: id, Type: "openai-compatible", Enabled: true,
		BaseURL: baseURL, APIKeyEnv: apiKeyEnv, APIKey: apiKey,
	}, nil
}
