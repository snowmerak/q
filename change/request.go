// Package change defines the durable handoff between delegated Git work and
// the parent agent that reviews it.
package change

import (
	"errors"
	"strings"
	"time"
)

const Version = 1

const (
	StatusWorking = "working"
	StatusOpen    = "open"
	StatusBlocked = "blocked"
	StatusMerged  = "merged"
	StatusClosed  = "closed"
)

// Request pins both sides of an internal merge request to commits. Branch
// names remain useful for humans, while the commit IDs are authoritative for
// review and merge.
type Request struct {
	Version        int       `json:"version"`
	ID             string    `json:"id"`
	RepositoryRoot string    `json:"repository_root"`
	WorktreePath   string    `json:"worktree_path,omitempty"`
	BaseRef        string    `json:"base_ref"`
	BaseCommit     string    `json:"base_commit"`
	HeadRef        string    `json:"head_ref"`
	HeadCommit     string    `json:"head_commit,omitempty"`
	MergedCommit   string    `json:"merged_commit,omitempty"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (r Request) Final() bool {
	return r.Status == StatusMerged || r.Status == StatusClosed
}

func (r Request) Validate() error {
	if r.Version != Version || strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.RepositoryRoot) == "" ||
		strings.TrimSpace(r.BaseRef) == "" || strings.TrimSpace(r.BaseCommit) == "" || strings.TrimSpace(r.HeadRef) == "" ||
		r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return errors.New("change: invalid request identity")
	}
	switch r.Status {
	case StatusWorking, StatusBlocked:
		if strings.TrimSpace(r.WorktreePath) == "" {
			return errors.New("change: active request has no worktree")
		}
	case StatusOpen:
		if strings.TrimSpace(r.WorktreePath) == "" || strings.TrimSpace(r.HeadCommit) == "" {
			return errors.New("change: open request is incomplete")
		}
	case StatusMerged:
		if strings.TrimSpace(r.HeadCommit) == "" || strings.TrimSpace(r.MergedCommit) == "" {
			return errors.New("change: merged request is incomplete")
		}
	case StatusClosed:
	default:
		return errors.New("change: invalid request status")
	}
	return nil
}
