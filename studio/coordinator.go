package studio

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/snowmerak/q/gitwork"
	"github.com/snowmerak/q/internal/fsopen"
	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspace"
)

func (s *sessionsService) deliverManagerOperation(ctx context.Context, store workspace.Store, operationID, prompt string) error {
	if operationID == "" {
		return errors.New("manager delivery requires an operation ID")
	}
	// Receipts outlive the current conversation and latest-run pointer. A user
	// turn or clear between completion and inbox acknowledgement cannot replay
	// an already completed manager notification.
	key := sha256.Sum256([]byte(store.SessionID + "\n" + operationID))
	receipt := filepath.Join(s.main.Dir, "coordinator", "manager-deliveries", fmt.Sprintf("%x", key))
	expected := fmt.Sprintf("%x", sha256.Sum256([]byte(prompt)))
	if raw, err := fsopen.ReadFile(receipt); err == nil {
		if string(raw) != expected {
			return errors.New("manager delivery prompt changed")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	complete := func() error {
		if err := os.MkdirAll(filepath.Dir(receipt), 0o700); err != nil {
			return err
		}
		return fsreplace.WriteFile(receipt, []byte(expected), 0o600)
	}
	if _, err := s.registry.register(store.Root, store.SessionID); err != nil {
		return err
	}
	if previous, err := s.runs.latest(store.Root, store.SessionID); err == nil {
		snapshot := previous.snapshotCopy()
		if snapshot.OperationID == operationID && snapshot.Status == "completed" {
			return complete()
		}
	}
	run, err := s.runs.startOperation(store.Root, store.SessionID, prompt, operationID, ctx)
	if err != nil {
		return err
	}
	for {
		run.mu.Lock()
		snapshot, notify := run.snapshot, run.notify
		run.mu.Unlock()
		if snapshot.Status == "completed" {
			return complete()
		}
		if terminalRunStatus(snapshot.Status) {
			return errors.New("manager review turn " + snapshot.Status + ": " + snapshot.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notify:
		}
	}
}

func (s *sessionsService) serveWorkMembers(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (library.MemberList, error) {
			return c.ListMembers(ctx, r.PathValue("project"))
		})
		return
	}
	var in workitems.MemberInput
	if err := decodeWorkRequest(w, r, &in); err != nil {
		writeAPIError(w, 400, err)
		return
	}
	workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (workitems.Member, error) {
		return c.PutMember(ctx, r.PathValue("project"), in)
	})
}

func (s *sessionsService) serveTicketWorker(w http.ResponseWriter, r *http.Request) {
	workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (library.WorkerPage, error) {
		return c.ReadWorker(ctx, r.PathValue("attempt"), r.URL.Query().Get("cursor"), 20)
	})
}
func (s *sessionsService) serveWorkInbox(w http.ResponseWriter, r *http.Request) {
	workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (library.InboxList, error) {
		return c.ListInbox(ctx, r.PathValue("project"))
	})
}
func (s *sessionsService) serveTicketControl(w http.ResponseWriter, r *http.Request) {
	var in workitems.TicketAction
	if err := decodeWorkRequest(w, r, &in); err != nil {
		writeAPIError(w, 400, err)
		return
	}
	if in.Action != "queue" && in.Action != "retry" && in.Action != "cancel" {
		writeAPIError(w, 400, errors.New("action must be queue, retry, or cancel"))
		return
	}
	workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (workitems.Item, error) {
		return c.ControlTicket(ctx, r.PathValue("item"), in)
	})
}
func (s *sessionsService) serveTicketAttempts(w http.ResponseWriter, r *http.Request) {
	workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (library.AttemptList, error) {
		out, err := c.ListAttempts(ctx, r.PathValue("item"))
		for i := range out.Attempts {
			out.Attempts[i].Owner = ""
			out.Attempts[i].Token = ""
		}
		return out, err
	})
}
func (s *sessionsService) serveTicketChange(w http.ResponseWriter, r *http.Request) {
	workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (gitwork.Diff, error) {
		a, err := c.GetAttempt(ctx, r.PathValue("attempt"))
		if err != nil {
			return gitwork.Diff{}, err
		}
		if a.ChangeRequest == nil {
			return gitwork.Diff{}, errors.New("attempt has no Git change request")
		}
		return (gitwork.Manager{Root: filepath.Join(s.main.Dir, "worktrees")}).Read(ctx, *a.ChangeRequest)
	})
}
func (s *sessionsService) serveTicketReview(w http.ResponseWriter, r *http.Request) {
	var in workitems.ReviewInput
	if err := decodeWorkRequest(w, r, &in); err != nil {
		writeAPIError(w, 400, err)
		return
	}
	// Studio acts as the user reviewing through the project's reserved manager.
	// The caller cannot provide a different session identity.
	workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (workitems.Attempt, error) {
		a, err := c.GetAttempt(ctx, r.PathValue("attempt"))
		if err != nil {
			return a, err
		}
		projects, err := c.ListWorkspaces(ctx, false)
		if err != nil {
			return a, err
		}
		in.ManagerSessionID = ""
		for _, p := range projects.Workspaces {
			if p.ID == a.WorkspaceID {
				in.ManagerSessionID = p.ManagerSessionID
			}
		}
		if in.ManagerSessionID == "" {
			return workitems.Attempt{}, errors.New("workspace manager is unavailable")
		}
		out, err := c.ReviewAttempt(ctx, a.ID, in)
		out.Token = ""
		out.Owner = ""
		return out, err
	})
}
