package usagelog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"time"

	"github.com/snowmerak/q/worklock"
)

const (
	defaultProbeTimeout   = 300 * time.Millisecond
	defaultStartupTimeout = 3 * time.Second
	defaultRequestTimeout = 5 * time.Second
)

type EnsureOptions struct {
	Dir            string
	Config         Config
	LeaderContext  context.Context
	ProbeTimeout   time.Duration
	StartupTimeout time.Duration
	RequestTimeout time.Duration
}

type Runtime struct {
	client   *Client
	leader   *leader
	once     sync.Once
	closeErr error
}

func Ensure(ctx context.Context, dir string) (*Runtime, error) {
	config, err := (ConfigStore{Dir: dir}).LoadOrDefault()
	if err != nil {
		return nil, err
	}
	return EnsureWithOptions(ctx, EnsureOptions{Dir: dir, Config: config})
}

func EnsureWithOptions(ctx context.Context, options EnsureOptions) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("usage: context is nil")
	}
	if err := os.MkdirAll(options.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("usage: create config directory: %w", err)
	}
	config := options.Config.Effective()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	probeTimeout := durationOr(options.ProbeTimeout, defaultProbeTimeout)
	startupTimeout := durationOr(options.StartupTimeout, defaultStartupTimeout)
	requestTimeout := durationOr(options.RequestTimeout, defaultRequestTimeout)
	probe := NewClient(config.Endpoint(), probeTimeout)
	client := NewClient(config.Endpoint(), requestTimeout)
	deadline := time.Now().Add(startupTimeout)
	delay := 20 * time.Millisecond
	for {
		if health, err := probe.Health(ctx); err == nil {
			if !health.Compatible() {
				return nil, incompatibleError(health)
			}
			return &Runtime{client: client}, nil
		}
		serviceLock, lockErr := worklock.AcquireFile(options.Dir, ServiceLockFileName, "q usage service")
		if lockErr == nil {
			if health, healthErr := probe.Health(ctx); healthErr == nil {
				_ = serviceLock.Close()
				if !health.Compatible() {
					return nil, incompatibleError(health)
				}
				return &Runtime{client: client}, nil
			}
			listener, listenErr := net.Listen("tcp", config.ListenAddress())
			if listenErr != nil {
				_ = serviceLock.Close()
				return nil, fmt.Errorf("usage: configured address %s is unavailable: %w", config.ListenAddress(), listenErr)
			}
			leaderContext := options.LeaderContext
			if leaderContext == nil {
				leaderContext = ctx
			}
			leader, startErr := startLeader(leaderContext, options.Dir, listener, serviceLock)
			if startErr != nil {
				_ = listener.Close()
				_ = serviceLock.Close()
				return nil, startErr
			}
			return &Runtime{client: client, leader: leader}, nil
		}
		if !errors.Is(lockErr, worklock.ErrLocked) {
			return nil, lockErr
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("usage: leader did not become ready within %s", startupTimeout)
		}
		jitter := time.Duration(rand.IntN(max(1, int(delay/3))))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay + jitter):
		}
		delay = min(delay*2, 500*time.Millisecond)
	}
}

func (r *Runtime) Client() *Client {
	if r == nil {
		return nil
	}
	return r.client
}

func (r *Runtime) Endpoint() string {
	if r == nil || r.client == nil {
		return ""
	}
	return r.client.Endpoint()
}

func (r *Runtime) DashboardURL() string { return r.Endpoint() + "/" }
func (r *Runtime) IsLeader() bool       { return r != nil && r.leader != nil }

func (r *Runtime) Done() <-chan struct{} {
	if r == nil || r.leader == nil {
		return nil
	}
	return r.leader.Done()
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		if r.leader != nil {
			r.closeErr = r.leader.Close()
		}
	})
	return r.closeErr
}

func Run(ctx context.Context, dir string, output io.Writer) error {
	announced := false
	for ctx.Err() == nil {
		runtime, err := Ensure(ctx, dir)
		if err != nil {
			return err
		}
		if !announced && output != nil {
			mode := "connected to"
			if runtime.IsLeader() {
				mode = "listening on"
			}
			if _, err := fmt.Fprintf(output, "q usage %s %s\n", mode, runtime.DashboardURL()); err != nil {
				_ = runtime.Close()
				return err
			}
			announced = true
		}
		if runtime.IsLeader() {
			select {
			case <-ctx.Done():
				return runtime.Close()
			case <-runtime.Done():
				_ = runtime.Close()
			}
		} else if waitForFailure(ctx, runtime.client) == nil {
			_ = runtime.Close()
			return nil
		}
		_ = runtime.Close()
	}
	return nil
}

func waitForFailure(ctx context.Context, client *Client) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			probeContext, cancel := context.WithTimeout(ctx, defaultProbeTimeout)
			health, err := client.Health(probeContext)
			cancel()
			if err != nil || !health.Compatible() {
				return errors.New("usage: leader unavailable")
			}
		}
	}
}

func incompatibleError(health Health) error {
	return fmt.Errorf("usage: configured port is occupied by incompatible service %q protocol %d", health.Service, health.ProtocolVersion)
}

func durationOr(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
