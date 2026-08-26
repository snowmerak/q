package sessionstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const defaultWriterBuffer = 256
const defaultWriterBatchSize = 32

var (
	ErrWriterClosed    = errors.New("sessionstore: writer is closed")
	ErrWriterQueueFull = errors.New("sessionstore: writer queue is full")
)

type writeRequest struct {
	record *Record
	flush  chan struct{}
}

// RecordBatchPreparer may enrich records before they are persisted. Writer
// falls back to the original records when preparation fails so optional
// derived data cannot cause archive loss.
type RecordBatchPreparer func(context.Context, []Record) ([]Record, error)

type WriterOptions struct {
	Buffer    int
	BatchSize int
	Context   context.Context
	Prepare   RecordBatchPreparer
}

// WriterStore is the persistence boundary owned by Writer. Implementations
// may be the local Store or a client for a process that owns the workspace
// archive. Close releases that implementation when Writer is closed.
type WriterStore interface {
	Save(Record) (Record, error)
	SaveBatch([]Record) ([]Record, error)
	Close() error
}

// Writer serializes archive writes on a background goroutine. Append never
// waits for disk I/O; Flush and Close provide explicit durability barriers.
type Writer struct {
	store     WriterStore
	queue     chan writeRequest
	done      chan struct{}
	ctx       context.Context
	prepare   RecordBatchPreparer
	batchSize int

	mu       sync.Mutex
	closed   bool
	errMu    sync.Mutex
	firstErr error
}

func NewWriter(store WriterStore, buffer int) *Writer {
	return NewWriterWithOptions(store, WriterOptions{Buffer: buffer})
}

func NewWriterWithOptions(store WriterStore, options WriterOptions) *Writer {
	if options.Buffer <= 0 {
		options.Buffer = defaultWriterBuffer
	}
	if options.BatchSize <= 0 {
		options.BatchSize = defaultWriterBatchSize
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	w := &Writer{
		store: store,
		queue: make(chan writeRequest, options.Buffer),
		done:  make(chan struct{}),
		ctx:   options.Context, prepare: options.Prepare, batchSize: options.BatchSize,
	}
	go w.run()
	return w
}

// Append queues a record without waiting for filesystem or index work.
func (w *Writer) Append(record Record) error {
	if w == nil {
		return ErrWriterClosed
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrWriterClosed
	}
	copy := cloneRecord(record)
	select {
	case w.queue <- writeRequest{record: &copy}:
		return nil
	default:
		return ErrWriterQueueFull
	}
}

// Flush waits until all records queued before it have been processed.
func (w *Writer) Flush() error {
	if w == nil {
		return ErrWriterClosed
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ErrWriterClosed
	}
	flushed := make(chan struct{})
	w.queue <- writeRequest{flush: flushed}
	w.mu.Unlock()
	<-flushed
	return w.Err()
}

// Err reports the first background persistence error, if any.
func (w *Writer) Err() error {
	if w == nil {
		return ErrWriterClosed
	}
	w.errMu.Lock()
	defer w.errMu.Unlock()
	return w.firstErr
}

// Close drains queued records and closes the owned Store.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		<-w.done
		return w.Err()
	}
	w.closed = true
	close(w.queue)
	w.mu.Unlock()
	<-w.done
	return errors.Join(w.Err(), w.store.Close())
}

func (w *Writer) run() {
	defer close(w.done)
	for {
		request, ok := <-w.queue
		if !ok {
			return
		}
		if request.flush != nil {
			close(request.flush)
			continue
		}
		if request.record == nil {
			continue
		}
		records := []Record{*request.record}
		var barrier chan struct{}
		closed := false
	collect:
		for len(records) < w.batchSize {
			select {
			case next, open := <-w.queue:
				if !open {
					closed = true
					break collect
				}
				if next.flush != nil {
					barrier = next.flush
					break collect
				}
				if next.record != nil {
					records = append(records, *next.record)
				}
			default:
				break collect
			}
		}
		w.saveBatch(records)
		if barrier != nil {
			close(barrier)
		}
		if closed {
			return
		}
	}
}

func (w *Writer) saveBatch(records []Record) {
	prepared := records
	if w.prepare != nil {
		copies := make([]Record, len(records))
		for index, record := range records {
			copies[index] = cloneRecord(record)
		}
		candidate, err := w.prepare(w.ctx, copies)
		if err != nil {
			w.rememberError(fmt.Errorf("sessionstore: prepare archive records: %w", err))
		} else if len(candidate) != len(records) {
			w.rememberError(fmt.Errorf("sessionstore: prepared %d archive records; want %d", len(candidate), len(records)))
		} else {
			prepared = candidate
		}
	}
	saved, err := w.store.SaveBatch(prepared)
	if err != nil {
		w.rememberError(err)
		// A malformed record or partial batch failure must not prevent unrelated
		// archive entries from reaching their individual durability boundary.
		// Preserve IDs and timestamps assigned before a derived-index failure so
		// the fallback updates those records instead of creating duplicates.
		retry := make([]Record, len(prepared))
		copy(retry, prepared)
		for index := 0; index < len(saved) && index < len(retry); index++ {
			retry[index] = saved[index]
		}
		for _, record := range retry {
			if _, saveErr := w.store.Save(record); saveErr != nil {
				w.rememberError(saveErr)
			}
		}
	}
}

func (w *Writer) rememberError(err error) {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	if w.firstErr == nil {
		w.firstErr = err
	}
}
