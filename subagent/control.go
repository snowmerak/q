package subagent

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/snowmerak/q/client"
)

var ErrRunStopped = errors.New("subagent stopped by user")

// RunControl belongs to one inner invocation. Guidance is acknowledged after
// the runner checkpoints it; cancellation affects this invocation and its children.
type RunControl struct {
	mu       sync.Mutex
	paused   bool
	finished bool
	stopped  bool
	cancel   context.CancelCauseFunc
	wake     chan struct{}
	done     chan struct{}
	guidance []*runGuidance
}

type runGuidance struct {
	ctx     context.Context
	content string
	ack     chan error
}

func NewRunControl(cancel context.CancelCauseFunc) *RunControl {
	return &RunControl{cancel: cancel, wake: make(chan struct{}), done: make(chan struct{})}
}

func (c *RunControl) Status() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		if !c.finished {
			return "cancelling"
		}
		return "cancelled"
	}
	if c.finished {
		return "completed"
	}
	if c.paused {
		return "paused"
	}
	return "running"
}

func (c *RunControl) Pause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || c.stopped {
		return errors.New("subagent is no longer running")
	}
	c.paused = true
	return nil
}

func (c *RunControl) Resume() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || c.stopped {
		return errors.New("subagent is no longer running")
	}
	c.paused = false
	c.notify()
	return nil
}

func (c *RunControl) Cancel() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || c.stopped {
		return errors.New("subagent is no longer running")
	}
	c.stopped = true
	c.cancel(ErrRunStopped)
	c.notify()
	return nil
}

func (c *RunControl) Guide(ctx context.Context, content string) error {
	content = strings.TrimSpace(content)
	if content == "" || len(content) > MaximumDelegatePromptBytes {
		return errors.New("guidance must be nonempty and within the prompt size limit")
	}
	input := &runGuidance{ctx: ctx, content: content, ack: make(chan error, 1)}
	c.mu.Lock()
	if c.finished || c.stopped {
		c.mu.Unlock()
		return errors.New("subagent is no longer running")
	}
	c.guidance = append(c.guidance, input)
	// Sending guidance resumes a paused conversation, like the root composer.
	c.paused = false
	c.notify()
	c.mu.Unlock()
	select {
	case err := <-input.ack:
		return err
	case <-c.done:
		select {
		case err := <-input.ack:
			return err
		default:
			return errors.New("subagent finished before accepting guidance")
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *RunControl) Finish() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.finished {
		c.finished = true
		close(c.done)
		c.notify()
	}
}

func (c *RunControl) notify() {
	close(c.wake)
	c.wake = make(chan struct{})
}

func (c *RunControl) wait(ctx context.Context) error {
	if c == nil {
		return ctx.Err()
	}
	for {
		c.mu.Lock()
		paused, wake := c.paused, c.wake
		c.mu.Unlock()
		if !paused {
			return ctx.Err()
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *RunControl) takeGuidance() []*runGuidance {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	inputs := c.guidance
	c.guidance = nil
	return inputs
}

func (c *RunControl) hasGuidance() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, input := range c.guidance {
		if input.ctx.Err() == nil {
			return true
		}
	}
	return false
}

func (r *GeneralRunner) applyGuidance(state *GeneralRunState, history *ContextCompactor, lifecycle *Lifecycle, checkpoint func() error) error {
	for _, input := range r.Control.takeGuidance() {
		if err := input.ctx.Err(); err != nil {
			input.ack <- err
			continue
		}
		message := client.Message{Role: client.RoleUser, Content: input.content}
		history.Append(message)
		state.Transcript = append(state.Transcript, message)
		if err := checkpoint(); err != nil {
			input.ack <- err
			return err
		}
		input.ack <- nil
		if lifecycle != nil {
			if err := lifecycle.Message(message); err != nil {
				return err
			}
		}
	}
	return nil
}
