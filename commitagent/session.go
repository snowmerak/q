package commitagent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/snowmerak/q/loom"
)

var commitHeaderPattern = regexp.MustCompile(`^([a-z]+)(?:\(([a-z0-9][a-z0-9._/-]*)\))?: (.+)$`)

type Session struct {
	state       repositoryState
	runtime     resolvedRuntime
	runtimeErr  error
	maxParallel int
	loomOptions loom.StoreOptions
	proposal    proposalState
	source      string
	logger      *progressLogger
	cancel      context.CancelFunc

	mu        sync.Mutex
	committed bool
	closed    bool
}

func (session *Session) Root() string { return session.state.root }

func (session *Session) Proposals() []Proposal {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.proposal.Single != nil {
		return []Proposal{cloneProposal(*session.proposal.Single)}
	}
	result := make([]Proposal, len(session.proposal.Split))
	for index, proposal := range session.proposal.Split {
		result[index] = cloneProposal(proposal)
	}
	return result
}

func (session *Session) Regenerate(ctx context.Context, logger *progressLogger) error {
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return errors.New("q commit: session is closed")
	}
	runtimeErr := session.runtimeErr
	runtime := session.runtime
	state := session.state
	maxParallel := session.maxParallel
	loomOptions := session.loomOptions
	session.mu.Unlock()
	if runtimeErr != nil {
		return runtimeErr
	}
	if runtime.client == nil {
		return errors.New("q commit: commit model is unavailable")
	}
	logger.step("agent", "starting an isolated commit session")
	proposal, fallback, err := runCommitAgent(ctx, runtime.client, runtime.spec, state, maxParallel, loomOptions, logger)
	if err != nil {
		return err
	}
	source := "commit agent"
	if fallback {
		source = "mechanical fallback"
		logger.step("fallback", "using a mechanical commit proposal")
	}
	session.mu.Lock()
	session.proposal = proposal
	session.source = source
	session.logger = logger
	session.mu.Unlock()
	return nil
}

func (session *Session) UpdateProposal(index int, message string) error {
	parsed, err := parseCommitMessage(message)
	if err != nil {
		return err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.committed {
		return errors.New("q commit: commit was already created")
	}
	if session.proposal.Single != nil {
		if index != 0 {
			return fmt.Errorf("q commit: proposal index %d is out of range", index)
		}
		parsed.Files = append([]string(nil), session.proposal.Single.Files...)
		session.proposal.Single = &parsed
	} else {
		if index < 0 || index >= len(session.proposal.Split) {
			return fmt.Errorf("q commit: proposal index %d is out of range", index)
		}
		parsed.Files = append([]string(nil), session.proposal.Split[index].Files...)
		session.proposal.Split[index] = parsed
	}
	session.source = "edited proposal"
	return nil
}

func (session *Session) Commit(ctx context.Context, logger *progressLogger) (Result, error) {
	session.mu.Lock()
	if session.committed {
		session.mu.Unlock()
		return Result{}, errors.New("q commit: commit was already created")
	}
	proposal := cloneProposalState(session.proposal)
	source := session.source
	state := session.state
	session.mu.Unlock()

	currentDiff, err := gitOutput(ctx, state.root, "diff", "--cached", "--binary", "--full-index", "--no-ext-diff")
	if err != nil {
		return Result{}, fmt.Errorf("q commit: verify staged changes: %w", err)
	}
	if currentDiff != state.fullDiff {
		return Result{}, errors.New("q commit: staged changes changed during review; restart q commit")
	}
	result, err := executeProposal(ctx, state, proposal, source, logger)
	if err != nil {
		return Result{}, err
	}
	session.mu.Lock()
	session.committed = true
	session.mu.Unlock()
	return result, nil
}

func (session *Session) Push(ctx context.Context, logger *progressLogger) error {
	session.mu.Lock()
	committed := session.committed
	closed := session.closed
	root := session.state.root
	session.mu.Unlock()
	if closed {
		return errors.New("q commit: session is closed")
	}
	if !committed {
		return errors.New("q commit: create the commit before pushing")
	}
	branch, err := gitOutput(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) == "" {
		return errors.New("q commit: cannot push from a detached HEAD")
	}
	upstream, err := gitOutput(ctx, root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil || strings.TrimSpace(upstream) == "" {
		return fmt.Errorf("q commit: branch %s has no upstream; configure one before pushing", strings.TrimSpace(branch))
	}
	logger.step("push", "pushing %s to %s", strings.TrimSpace(branch), strings.TrimSpace(upstream))
	if _, err := gitOutput(ctx, root, "push"); err != nil {
		return fmt.Errorf("q commit: push: %w", err)
	}
	logger.step("push", "push completed")
	return nil
}

func (session *Session) Close() error {
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil
	}
	session.closed = true
	cancel := session.cancel
	closeRuntime := session.runtime.close
	session.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if closeRuntime != nil {
		return closeRuntime()
	}
	return nil
}

func parseCommitMessage(message string) (Proposal, error) {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	lines := strings.Split(strings.TrimSpace(message), "\n")
	if len(lines) == 0 {
		return Proposal{}, errors.New("commit message is empty")
	}
	matches := commitHeaderPattern.FindStringSubmatch(strings.TrimSpace(lines[0]))
	if matches == nil {
		return Proposal{}, errors.New("subject must use type(scope): summary or type: summary")
	}
	proposal := Proposal{Type: matches[1], Scope: matches[2], Summary: matches[3]}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		proposal.Body = append(proposal.Body, strings.TrimSpace(strings.TrimPrefix(line, "-")))
	}
	if err := validateProposal(&proposal, false); err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

func cloneProposal(value Proposal) Proposal {
	value.Body = append([]string(nil), value.Body...)
	value.Files = append([]string(nil), value.Files...)
	return value
}

func cloneProposalState(value proposalState) proposalState {
	var result proposalState
	if value.Single != nil {
		proposal := cloneProposal(*value.Single)
		result.Single = &proposal
	}
	result.Split = make([]Proposal, len(value.Split))
	for index, proposal := range value.Split {
		result.Split[index] = cloneProposal(proposal)
	}
	return result
}
