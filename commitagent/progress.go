package commitagent

import (
	"fmt"
	"io"
	"sync"
)

type progressLogger struct {
	output io.Writer
	events chan<- ProgressEvent
	notify func(ProgressEvent)
	mu     sync.Mutex
}

type ProgressEvent struct {
	Stage   string
	Message string
}

func newProgressLogger(output io.Writer) *progressLogger {
	return &progressLogger{output: output}
}

func newEventProgressLogger(events chan<- ProgressEvent) *progressLogger {
	return &progressLogger{events: events}
}

func newCallbackProgressLogger(notify func(ProgressEvent)) *progressLogger {
	return &progressLogger{notify: notify}
}

func (logger *progressLogger) step(stage, format string, arguments ...any) {
	if logger == nil {
		return
	}
	event := ProgressEvent{Stage: stage, Message: fmt.Sprintf(format, arguments...)}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	if logger.output != nil {
		fmt.Fprintf(logger.output, "q commit · %s · %s\n", event.Stage, event.Message)
	}
	if logger.events != nil {
		select {
		case logger.events <- event:
		default:
		}
	}
	if logger.notify != nil {
		logger.notify(event)
	}
}
