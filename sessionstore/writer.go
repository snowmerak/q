package sessionstore

import (
	"errors"
	"sync"
)

const defaultWriterBuffer = 256

var (
	ErrWriterClosed    = errors.New("sessionstore: writer is closed")
	ErrWriterQueueFull = errors.New("sessionstore: writer queue is full")
)

type writeRequest struct {
	record *Record
	flush  chan struct{}
}

// Writer serializes archive writes on a background goroutine. Append never
// waits for disk I/O; Flush and Close provide explicit durability barriers.
type Writer struct {
	store *Store
	queue chan writeRequest
	done  chan struct{}

	mu       sync.Mutex
	closed   bool
	errMu    sync.Mutex
	firstErr error
}

func NewWriter(store *Store, buffer int) *Writer {
	if buffer <= 0 {
		buffer = defaultWriterBuffer
	}
	w := &Writer{
		store: store,
		queue: make(chan writeRequest, buffer),
		done:  make(chan struct{}),
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
	for request := range w.queue {
		if request.record != nil {
			if _, err := w.store.Save(*request.record); err != nil {
				w.rememberError(err)
			}
		}
		if request.flush != nil {
			close(request.flush)
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
