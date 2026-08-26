package commitagent

import (
	"context"
	"errors"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

// ProgressFunc receives bounded status updates from a headless commit session.
type ProgressFunc func(ProgressEvent)

// HeadlessSession exposes the commit workflow without owning a terminal UI.
// It retains the staged-diff snapshot and verifies it again before committing.
type HeadlessSession struct {
	session        *Session
	logger         *progressLogger
	repositoryLock *workspace.Lock
}

// PrepareHeadlessWithConfig prepares a commit proposal while reusing an
// embedding q process's workspace lock when possible.
func PrepareHeadlessWithConfig(
	ctx context.Context,
	directory string,
	existingLock *workspace.Lock,
	value config.Config,
	progress ProgressFunc,
) (*HeadlessSession, error) {
	root, err := repositoryRoot(ctx, directory)
	if err != nil {
		return nil, err
	}
	var repositoryLock *workspace.Lock
	if existingLock == nil || !existingLock.Owns(root) {
		repositoryLock, err = workspace.AcquireLock(root, "q commit")
		if err != nil {
			return nil, err
		}
	}
	logger := newCallbackProgressLogger(func(event ProgressEvent) {
		if progress != nil {
			progress(event)
		}
	})
	session, err := prepareSessionWithConfig(ctx, directory, value, logger)
	if err != nil {
		if repositoryLock != nil {
			_ = repositoryLock.Close()
		}
		return nil, err
	}
	return &HeadlessSession{session: session, logger: logger, repositoryLock: repositoryLock}, nil
}

func (session *HeadlessSession) Proposals() []Proposal {
	if session == nil || session.session == nil {
		return nil
	}
	proposals := session.session.Proposals()
	if len(proposals) == 1 && len(proposals[0].Files) == 0 {
		proposals[0].Files = append([]string(nil), session.session.state.files...)
	}
	return proposals
}

func (session *HeadlessSession) AutoStaged() bool {
	return session != nil && session.session != nil && session.session.state.autoStaged
}

func (session *HeadlessSession) Regenerate(ctx context.Context) error {
	if session == nil || session.session == nil {
		return errors.New("q commit: session is unavailable")
	}
	return session.session.Regenerate(ctx, session.logger)
}

func (session *HeadlessSession) Commit(ctx context.Context) (Result, error) {
	if session == nil || session.session == nil {
		return Result{}, errors.New("q commit: session is unavailable")
	}
	return session.session.Commit(ctx, session.logger)
}

func (session *HeadlessSession) Push(ctx context.Context) error {
	if session == nil || session.session == nil {
		return errors.New("q commit: session is unavailable")
	}
	return session.session.Push(ctx, session.logger)
}

func (session *HeadlessSession) Close() error {
	if session == nil {
		return nil
	}
	var closeErrors []error
	if session.session != nil {
		closeErrors = append(closeErrors, session.session.Close())
	}
	if session.repositoryLock != nil {
		closeErrors = append(closeErrors, session.repositoryLock.Close())
	}
	return errors.Join(closeErrors...)
}
