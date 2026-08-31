package workspacememory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/snowmerak/q/worklock"
)

const (
	defaultProbeTimeout   = 300 * time.Millisecond
	defaultStartupTimeout = 3 * time.Second
	defaultRequestTimeout = 2 * time.Minute
	defaultLeaseTTL       = 2 * time.Minute
)

type EnsureOptions struct {
	Dir            string
	Config         Config
	ProbeTimeout   time.Duration
	StartupTimeout time.Duration
	RequestTimeout time.Duration
	LeaseTTL       time.Duration
	SweepInterval  time.Duration
}

func (o EnsureOptions) effectiveProbeTimeout() time.Duration {
	if o.ProbeTimeout <= 0 {
		return defaultProbeTimeout
	}
	return o.ProbeTimeout
}

func (o EnsureOptions) effectiveStartupTimeout() time.Duration {
	if o.StartupTimeout <= 0 {
		return defaultStartupTimeout
	}
	return o.StartupTimeout
}

func (o EnsureOptions) effectiveRequestTimeout() time.Duration {
	if o.RequestTimeout <= 0 {
		return defaultRequestTimeout
	}
	return o.RequestTimeout
}

func (o EnsureOptions) effectiveLeaseTTL() time.Duration {
	if o.LeaseTTL <= 0 {
		return defaultLeaseTTL
	}
	return o.LeaseTTL
}

func (o EnsureOptions) effectiveSweepInterval() time.Duration {
	if o.SweepInterval > 0 {
		return o.SweepInterval
	}
	return min(30*time.Second, o.effectiveLeaseTTL()/4)
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
		return nil, errors.New("workspacememory: context is nil")
	}
	if err := os.MkdirAll(options.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("workspacememory: create config directory: %w", err)
	}
	config := options.Config.Effective()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	probe := NewClient(config.Endpoint(), "", options.effectiveProbeTimeout())
	client := NewClient(config.Endpoint(), "", options.effectiveRequestTimeout())
	deadline := time.Now().Add(options.effectiveStartupTimeout())
	delay := 20 * time.Millisecond
	for {
		if health, healthErr := probe.Health(ctx); healthErr == nil {
			if !health.Compatible() {
				return nil, incompatibleError(health)
			}
			if _, statusErr := probe.Status(ctx); statusErr != nil {
				return nil, fmt.Errorf("workspacememory: inspect existing service: %w", statusErr)
			}
			return &Runtime{client: client}, nil
		}

		serviceLock, lockErr := worklock.AcquireFile(options.Dir, ServiceLockFileName, "q workspace memory service")
		if lockErr == nil {
			if health, healthErr := probe.Health(ctx); healthErr == nil {
				_ = serviceLock.Close()
				if !health.Compatible() {
					return nil, incompatibleError(health)
				}
				if _, statusErr := probe.Status(ctx); statusErr != nil {
					return nil, fmt.Errorf("workspacememory: inspect existing service: %w", statusErr)
				}
				return &Runtime{client: client}, nil
			}
			listener, listenErr := net.Listen("tcp", config.ListenAddress())
			if listenErr != nil {
				_ = serviceLock.Close()
				return nil, fmt.Errorf("workspacememory: configured address %s is unavailable: %w", config.ListenAddress(), listenErr)
			}
			leader, startErr := startLeader(ctx, options, listener, serviceLock)
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
			return nil, fmt.Errorf("workspacememory: leader did not become ready within %s", options.effectiveStartupTimeout())
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

func (r *Runtime) IsLeader() bool { return r != nil && r.leader != nil }

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

// Run keeps a foreground service alive and performs follower takeover after a
// leader exits. Callers should cancel ctx only after all remote archive Writers
// have flushed and their Workspace handles have closed.
func Run(ctx context.Context, dir string, output io.Writer) error {
	announced := false
	for ctx.Err() == nil {
		runtime, err := Ensure(ctx, dir)
		if err != nil {
			return err
		}
		if !announced {
			mode := "connected to"
			if runtime.IsLeader() {
				mode = "listening on"
			}
			if output != nil {
				if _, err := fmt.Fprintf(output, "q workspace memory %s %s\n", mode, runtime.Endpoint()); err != nil {
					_ = runtime.Close()
					return err
				}
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
				return errors.New("workspacememory: leader unavailable")
			}
		}
	}
}

func incompatibleError(health Health) error {
	return fmt.Errorf(
		"workspacememory: configured port is occupied by incompatible service %q protocol %d",
		health.Service, health.ProtocolVersion,
	)
}

func IsAddressInUse(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == syscall.Errno(10048)
}
