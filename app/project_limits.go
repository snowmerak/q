package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
)

// Each parent gets its own budget so a delegate waiting on its children never
// holds the only slot those children need. Descendants share the dispatcher.
func (d *delegationDispatcher) acquireDelegation(ctx context.Context, key string) (func(), error) {
	s := d.shared()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		limit := workitems.EffectiveParallel(0, d.value.EffectiveAgents().MaxParallel)
		if s.parallel != nil {
			var err error
			limit, err = s.parallel(ctx)
			if err != nil {
				return nil, err
			}
		}
		s.mu.Lock()
		if s.active == nil {
			s.active = map[string]int{}
			s.notify = make(chan struct{})
		}
		if s.active[key] < max(1, limit) {
			s.active[key]++
			s.mu.Unlock()
			return func() {
				s.mu.Lock()
				s.active[key]--
				if s.active[key] == 0 {
					delete(s.active, key)
				}
				close(s.notify)
				s.notify = make(chan struct{})
				s.mu.Unlock()
			}, nil
		}
		wake := s.notify
		s.mu.Unlock()
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (m model) projectDelegationLimit(execution executionWorkspace) func(context.Context) (int, error) {
	return func(ctx context.Context) (limit int, resultErr error) {
		value := m.activeConfig()
		if strings.TrimSpace(m.store.Dir) != "" {
			loaded, err := m.store.Load()
			if err != nil && !errors.Is(err, config.ErrNotFound) {
				return 0, err
			}
			if err == nil {
				value = loaded
			}
		}
		limit = workitems.EffectiveParallel(0, value.EffectiveAgents().MaxParallel)
		root := execution.workspaceStateRoot
		projectID := ""
		if m.studioWorkspaceContext != nil {
			projectID = m.studioWorkspaceContext.ProjectID
		}
		if projectID == "" && (root == "" || m.libraryClient == nil) {
			return limit, nil
		}
		c := m.libraryClient
		if c == nil {
			if strings.TrimSpace(m.store.Dir) == "" {
				return limit, nil
			}
			runtime, err := library.Ensure(ctx, m.store.Dir)
			if err != nil {
				return 0, err
			}
			defer func() { resultErr = errors.Join(resultErr, runtime.Close()) }()
			c = runtime.Client()
		}
		projects, err := c.ListWorkspaces(ctx, false)
		if err != nil {
			return 0, err
		}
		for _, project := range projects.Workspaces {
			matched := project.ID == projectID
			if projectID == "" {
				for _, candidate := range project.WorkspaceRoots {
					matched = matched || sameDelegationDirectory(candidate, root)
				}
			}
			if matched {
				return workitems.EffectiveParallel(project.Limits.MaxParallelSubagents, limit), nil
			}
		}
		return limit, nil
	}
}
