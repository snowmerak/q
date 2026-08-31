package library

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

	qconfig "github.com/snowmerak/q/config"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/worklock"
)

type EnsureOptions struct {
	Dir            string
	Config         Config
	Vector         sessionstore.VectorConfig
	Judge          PropositionJudge
	ProbeTimeout   time.Duration
	StartupTimeout time.Duration
}

type Runtime struct {
	client   *Client
	leader   *leader
	once     sync.Once
	closeErr error
}

func Ensure(ctx context.Context, dir string) (*Runtime, error) {
	value, err := (ConfigStore{Dir: dir}).LoadOrDefault()
	if err != nil {
		return nil, err
	}
	options := EnsureOptions{Dir: dir, Config: value}
	if global, loadErr := (qconfig.Store{Dir: dir}).Load(); loadErr == nil {
		options.Vector = sessionstore.VectorConfig{
			Model: global.Embedding.Model, Dimensions: global.Embedding.Dimensions,
		}
	}
	return EnsureWithOptions(ctx, options)
}

func EnsureWithOptions(ctx context.Context, options EnsureOptions) (*Runtime, error) {
	if err := os.MkdirAll(options.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("library: create config directory: %w", err)
	}
	value := options.Config.Effective()
	if err := value.Validate(); err != nil {
		return nil, err
	}
	probeTimeout := options.ProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = 300 * time.Millisecond
	}
	startupTimeout := options.StartupTimeout
	if startupTimeout <= 0 {
		startupTimeout = 3 * time.Second
	}
	client := NewClient(value.Endpoint(), "", probeTimeout)
	deadline := time.Now().Add(startupTimeout)
	delay := 20 * time.Millisecond
	for {
		if health, err := client.Health(ctx); err == nil {
			if !health.Compatible() {
				return nil, incompatibleError(health)
			}
			return &Runtime{client: client}, nil
		}

		lock, err := worklock.AcquireFile(options.Dir, LockFileName, "q library")
		if err == nil {
			if health, probeErr := client.Health(ctx); probeErr == nil {
				_ = lock.Close()
				if !health.Compatible() {
					return nil, incompatibleError(health)
				}
				return &Runtime{client: client}, nil
			}
			listener, listenErr := net.Listen("tcp", value.ListenAddress())
			if listenErr != nil {
				_ = lock.Close()
				return nil, fmt.Errorf("library: configured address %s is unavailable: %w", value.ListenAddress(), listenErr)
			}
			leader, startErr := startLeader(ctx, options.Dir, options.Vector, options.Judge, listener, lock)
			if startErr != nil {
				_ = listener.Close()
				_ = lock.Close()
				return nil, startErr
			}
			return &Runtime{client: client, leader: leader}, nil
		}
		if !errors.Is(err, worklock.ErrLocked) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("library: leader did not become ready within %s", startupTimeout)
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

func (r *Runtime) Client() *Client { return r.client }

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
			if _, err := fmt.Fprintf(output, "q library %s %s\n", mode, runtime.Endpoint()); err != nil {
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
		} else {
			if waitForFailure(ctx, runtime.client) == nil {
				_ = runtime.Close()
				return nil
			}
		}
		_ = runtime.Close()
	}
	return nil
}

func waitUntilReady(ctx context.Context, client *Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	delay := 20 * time.Millisecond
	for {
		health, err := client.Health(ctx)
		if err == nil {
			if !health.Compatible() {
				return incompatibleError(health)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("library: leader did not become ready within %s: %w", timeout, err)
		}
		jitter := time.Duration(rand.IntN(max(1, int(delay/3))))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay + jitter):
		}
		delay = min(delay*2, 500*time.Millisecond)
	}
}

func waitForFailure(ctx context.Context, client *Client) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			health, err := client.Health(ctx)
			if err != nil || !health.Compatible() {
				return errors.New("library: leader unavailable")
			}
		}
	}
}

func incompatibleError(health Health) error {
	return fmt.Errorf(
		"library: configured port is occupied by incompatible service %q protocol %d",
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
