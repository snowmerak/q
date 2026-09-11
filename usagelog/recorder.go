// Package usagelog owns q's user-level token usage service and durable stores.
package usagelog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/client"
)

const recorderDeadline = 250 * time.Millisecond

// Recorder is a bounded best-effort proxy to the user-level Usage service.
// Client instrumentation intentionally ignores its errors, so telemetry can
// delay a completed model call by at most recorderDeadline but cannot fail it.
type Recorder struct {
	Dir string

	once     sync.Once
	gate     chan struct{}
	runtime  *Runtime
	now      func() time.Time
	ensure   func(context.Context, EnsureOptions) (*Runtime, error)
	lifetime context.Context
	cancel   context.CancelFunc
}

func New(configDir string) *Recorder {
	return &Recorder{Dir: strings.TrimSpace(configDir), now: time.Now}
}

func (r *Recorder) Directory() string {
	if r == nil || strings.TrimSpace(r.Dir) == "" {
		return ""
	}
	return r.Dir
}

func (r *Recorder) RecordUsage(record client.UsageRecord) error {
	if r == nil || r.Directory() == "" {
		return errors.New("usage: config directory is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), recorderDeadline)
	defer cancel()
	r.initializeGate()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.gate:
	}
	result := make(chan error, 1)
	go func() {
		defer func() { r.gate <- struct{}{} }()
		result <- r.recordUsage(ctx, record)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-result:
		return err
	}
}

func (r *Recorder) recordUsage(ctx context.Context, record client.UsageRecord) error {
	if record.EventID == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		record.EventID = hex.EncodeToString(id[:])
	}
	if record.At.IsZero() {
		record.At = r.currentTime()
	}
	if record.Role == "" {
		record.Role = client.UsageRoleUnknown
	}
	for attempt := 0; attempt < 2; attempt++ {
		if r.runtime == nil {
			if r.lifetime == nil {
				r.lifetime, r.cancel = context.WithCancel(context.Background())
			}
			configured, err := (ConfigStore{Dir: r.Dir}).LoadOrDefault()
			if err != nil {
				return err
			}
			ensure := r.ensure
			if ensure == nil {
				ensure = EnsureWithOptions
			}
			runtime, err := ensure(ctx, EnsureOptions{
				Dir: r.Dir, Config: configured, LeaderContext: r.lifetime, ProbeTimeout: 50 * time.Millisecond,
				StartupTimeout: 175 * time.Millisecond, RequestTimeout: 125 * time.Millisecond,
			})
			if err != nil {
				return err
			}
			r.runtime = runtime
		}
		if err := r.runtime.Client().Append(ctx, record); err == nil {
			return nil
		} else if attempt == 1 {
			return err
		}
		_ = r.runtime.Close()
		r.runtime = nil
	}
	return nil
}

func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.initializeGate()
	<-r.gate
	defer func() { r.gate <- struct{}{} }()
	if r.runtime == nil {
		if r.cancel != nil {
			r.cancel()
			r.cancel = nil
			r.lifetime = nil
		}
		return nil
	}
	if r.cancel != nil {
		r.cancel()
	}
	err := r.runtime.Close()
	r.runtime = nil
	r.cancel = nil
	r.lifetime = nil
	return err
}

func (r *Recorder) initializeGate() {
	r.once.Do(func() {
		r.gate = make(chan struct{}, 1)
		r.gate <- struct{}{}
	})
}

func (r *Recorder) currentTime() time.Time {
	if r != nil && r.now != nil {
		return r.now().UTC()
	}
	return time.Now().UTC()
}

var _ client.UsageRecorder = (*Recorder)(nil)
