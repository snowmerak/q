package gitwork

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/snowmerak/q/change"
)

var (
	ErrMergeConflict         = errors.New("gitwork: reviewed change conflicts with the target branch")
	ErrMergeTreeUnavailable  = errors.New("gitwork: this Git version does not support merge-tree --write-tree")
	ErrTargetRewritten       = errors.New("gitwork: target history changed outside a fast-forward")
	ErrReviewAlreadyInTarget = errors.New("gitwork: reviewed head already belongs to the target; verify the existing integration")
	ErrMergeCommitHooks      = errors.New("gitwork: merge commit hooks require the original target base; update and review the ticket against the current target")
)

// Clean verifies that a checkout can be used for a handoff or Git operation.
func Clean(ctx context.Context, root string) error {
	status, err := gitText(ctx, root, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if status != "" {
		return ErrParentDirty
	}
	for _, state := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD"} {
		if exists, err := gitPredicate(ctx, root, "rev-parse", "--verify", "--quiet", state); err != nil {
			return err
		} else if exists {
			return fmt.Errorf("gitwork: checkout has an active Git operation (%s)", state)
		}
	}
	return nil
}

func SameRepository(ctx context.Context, left, right string) (bool, error) {
	a, err := gitText(ctx, left, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return false, err
	}
	b, err := gitText(ctx, right, "rev-parse", "--path-format=absolute", "--git-common-dir")
	return err == nil && samePath(a, b), err
}

func gitPredicate(ctx context.Context, root string, args ...string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(output)))
}

func (m Manager) mergeTarget(ctx context.Context, request change.Request) (string, error) {
	if request.Status != change.StatusOpen {
		return "", errors.New("gitwork: change request is not open")
	}
	if _, err := m.validateRequest(ctx, request); err != nil {
		return "", err
	}
	branch, err := gitText(ctx, request.RepositoryRoot, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch != request.BaseRef {
		return "", ErrBaseMoved
	}
	return gitText(ctx, request.RepositoryRoot, "rev-parse", "HEAD")
}

// PrepareCandidate computes a merge without changing the target checkout.
func (m Manager) PrepareCandidate(ctx context.Context, request change.Request) (change.MergeCandidate, error) {
	var candidate change.MergeCandidate
	target, err := m.mergeTarget(ctx, request)
	if err != nil {
		return candidate, err
	}
	candidate.Target = target
	root := request.RepositoryRoot
	if ok, err := gitPredicate(ctx, root, "merge-base", "--is-ancestor", request.BaseCommit, target); err != nil {
		return candidate, err
	} else if !ok {
		return candidate, ErrBaseMoved
	}
	if err := m.Resume(ctx, request); err != nil {
		return candidate, err
	}
	if err := Clean(ctx, request.WorktreePath); err != nil {
		return candidate, fmt.Errorf("gitwork: reviewed checkout: %w", err)
	}
	if err := Clean(ctx, root); err != nil {
		return candidate, err
	}
	for _, hook := range []string{"pre-merge-commit", "prepare-commit-msg", "commit-msg"} {
		path, err := gitText(ctx, root, "rev-parse", "--path-format=absolute", "--git-path", "hooks/"+hook)
		if err != nil {
			return candidate, err
		}
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0) {
			return candidate, ErrMergeCommitHooks
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return candidate, err
		}
	}
	if ok, err := gitPredicate(ctx, root, "merge-base", "--is-ancestor", request.HeadCommit, target); err != nil {
		return candidate, err
	} else if ok {
		return candidate, ErrReviewAlreadyInTarget
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "merge-tree", "--write-tree", target, request.HeadCommit)
	var output, diagnostics cappedBuffer
	output.limit, diagnostics.limit = maximumGitOutput, 32<<10
	cmd.Stdout, cmd.Stderr = &output, &diagnostics
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if ctx.Err() == nil && errors.As(err, &exit) {
			if exit.ExitCode() == 1 {
				return candidate, fmt.Errorf("%w: %s", ErrMergeConflict, strings.TrimSpace(output.String()))
			}
			if exit.ExitCode() == 129 && strings.Contains(output.String()+diagnostics.String(), "write-tree") {
				return candidate, ErrMergeTreeUnavailable
			}
		}
		return candidate, fmt.Errorf("gitwork: compute merge: %w: %s", err, strings.TrimSpace(diagnostics.String()))
	}
	tree, _, _ := strings.Cut(strings.TrimSpace(output.String()), "\n")
	tree = strings.TrimSpace(tree)
	if !objectID(tree) {
		return candidate, errors.New("gitwork: merge-tree returned an invalid tree ID")
	}
	signing, err := gitText(ctx, root, "config", "--bool", "--default", "false", "--get", "commit.gpgsign")
	if err != nil {
		return candidate, err
	}
	args := []string{"-c", "user.name=Q", "-c", "user.email=q@localhost", "commit-tree", tree, "-p", target, "-p", request.HeadCommit, "-m", "Merge delegated change " + request.ID}
	if signing == "true" {
		args = append(args, "-S")
	}
	commit, err := gitText(ctx, root, args...)
	if err != nil {
		return candidate, err
	}
	if _, err := git(ctx, root, "update-ref", candidateRef(request), commit); err != nil {
		return candidate, err
	}
	return change.MergeCandidate{Target: target, Commit: commit}, nil
}

func candidateRef(request change.Request) string {
	return "refs/q/ticket-merges/" + normalizedID(request.ID)
}

// ApplyCandidate recovers an already applied candidate or fast-forwards the
// checked-out target if it still matches the input to the merge computation.
func (m Manager) ApplyCandidate(ctx context.Context, request change.Request, candidate change.MergeCandidate) (change.Request, error) {
	if !objectID(candidate.Commit) || !objectID(candidate.Target) {
		return request, errors.New("gitwork: invalid merge candidate commit ID")
	}
	target, err := m.mergeTarget(ctx, request)
	if err != nil {
		return request, err
	}
	root := request.RepositoryRoot
	parents, err := gitText(ctx, root, "show", "-s", "--format=%P", candidate.Commit, "--")
	if err != nil || parents != candidate.Target+" "+request.HeadCommit {
		return request, errors.New("gitwork: recorded merge candidate has unexpected parents")
	}
	applied, err := gitPredicate(ctx, root, "merge-base", "--is-ancestor", candidate.Commit, target)
	if err != nil {
		return request, err
	}
	if !applied {
		if target != candidate.Target {
			if forward, err := gitPredicate(ctx, root, "merge-base", "--is-ancestor", candidate.Target, target); err != nil {
				return request, err
			} else if !forward {
				return request, ErrTargetRewritten
			}
			return request, ErrBaseMoved
		}
		if err := m.Resume(ctx, request); err != nil {
			return request, err
		}
		if err := Clean(ctx, root); err != nil {
			return request, err
		}
		if _, err := git(ctx, root, "merge", "--ff-only", candidate.Commit); err != nil {
			if current, readErr := m.mergeTarget(ctx, request); readErr == nil && current != candidate.Target {
				return request, ErrBaseMoved
			}
			return request, err
		}
		target, err = m.mergeTarget(ctx, request)
		if err != nil {
			return request, err
		}
		if landed, err := gitPredicate(ctx, root, "merge-base", "--is-ancestor", candidate.Commit, target); err != nil {
			return request, err
		} else if !landed {
			return request, ErrTargetRewritten
		}
	}
	request.Status, request.MergedCommit = change.StatusMerged, candidate.Commit
	if _, err := os.Stat(request.WorktreePath); err == nil {
		if err := m.Resume(ctx, request); err != nil {
			return request, err
		}
		if err := Clean(ctx, request.WorktreePath); err != nil {
			return request, fmt.Errorf("gitwork: reviewed checkout: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return request, err
	}
	if err := m.cleanup(ctx, request); err != nil {
		return request, err
	}
	request.WorktreePath = ""
	request.UpdatedAt = time.Now().UTC()
	return request, nil
}

// RepairReviewBase pins a fresh diff only after the worker has incorporated the
// target that conflicted with the prior, already reviewed change.
func RepairReviewBase(ctx context.Context, request change.Request, base string) (change.Request, error) {
	head, err := gitText(ctx, request.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return request, err
	}
	if ok, err := gitPredicate(ctx, request.WorktreePath, "merge-base", "--is-ancestor", base, head); err != nil {
		return request, err
	} else if !ok {
		return request, fmt.Errorf("gitwork: integrate conflicting target %s before submitting for review", base)
	}
	request.BaseCommit = base
	return request, nil
}

func (m Manager) DeleteCandidate(ctx context.Context, request change.Request, candidate change.MergeCandidate) error {
	_, err := git(ctx, request.RepositoryRoot, "update-ref", "-d", candidateRef(request), candidate.Commit)
	return err
}

func CheckoutHead(ctx context.Context, root string) (string, error) {
	return gitText(ctx, root, "rev-parse", "HEAD")
}

func objectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
