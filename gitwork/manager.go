// Package gitwork owns local branches, linked worktrees, and the immutable
// commit identities used by Q's internal change requests.
package gitwork

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/snowmerak/q/change"
)

const maximumGitOutput = 256 << 10

var safeID = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

var (
	ErrParentDirty = errors.New("gitwork: parent worktree has uncommitted changes")
	ErrBaseMoved   = errors.New("gitwork: parent branch moved after delegation")
)

type Manager struct {
	Root string
}

type Diff struct {
	ChangeRequest change.Request `json:"change_request"`
	Patch         string         `json:"patch"`
	Truncated     bool           `json:"truncated,omitempty"`
}

// Prepare creates or adopts the deterministic branch and linked worktree for
// one delegated invocation. The parent must be clean so the pinned base commit
// fully represents the work handed to the child.
func (m Manager) Prepare(ctx context.Context, parentCheckout, id string) (change.Request, error) {
	if strings.TrimSpace(m.Root) == "" {
		return change.Request{}, errors.New("gitwork: manager root is required")
	}
	parentCheckout, err := filepath.Abs(parentCheckout)
	if err != nil {
		return change.Request{}, fmt.Errorf("gitwork: resolve parent checkout: %w", err)
	}
	repositoryRoot, err := gitText(ctx, parentCheckout, "rev-parse", "--show-toplevel")
	if err != nil {
		return change.Request{}, fmt.Errorf("gitwork: open parent repository: %w", err)
	}
	repositoryRoot = filepath.Clean(repositoryRoot)
	status, err := gitText(ctx, parentCheckout, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return change.Request{}, err
	}
	if strings.TrimSpace(status) != "" {
		return change.Request{}, ErrParentDirty
	}
	baseRef, err := gitText(ctx, parentCheckout, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return change.Request{}, errors.New("gitwork: parent must have a checked-out branch")
	}
	baseCommit, err := gitText(ctx, parentCheckout, "rev-parse", "HEAD")
	if err != nil {
		return change.Request{}, err
	}
	commonDir, err := gitText(ctx, parentCheckout, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return change.Request{}, err
	}
	commonDir = filepath.Clean(commonDir)
	branch := "q/delegate/" + normalizedID(id)
	worktree := filepath.Join(m.Root, repositoryKey(commonDir), normalizedID(id))

	request := change.Request{
		Version: change.Version, ID: id, RepositoryRoot: repositoryRoot,
		WorktreePath: worktree, BaseRef: baseRef, BaseCommit: baseCommit,
		HeadRef: branch, Status: change.StatusWorking,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if adopted, adoptErr := m.adopt(ctx, request); adopted || adoptErr != nil {
		return request, adoptErr
	}
	if err := os.MkdirAll(filepath.Dir(worktree), 0o700); err != nil {
		return change.Request{}, fmt.Errorf("gitwork: create lease directory: %w", err)
	}
	arguments := []string{"worktree", "add", "-b", branch, worktree, baseCommit}
	if branchCommit, branchErr := gitText(ctx, parentCheckout, "rev-parse", "--verify", "refs/heads/"+branch); branchErr == nil {
		if branchCommit != baseCommit {
			return request, errors.New("gitwork: existing delegated branch does not match the base commit")
		}
		// A process may stop after creating the branch but before the lease is
		// durably recorded. Drop stale administrative entries and reuse that
		// verified branch instead of creating a second identity.
		if _, err := git(ctx, parentCheckout, "worktree", "prune"); err != nil {
			return request, fmt.Errorf("gitwork: prune stale worktrees: %w", err)
		}
		arguments = []string{"worktree", "add", worktree, branch}
	}
	if _, err := git(ctx, parentCheckout, arguments...); err != nil {
		return change.Request{}, fmt.Errorf("gitwork: create worktree: %w", err)
	}
	return request, nil
}

// Resume verifies that a recorded lease still names the branch and commit that
// Q created. It never recreates a missing worktree with unverified state.
func (m Manager) Resume(ctx context.Context, request change.Request) error {
	if _, err := m.validateRequest(ctx, request); err != nil {
		return err
	}
	branch, err := gitText(ctx, request.WorktreePath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch != request.HeadRef {
		return errors.New("gitwork: delegated worktree branch is unavailable")
	}
	head, err := gitText(ctx, request.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if request.HeadCommit != "" && head != request.HeadCommit {
		return errors.New("gitwork: delegated branch moved outside the recorded change request")
	}
	if request.HeadCommit == "" {
		if _, err := git(ctx, request.WorktreePath, "merge-base", "--is-ancestor", request.BaseCommit, head); err != nil {
			return errors.New("gitwork: delegated branch no longer descends from the recorded base")
		}
	}
	return nil
}

// Submit commits any remaining child changes and opens the change request at a
// fixed head commit. A child that already committed is accepted as-is.
func (m Manager) Submit(ctx context.Context, request change.Request, summary string) (change.Request, error) {
	if request.Status == change.StatusOpen {
		return request, m.Resume(ctx, request)
	}
	if err := m.Resume(ctx, request); err != nil {
		return request, err
	}
	status, err := gitText(ctx, request.WorktreePath, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return request, err
	}
	if strings.TrimSpace(status) != "" {
		if _, err := git(ctx, request.WorktreePath, "add", "-A"); err != nil {
			return request, fmt.Errorf("gitwork: stage delegated changes: %w", err)
		}
		message := commitSubject(summary)
		if _, err := git(ctx, request.WorktreePath,
			"-c", "user.name=Q", "-c", "user.email=q@localhost",
			"commit", "-m", message); err != nil {
			return request, fmt.Errorf("gitwork: commit delegated changes: %w", err)
		}
	}
	head, err := gitText(ctx, request.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return request, err
	}
	request.HeadCommit = head
	request.UpdatedAt = time.Now().UTC()
	if head == request.BaseCommit {
		request.Status = change.StatusClosed
		if err := m.cleanup(ctx, request); err != nil {
			return request, err
		}
		request.WorktreePath = ""
		return request, nil
	}
	clean, err := gitText(ctx, request.WorktreePath, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return request, err
	}
	if strings.TrimSpace(clean) != "" {
		return request, errors.New("gitwork: delegated worktree is not clean after commit")
	}
	request.Status = change.StatusOpen
	return request, nil
}

func (m Manager) MarkBlocked(request change.Request) change.Request {
	request.Status = change.StatusBlocked
	request.UpdatedAt = time.Now().UTC()
	return request
}

func (m Manager) Read(ctx context.Context, request change.Request) (Diff, error) {
	if err := request.Validate(); err != nil {
		return Diff{}, err
	}
	if request.HeadCommit == "" {
		return Diff{}, errors.New("gitwork: change request has no submitted head commit")
	}
	root := request.WorktreePath
	if root == "" {
		root = request.RepositoryRoot
	} else if _, err := m.validateRequest(ctx, request); err != nil {
		return Diff{}, err
	}
	body, truncated, err := gitBounded(ctx, root, maximumGitOutput,
		"diff", "--no-ext-diff", "--no-color", "--find-renames", "--binary", request.BaseCommit, request.HeadCommit, "--")
	if err != nil {
		return Diff{}, err
	}
	return Diff{ChangeRequest: request, Patch: string(body), Truncated: truncated}, nil
}

// Merge verifies the pinned base and head again, creates a merge commit in the
// parent checkout, then removes the linked worktree and task branch.
func (m Manager) Merge(ctx context.Context, parentCheckout string, request change.Request) (change.Request, error) {
	if request.Status != change.StatusOpen {
		return request, errors.New("gitwork: change request is not open")
	}
	requestCommonDir, err := m.validateRequest(ctx, request)
	if err != nil {
		return request, err
	}
	parentCommonDir, err := gitText(ctx, parentCheckout, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || !samePath(requestCommonDir, parentCommonDir) {
		return request, errors.New("gitwork: parent checkout belongs to a different repository")
	}
	baseRef, err := gitText(ctx, parentCheckout, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || baseRef != request.BaseRef {
		return request, ErrBaseMoved
	}
	baseCommit, err := gitText(ctx, parentCheckout, "rev-parse", "HEAD")
	if err != nil {
		return request, ErrBaseMoved
	}
	if baseCommit != request.BaseCommit {
		// The process may have stopped after Git created the merge commit but
		// before the updated request reached durable session state. Adopt only
		// that exact two-parent commit; any other branch movement remains an
		// explicit conflict for the caller.
		first, firstErr := gitText(ctx, parentCheckout, "rev-parse", baseCommit+"^1")
		second, secondErr := gitText(ctx, parentCheckout, "rev-parse", baseCommit+"^2")
		if firstErr != nil || secondErr != nil || first != request.BaseCommit || second != request.HeadCommit {
			return request, ErrBaseMoved
		}
		request.Status = change.StatusMerged
		request.MergedCommit = baseCommit
		request.UpdatedAt = time.Now().UTC()
		if err := m.cleanup(ctx, request); err != nil {
			return request, err
		}
		request.WorktreePath = ""
		return request, nil
	}
	if err := m.Resume(ctx, request); err != nil {
		return request, err
	}
	status, err := gitText(ctx, parentCheckout, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return request, err
	}
	if strings.TrimSpace(status) != "" {
		return request, ErrParentDirty
	}
	if _, err := git(ctx, parentCheckout,
		"-c", "user.name=Q", "-c", "user.email=q@localhost",
		"merge", "--no-ff", "-m", "Merge delegated change "+request.ID, request.HeadCommit); err != nil {
		return request, fmt.Errorf("gitwork: merge change request: %w", err)
	}
	merged, err := gitText(ctx, parentCheckout, "rev-parse", "HEAD")
	if err != nil {
		return request, err
	}
	request.Status = change.StatusMerged
	request.MergedCommit = merged
	request.UpdatedAt = time.Now().UTC()
	if err := m.cleanup(ctx, request); err != nil {
		return request, err
	}
	request.WorktreePath = ""
	return request, nil
}

func (m Manager) Close(ctx context.Context, request change.Request) (change.Request, error) {
	if request.Status == change.StatusMerged || request.Status == change.StatusClosed {
		return request, nil
	}
	if err := m.cleanup(ctx, request); err != nil {
		return request, err
	}
	request.Status = change.StatusClosed
	request.WorktreePath = ""
	request.UpdatedAt = time.Now().UTC()
	return request, nil
}

func (m Manager) adopt(ctx context.Context, request change.Request) (bool, error) {
	if err := m.validatePath(request.WorktreePath); err != nil {
		return false, err
	}
	if _, err := os.Stat(request.WorktreePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	branch, err := gitText(ctx, request.WorktreePath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch != request.HeadRef {
		return true, errors.New("gitwork: existing lease path does not match the delegated branch")
	}
	head, err := gitText(ctx, request.WorktreePath, "rev-parse", "HEAD")
	if err != nil || head != request.BaseCommit {
		return true, errors.New("gitwork: existing delegated branch does not match the base commit")
	}
	return true, nil
}

func (m Manager) cleanup(ctx context.Context, request change.Request) error {
	if request.WorktreePath != "" {
		if _, err := m.validateRequest(ctx, request); err != nil {
			return err
		}
		if _, statErr := os.Stat(request.WorktreePath); statErr == nil {
			if _, err := git(ctx, request.RepositoryRoot, "worktree", "remove", "--force", request.WorktreePath); err != nil {
				return fmt.Errorf("gitwork: remove worktree: %w", err)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("gitwork: inspect worktree: %w", statErr)
		} else if _, err := git(ctx, request.RepositoryRoot, "worktree", "prune"); err != nil {
			return fmt.Errorf("gitwork: prune missing worktree: %w", err)
		}
	}
	if request.HeadRef != "" {
		if _, err := git(ctx, request.RepositoryRoot, "branch", "-D", request.HeadRef); err != nil {
			// A branch already removed after a crash is a completed cleanup step.
			if _, verifyErr := git(ctx, request.RepositoryRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+request.HeadRef); verifyErr == nil {
				return fmt.Errorf("gitwork: remove delegated branch: %w", err)
			}
		}
	}
	return nil
}

func (m Manager) validateRequest(ctx context.Context, request change.Request) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	if request.HeadRef != "q/delegate/"+normalizedID(request.ID) {
		return "", errors.New("gitwork: delegated branch does not match the request identity")
	}
	if err := m.validatePath(request.WorktreePath); err != nil {
		return "", err
	}
	repositoryRoot, err := gitText(ctx, request.RepositoryRoot, "rev-parse", "--show-toplevel")
	if err != nil || !samePath(repositoryRoot, request.RepositoryRoot) {
		return "", errors.New("gitwork: recorded repository root is unavailable")
	}
	commonDir, err := gitText(ctx, request.RepositoryRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", errors.New("gitwork: recorded repository metadata is unavailable")
	}
	expected := filepath.Join(m.Root, repositoryKey(commonDir), normalizedID(request.ID))
	if !samePath(expected, request.WorktreePath) {
		return "", errors.New("gitwork: worktree lease does not match the recorded repository")
	}
	return filepath.Clean(commonDir), nil
}

func samePath(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(left), filepath.Clean(right))
	return err == nil && relative == "."
}

func (m Manager) validatePath(path string) error {
	root, err := filepath.Abs(m.Root)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("gitwork: worktree path escapes the lease root")
	}
	return nil
}

func normalizedID(id string) string {
	value := strings.Trim(safeID.ReplaceAllString(id, "-"), "-")
	if value == "" {
		sum := sha256.Sum256([]byte(id))
		value = hex.EncodeToString(sum[:8])
	}
	if len(value) > 64 {
		value = value[:64]
	}
	return value
}

func repositoryKey(commonDir string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(commonDir))))
	return hex.EncodeToString(sum[:12])
}

func commitSubject(summary string) string {
	line := strings.TrimSpace(strings.SplitN(summary, "\n", 2)[0])
	if line == "" {
		line = "Apply delegated change"
	}
	if len(line) > 72 {
		line = strings.TrimSpace(line[:72])
	}
	return line
}

func gitText(ctx context.Context, root string, arguments ...string) (string, error) {
	body, _, err := gitBounded(ctx, root, maximumGitOutput, arguments...)
	return strings.TrimSpace(string(body)), err
}

func git(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	body, _, err := gitBounded(ctx, root, maximumGitOutput, arguments...)
	return body, err
}

func gitBounded(ctx context.Context, root string, limit int, arguments ...string) ([]byte, bool, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root, "--no-pager"}, arguments...)...)
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = limit, 32<<10
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, false, errors.New(message)
	}
	return stdout.Bytes(), stdout.truncated, nil
}

type cappedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(body []byte) (int, error) {
	n := len(body)
	remaining := b.limit - b.Len()
	if remaining < len(body) {
		b.truncated = true
		if remaining > 0 {
			_, _ = b.Buffer.Write(body[:remaining])
		}
		return n, nil
	}
	_, _ = b.Buffer.Write(body)
	return n, nil
}
