package commitagent

import (
	"fmt"
	"sync"
)

type progressLogger struct {
	events chan<- ProgressEvent
	notify func(ProgressEvent)
	mu     sync.Mutex
}

type ProgressEvent struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
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
