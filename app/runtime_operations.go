package app

import (
	"context"
	"strings"
	"sync"
	"time"

	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/usagelog"
	"github.com/snowmerak/q/workspacememory"
)

type RuntimeServiceStatus struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Endpoint string `json:"endpoint,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Leader   bool   `json:"leader,omitempty"`
}

type RuntimeStatus struct {
	Services []RuntimeServiceStatus `json:"services"`
	Logs     []string               `json:"logs"`
}

type runtimeLogBuffer struct {
	mu      sync.Mutex
	maximum int
	lines   []string
}

func newRuntimeLogBuffer(maximum int) *runtimeLogBuffer {
	return &runtimeLogBuffer{maximum: maximum}
}

func (buffer *runtimeLogBuffer) Write(value []byte) (int, error) {
	if buffer == nil {
		return len(value), nil
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	for _, line := range strings.Split(strings.ReplaceAll(string(value), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 8192 {
			line = line[:8192] + "…"
		}
		buffer.lines = append(buffer.lines, line)
	}
	if extra := len(buffer.lines) - buffer.maximum; extra > 0 {
		copy(buffer.lines, buffer.lines[extra:])
		buffer.lines = buffer.lines[:buffer.maximum]
	}
	return len(value), nil
}

func (buffer *runtimeLogBuffer) snapshot() []string {
	if buffer == nil {
		return nil
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]string(nil), buffer.lines...)
}

func (host *SessionHost) RuntimeStatus(ctx context.Context) RuntimeStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	result := RuntimeStatus{Logs: host.logs.snapshot()}

	host.providerMu.Lock()
	gatewayEndpoint := host.manager.Endpoint()
	gatewayReady := host.providerReady && gatewayEndpoint != ""
	host.providerMu.Unlock()
	gatewayState := "idle"
	if gatewayReady {
		gatewayState = "ready"
	}
	result.Services = append(result.Services, RuntimeServiceStatus{ID: "gateway", State: gatewayState, Endpoint: gatewayEndpoint})

	libraryConfig, libraryErr := (qlibrary.ConfigStore{Dir: host.store.Dir}).LoadOrDefault()
	if libraryErr != nil {
		result.Services = append(result.Services, RuntimeServiceStatus{ID: "library", State: "error", Detail: libraryErr.Error()})
	} else {
		status := RuntimeServiceStatus{ID: "library", State: "unavailable", Endpoint: libraryConfig.Endpoint()}
		probeContext, cancel := context.WithTimeout(ctx, time.Second)
		if health, err := qlibrary.NewClient(libraryConfig.Endpoint(), "", time.Second).Health(probeContext); err != nil {
			status.Detail = err.Error()
		} else if health.Compatible() {
			status.State = "ready"
		} else {
			status.State, status.Detail = "incompatible", "Library protocol is incompatible"
		}
		cancel()
		result.Services = append(result.Services, status)
	}

	memoryConfig, memoryErr := (workspacememory.ConfigStore{Dir: host.store.Dir}).LoadOrDefault()
	if memoryErr != nil {
		result.Services = append(result.Services, RuntimeServiceStatus{ID: "workspace-memory", State: "error", Detail: memoryErr.Error()})
	} else {
		status := RuntimeServiceStatus{ID: "workspace-memory", State: "unavailable", Endpoint: memoryConfig.Endpoint()}
		probeContext, cancel := context.WithTimeout(ctx, time.Second)
		if health, err := workspacememory.NewClient(memoryConfig.Endpoint(), "", time.Second).Health(probeContext); err != nil {
			status.Detail = err.Error()
		} else if health.Compatible() {
			status.State = "ready"
		} else {
			status.State, status.Detail = "incompatible", "Workspace Memory protocol is incompatible"
		}
		cancel()
		result.Services = append(result.Services, status)
	}

	usage := RuntimeServiceStatus{ID: "usage", State: "unavailable"}
	probeContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	if health, endpoint, leader, err := host.runtime.Recorder().Health(probeContext); err != nil {
		usage.Detail = err.Error()
	} else {
		usage.Endpoint, usage.Leader = endpoint, leader
		if health.Compatible() {
			usage.State = "ready"
		} else {
			usage.State, usage.Detail = "incompatible", "Usage protocol is incompatible"
		}
	}
	cancel()
	result.Services = append(result.Services, usage)
	return result
}

func (host *SessionHost) Usage(ctx context.Context, filter usagelog.Filter) (usagelog.UsageView, error) {
	return host.runtime.Recorder().Query(ctx, filter)
}
