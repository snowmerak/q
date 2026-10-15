package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const maximumRecordBytes = 32 << 10

// Options are effective Studio sink settings; byte limits include JSON and LF.
type Options struct {
	Level         slog.Level
	Stdout        bool
	FileEnabled   bool
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxAge        time.Duration
}

// Runtime owns bounded, independent local sinks. Close it after all producers.
type Runtime struct {
	logger     *slog.Logger
	sinks      []*asyncSink
	sequence   atomic.Uint64
	instance   string
	reportMu   sync.Mutex
	lastReport time.Time
	fallback   io.Writer
}

type asyncSink struct {
	queue    chan []byte
	done     chan struct{}
	mu       sync.RWMutex
	closed   bool
	dropped  atomic.Uint64
	failed   atomic.Uint64
	write    func([][]byte) error
	maintain func() error
	report   func(string)
}

// Open starts diagnostic sinks. Filesystem failures are reported independently
// and retried by the worker rather than preventing Studio from serving work.
func Open(directory string, options Options, stdout, fallback io.Writer) (*Runtime, error) {
	if options.FileEnabled && (options.MaxFileBytes < 1 || options.MaxTotalBytes < options.MaxFileBytes || options.MaxAge <= 0) {
		return nil, errors.New("diagnostics: invalid retention limits")
	}
	runtime := &Runtime{instance: uuid.NewString(), fallback: fallback}
	if options.FileEnabled {
		store := newFileStore(directory, runtime.instance, options)
		runtime.addSink(store.write, store.maintain)
	}
	if options.Stdout {
		if stdout == nil {
			stdout = io.Discard
		}
		runtime.addSink(func(batch [][]byte) error {
			for _, body := range batch {
				n, err := stdout.Write(body)
				if err != nil {
					return err
				}
				if n != len(body) {
					return io.ErrShortWrite
				}
			}
			return nil
		}, nil)
	}
	handler := slog.NewJSONHandler(runtime, &slog.HandlerOptions{Level: options.Level, ReplaceAttr: safeAttribute})
	runtime.logger = slog.New(&eventHandler{Handler: handler, runtime: runtime}).With("schema_version", 1, "service", "studio", "instance_id", runtime.instance)
	if len(runtime.sinks) == 0 {
		runtime.logger = slog.New(slog.DiscardHandler)
	}
	for _, sink := range runtime.sinks {
		go sink.run()
	}
	return runtime, nil
}

func (r *Runtime) Logger() *slog.Logger { return r.logger }

func (r *Runtime) addSink(write func([][]byte) error, maintain func() error) {
	sink := &asyncSink{queue: make(chan []byte, 256), done: make(chan struct{}), write: write, maintain: maintain, report: r.report}
	r.sinks = append(r.sinks, sink)
}

// Write receives one encoded JSON record. Queue pressure never blocks callers.
func (r *Runtime) Write(body []byte) (int, error) {
	if len(body) > maximumRecordBytes {
		for _, sink := range r.sinks {
			sink.dropped.Add(1)
		}
		return len(body), nil
	}
	copy := append([]byte(nil), body...)
	for _, sink := range r.sinks {
		sink.mu.RLock()
		if sink.closed {
			sink.dropped.Add(1)
		} else {
			select {
			case sink.queue <- copy:
			default:
				sink.dropped.Add(1)
			}
		}
		sink.mu.RUnlock()
	}
	return len(body), nil
}

func (r *Runtime) report(reason string) {
	r.reportMu.Lock()
	defer r.reportMu.Unlock()
	if r.fallback == nil || time.Since(r.lastReport) < time.Minute {
		return
	}
	r.lastReport = time.Now()
	// Reason is an internal code. Never print raw filesystem/provider errors.
	dropped, failed := r.counts()
	_, _ = fmt.Fprintf(r.fallback, "q studio: diagnostics degraded (%s; sink_records_dropped=%d; sink_failures=%d)\n", reason, dropped, failed)
}

func (r *Runtime) counts() (dropped, failed uint64) {
	for _, sink := range r.sinks {
		dropped += sink.dropped.Load()
		failed += sink.failed.Load()
	}
	return
}

func (s *asyncSink) run() {
	defer close(s.done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	maintain := func() {
		if s.maintain != nil {
			if err := s.maintain(); err != nil {
				s.failed.Add(1)
				s.report("file_maintenance_failed")
			}
		}
	}
	maintain()
	for {
		select {
		case body, ok := <-s.queue:
			if !ok {
				return
			}
			batch := [][]byte{body}
		gather:
			for len(batch) < 64 {
				select {
				case next, open := <-s.queue:
					if !open {
						break gather
					}
					batch = append(batch, next)
				default:
					break gather
				}
			}
			if err := s.write(batch); err != nil {
				s.failed.Add(1)
				s.dropped.Add(uint64(len(batch)))
				s.report("sink_write_failed")
			}
			if s.dropped.Load() > 0 {
				s.report("records_dropped")
			}
		case <-ticker.C:
			maintain()
		}
	}
}

// Close stops admission and drains accepted records within the caller's budget.
// It never closes stdout or the fallback stream.
func (r *Runtime) Close(ctx context.Context) error {
	for _, sink := range r.sinks {
		sink.mu.Lock()
		if !sink.closed {
			sink.closed = true
			close(sink.queue)
		}
		sink.mu.Unlock()
	}
	for _, sink := range r.sinks {
		select {
		case <-sink.done:
		case <-ctx.Done():
			return errors.New("diagnostics: drain timeout")
		}
	}
	if dropped, failed := r.counts(); dropped > 0 || failed > 0 {
		return fmt.Errorf("diagnostics: sink_records_dropped=%d; sink_failures=%d", dropped, failed)
	}
	return nil
}

type eventHandler struct {
	slog.Handler
	runtime *Runtime
}

func (h *eventHandler) Handle(ctx context.Context, record slog.Record) error {
	record = record.Clone()
	record.AddAttrs(slog.String("event", record.Message), slog.String("event_id", fmt.Sprintf("%s-%d", h.runtime.instance, h.runtime.sequence.Add(1))))
	return h.Handler.Handle(ctx, record)
}
func (h *eventHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &eventHandler{Handler: h.Handler.WithAttrs(attrs), runtime: h.runtime}
}
func (h *eventHandler) WithGroup(name string) slog.Handler {
	return &eventHandler{Handler: h.Handler.WithGroup(name), runtime: h.runtime}
}

func safeAttribute(_ []string, attr slog.Attr) slog.Attr {
	switch strings.ToLower(attr.Key) {
	case "content", "body", "prompt", "arguments", "authorization", "cookie", "api_key", "token", "password", "detail":
		return slog.Attr{}
	case "error":
		if attr.Value.Kind() == slog.KindAny && attr.Value.Any() != nil {
			return slog.String("error_type", fmt.Sprintf("%T", attr.Value.Any()))
		}
		return slog.Attr{}
	case slog.TimeKey:
		if attr.Value.Kind() == slog.KindTime {
			attr.Value = slog.TimeValue(attr.Value.Time().UTC())
		}
	}
	if attr.Value.Kind() == slog.KindString {
		value := attr.Value.String()
		if len(value) > 2048 {
			limit := 2048
			for !utf8.ValidString(value[:limit]) {
				limit--
			}
			return slog.Group(attr.Key, slog.String("value", value[:limit]), slog.Bool("truncated", true), slog.Int("omitted_bytes", len(value)-limit))
		}
	}
	return attr
}
