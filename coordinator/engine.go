package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/snowmerak/q/change"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gitwork"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/systemoneconfig"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/worklock"
	"github.com/snowmerak/q/workspace"
)

type EngineOptions struct {
	Directory  string
	Candidates func(context.Context, workitems.Workspace, workitems.Item, []workitems.Member) ([]Candidate, error)
	Run        func(context.Context, *library.Client, workitems.Workspace, workitems.Item, Candidate, workitems.Attempt, string) (subagent.TaskResult, error)
	Deliver    func(context.Context, workitems.Workspace, workitems.InboxEntry, workitems.Attempt) error
	Embed      func(context.Context) error
	Parallel   func() int
	Log        func(error)
}

// Engine schedules existing runners; the Library owns all durable transitions.
// An OS lock is held for the complete ticket execution, in addition to fencing
// every checkpoint/result with the current database reservation.
type Engine struct {
	options EngineOptions
	ctx     context.Context
	cancel  context.CancelFunc
	owner   string
	done    chan struct{}
	jobs    sync.WaitGroup
	mu      sync.Mutex
	active  map[string]bool
}

func Start(parent context.Context, options EngineOptions) *Engine {
	ctx, cancel := context.WithCancel(parent)
	e := &Engine{options: options, ctx: ctx, cancel: cancel, owner: uuid.NewString(), done: make(chan struct{}), active: map[string]bool{}}
	go e.loop()
	return e
}
func (e *Engine) Close() {
	if e == nil {
		return
	}
	e.cancel()
	<-e.done
	e.jobs.Wait()
}
func (e *Engine) log(err error) {
	if err != nil && e.ctx.Err() == nil && e.options.Log != nil {
		e.options.Log(err)
	}
}
func (e *Engine) launch(key string, run func()) bool {
	e.mu.Lock()
	limit := 3
	if e.options.Parallel != nil {
		limit = max(1, e.options.Parallel())
	}
	// Manager turns can wait for user input. Keep their capacity separate from
	// worker reservations so a question cannot stall all ticket execution.
	active := 0
	manager := strings.HasPrefix(key, "manager/")
	for current := range e.active {
		if current != "embeddings" && strings.HasPrefix(current, "manager/") == manager {
			active++
		}
	}
	if e.active[key] || (key != "embeddings" && active >= limit) {
		e.mu.Unlock()
		return false
	}
	e.active[key] = true
	e.jobs.Add(1)
	e.mu.Unlock()
	go func() {
		defer e.jobs.Done()
		defer func() { e.mu.Lock(); delete(e.active, key); e.mu.Unlock() }()
		run()
	}()
	return true
}
func (e *Engine) loop() {
	defer close(e.done)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	lastEmbedding := time.Time{}
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
		err := e.scan(ctx)
		cancel()
		e.log(err)
		if e.options.Embed != nil && time.Since(lastEmbedding) > 5*time.Second {
			lastEmbedding = time.Now()
			e.launch("embeddings", func() {
				ctx, cancel := context.WithTimeout(e.ctx, time.Minute)
				defer cancel()
				e.log(e.options.Embed(ctx))
			})
		}
	}
}
func (e *Engine) scan(ctx context.Context) (returnErr error) {
	runtime, err := library.Ensure(ctx, e.options.Directory)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, runtime.Close()) }()
	c := runtime.Client()
	if err := c.RecoverAttempts(ctx); err != nil {
		return err
	}
	projects, err := c.ListWorkspaces(ctx, false)
	if err != nil {
		return err
	}
	for _, project := range projects.Workspaces {
		if _, err := c.DispatchTicketSchedules(ctx, project.ID); err != nil {
			return err
		}
		load, err := c.GetTicketLoad(ctx, project.ID)
		if err != nil {
			return err
		}
		parallel := 3
		if e.options.Parallel != nil {
			parallel = max(1, e.options.Parallel())
		}
		remaining := workitems.EffectiveParallel(project.Limits.MaxParallelTickets, parallel) - load.Active
		if remaining > 0 {
			var queued []workitems.Item
			for _, queue := range []string{"resume", "assignment"} {
				for cursor := ""; ; {
					page, err := c.ListTicketQueue(ctx, project.ID, queue, cursor, 100)
					if err != nil {
						return err
					}
					queued = append(queued, page.Items...)
					cursor = page.NextCursor
					if cursor == "" {
						break
					}
				}
			}
			// Assignment decisions not yet reserved in Library also occupy a
			// local project slot. Reserved/running work is counted by source.
			e.mu.Lock()
			for _, item := range queued {
				if e.active[item.ID] {
					remaining--
				}
			}
			e.mu.Unlock()
			for _, item := range queued {
				if remaining <= 0 {
					break
				}
				if e.launch(item.ID, func() { e.log(e.execute(project, item)) }) {
					remaining--
				}
			}
		}
		cursor := ""
		for {
			page, err := c.ListTicketQueue(ctx, project.ID, "merge", cursor, 100)
			if err != nil {
				return err
			}
			for _, item := range page.Items {
				e.launch(item.ID, func() { e.log(e.merge(project, item)) })
			}
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
		if e.options.Deliver != nil {
			inbox, err := c.NextPendingInbox(ctx, project.ID)
			if err != nil {
				return err
			}
			for _, entry := range inbox.Entries {
				if !entry.Delivered && !time.Now().Before(entry.NextDeliveryAt) {
					e.launch("manager/"+project.ID, func() { e.log(e.deliver(project, entry)) })
					break
				}
			}
		}
	}
	return nil
}

func needsAssignment(item workitems.Item) bool {
	return !item.Deleted && len(item.WaitingOn) == 0 && workitems.TicketReadyAt(item, time.Now()) && (item.ExecutionState == workitems.WaitingChildren || item.ExecutionState == "queued" || item.ExecutionState == "assignment_waiting" ||
		(item.ExecutionState == "" && item.State == "todo" && item.ActiveAttemptID == ""))
}

func (e *Engine) ticketLock(id string) (*worklock.Lock, error) {
	if uuid.Validate(id) != nil {
		return nil, workitems.ErrInvalidInput
	}
	return worklock.AcquireFile(e.options.Directory, filepath.Join("coordinator", "ticket-locks", id+".lock"), "q ticket coordinator")
}
func (e *Engine) execute(project workitems.Workspace, item workitems.Item) (returnErr error) {
	lock, err := e.ticketLock(item.ID)
	if errors.Is(err, worklock.ErrLocked) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	runtime, err := library.Ensure(e.ctx, e.options.Directory)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, runtime.Close()) }()
	c := runtime.Client()
	item, err = c.GetItem(e.ctx, item.ID)
	if err != nil {
		return err
	}
	if !needsAssignment(item) {
		return nil
	}
	// Earlier versions left newly issued tickets without an execution state.
	// Admit those existing tickets as well, preserving explicit stops/failures.
	if item.ExecutionState == "" {
		item, err = c.ControlTicket(e.ctx, item.ID, workitems.TicketAction{Action: "queue", Revision: item.Revision})
		if err != nil {
			return err
		}
	}
	if time.Now().Before(item.NextAssignmentAt) {
		return nil
	}
	waiting := func(err error) error {
		ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
		defer cancel()
		_, saveErr := c.ControlTicket(ctx, item.ID, workitems.TicketAction{Action: "assignment_waiting", Revision: item.Revision, Reason: err.Error()})
		return errors.Join(err, saveErr)
	}
	members, err := c.ListMembers(e.ctx, project.ID)
	if err != nil {
		return waiting(err)
	}
	pinned, previous, err := continuationMember(e.ctx, c, item)
	if err != nil {
		return waiting(err)
	}
	selection := item
	if pinned != "" {
		selection.RequestedMemberID = pinned
	}
	if item.HandoffMemberID != "" {
		selection.RequestedRole = ""
	}
	if item.ParentTicketID != "" {
		parent, err := c.GetItem(e.ctx, item.ParentTicketID)
		if err != nil {
			return waiting(err)
		}
		owner, err := c.GetAttempt(e.ctx, parent.ActiveAttemptID)
		if err != nil {
			return waiting(err)
		}
		var executionRoot string
		for _, member := range members.Members {
			if member.ID == owner.MemberID {
				executionRoot = member.ExecutionRoot
				break
			}
		}
		members.Members = slices.DeleteFunc(members.Members, func(member workitems.Member) bool {
			return member.ExecutionRoot != executionRoot || member.ID == owner.MemberID || member.ID == parent.HandoffMemberID
		})
	}
	candidates, err := e.options.Candidates(e.ctx, project, selection, members.Members)
	if err != nil {
		return waiting(err)
	}
	parallel := 3
	if e.options.Parallel != nil {
		parallel = max(1, e.options.Parallel())
	}
	load, err := c.GetTicketLoad(e.ctx, project.ID)
	if err != nil {
		return waiting(err)
	}
	if load.Active >= workitems.EffectiveParallel(project.Limits.MaxParallelTickets, parallel) {
		return nil
	}
	for i := range candidates {
		candidates[i].ActiveAttempts = load.Members[candidates[i].Member.ID]
	}
	body, err := c.ReadContent(e.ctx, item.BodyPostID, "", 8192)
	if err != nil {
		return waiting(err)
	}
	var candidate Candidate
	if pinned == "" {
		if len(candidates) == 0 && item.RequestedRole != "" {
			return waiting(fmt.Errorf("no eligible joined subagent for requested role %q", item.RequestedRole))
		}
		decisionContext, cancelDecision := context.WithTimeout(e.ctx, assignmentTimeout)
		candidate, err = SelectMember(decisionContext, systemoneconfig.Store{Dir: e.options.Directory}, item, body.Post.Content, candidates)
		cancelDecision()
		if err != nil {
			return waiting(err)
		}
	} else {
		if len(candidates) != 1 || candidates[0].Member.ID != pinned {
			return waiting(errors.New("parent worker is no longer available; retry or select a handoff member"))
		}
		candidate = candidates[0]
		if item.HandoffMemberID == "" && candidate.Digest != previous.DefinitionDigest {
			return waiting(errors.New("parent worker profile changed; select a handoff member to continue"))
		}
	}
	attempt, err := c.ClaimTicket(e.ctx, item.ID, workitems.ClaimInput{Revision: item.Revision, MemberID: candidate.Member.ID, MemberRevision: candidate.Member.Revision, DefinitionDigest: candidate.Digest, Role: config.CanonicalAgentRole(candidate.Definition.Info.Role), Owner: e.owner, RequestID: uuid.NewString(), ParallelLimit: parallel})
	if err != nil {
		return err
	}
	prompt := fmt.Sprintf("Workspace %s (%s)\nTicket #%d: %s\nTicket ID: %s; body post: %s\nExecution mode: %s\nAcceptance criteria:\n%s\n\nBody excerpt:\n%s\n\nRead the ticket and source posts through the workspace tools when more context is needed. Record findings and lessons as issue comments. Complete with evidence and a structured result for the workspace manager.", project.Name, project.ID, item.Number, item.Title, item.ID, item.BodyPostID, item.ExecutionMode, excerptBytes(strings.Join(item.Acceptance, "\n"), 8000), excerptBytes(body.Post.Content, 12000))
	if attempt.Feedback != "" {
		prompt += "\n\nManager feedback:\n" + excerptBytes(attempt.Feedback, 4000)
	}
	if len(item.DependsOn) > 0 {
		prompt += "\n\nCompleted prerequisite ticket IDs:\n" + strings.Join(item.DependsOn, "\n") + "\nRead their results and source posts through the ticket tools before continuing dependent work."
	}
	if attempt.PreviousStatus == "unknown" || attempt.PreviousStatus == "cancelled" {
		prompt += "\n\nA previous attempt may have produced side effects with an unknown result. Do not replay pending operations or assume failure. Inspect saved outcomes and the current workspace before deciding what remains."
	}
	if attempt.RepairBase != "" {
		prompt += "\n\nThe merge requires an updated review base at " + attempt.RepairBase + ". Integrate that commit into this ticket's worktree, resolve any conflicts here, and verify. Submit the result for a fresh review."
	}
	if pinned != "" {
		prompt += "\n\nContinue the existing parent ticket in its saved worktree. Read its issue, ticket history, and child results before changing files. If child work is queued, finish this turn with a clean checkout."
		if attempt.ReviewChildAttemptID != "" {
			prompt = fmt.Sprintf("Review the assigned child of parent ticket %s. Call ticket_attempt_get and ticket_change_read with empty arguments and check the child's acceptance criteria. Call ticket_review with a decision of accept, request_changes, or reject. Include actionable feedback when requesting changes. Keep the parent's checkout clean. End this turn once the decision is recorded. Q applies an approved merge after this turn returns.", item.ID)
		} else if attempt.PreviousStatus == workitems.WaitingChildren {
			prompt += "\nThe required child changes are merged into this worktree. Verify the combined result and finish the parent ticket for its reviewer."
		}
		if item.HandoffMemberID != "" {
			prompt += "\nThis is an explicit handoff. Reconstruct context from the parent ticket, its recorded results and child tickets, and the existing worktree before acting."
			if attempt.ReviewChildAttemptID == "" {
				prompt += " Read the previous parent attempt with ticket_worker_read. Attempt ID: " + attempt.PreviousAttemptID
			}
		}
		if attempt.ReviewChildAttemptID == "" {
			if page, err := c.ListChildren(e.ctx, item.ID, attempt.WorktreeID); err == nil && len(page.Items) > 0 {
				var children strings.Builder
				for _, child := range page.Items {
					fmt.Fprintf(&children, "\n- #%d %s (%s). Attempt: %s", child.Number, child.ID, child.ExecutionState, child.ActiveAttemptID)
					if children.Len() >= 4000 {
						break
					}
				}
				prompt += "\n\nDirect child results to inspect:" + children.String()
			}
		}
	}
	if item.ExecutionMode == "git" && attempt.ReviewChildAttemptID == "" {
		prompt += "\nTo delegate a direct child, call ticket_create with as_child:true, execution_mode:git, and a stable request_id. Use requested_role when the child should go to a specific role. Finish the current turn with a clean committed checkout. If your tools include task_complete, use outcome:succeeded to finish this turn after issuing the child. Q keeps the parent open, runs the child, and reopens this session for review."
	}
	update := workitems.AttemptUpdate{Owner: attempt.Owner, Token: attempt.Token, Generation: attempt.Generation, Status: "running"}
	attempt, err = c.UpdateAttempt(e.ctx, attempt.ID, update)
	if err != nil {
		return err
	}
	runContext, stop := e.heartbeat(c, attempt)
	result, runErr := e.options.Run(runContext, c, project, selection, candidate, attempt, prompt)
	interrupted := runContext.Err() != nil
	stop()
	status := "awaiting_review"
	if runErr != nil {
		status = "blocked"
		if interrupted || e.ctx.Err() != nil {
			status = "unknown"
		}
		result.Outcome = "blocked"
		if result.Summary == "" {
			result.Summary = "Ticket execution did not complete"
		}
		result.Blocker = runErr.Error()
	} else if result.Outcome == workitems.WaitingChildren {
		status = workitems.WaitingChildren
	} else if result.Outcome != "succeeded" {
		status = "blocked"
	}
	if current, err := c.GetAttempt(e.ctx, attempt.ID); err == nil && current.StopRequested {
		status = "cancelled"
	}
	raw, _ := json.Marshal(result)
	update.Status = status
	update.Result = raw
	update.ChangeRequest = result.ChangeRequest
	update.OperationError = errorText(runErr)
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, saveErr := c.UpdateAttempt(cleanup, attempt.ID, update)
	return errors.Join(runErr, saveErr)
}

func continuationMember(ctx context.Context, c *library.Client, item workitems.Item) (string, workitems.Attempt, error) {
	if item.ActiveAttemptID == "" {
		return "", workitems.Attempt{}, nil
	}
	previous, err := c.GetAttempt(ctx, item.ActiveAttemptID)
	if err != nil {
		return "", previous, err
	}
	if item.HandoffMemberID != "" {
		return item.HandoffMemberID, previous, nil
	}
	if item.ExecutionState == workitems.WaitingChildren {
		return previous.MemberID, previous, nil
	}
	if request := previous.ChangeRequest; request != nil && !request.Final() {
		page, err := c.ListChildren(ctx, item.ID, previous.WorktreeID)
		if err != nil {
			return "", previous, err
		}
		if len(page.Items) > 0 {
			return previous.MemberID, previous, nil
		}
	}
	return "", previous, nil
}

// stop waits for an in-flight heartbeat before publishing a terminal result.
func (e *Engine) heartbeat(c *library.Client, a workitems.Attempt) (context.Context, func()) {
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				if _, err := c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: a.Status}); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { close(stop); <-done; cancel() }
}
func (e *Engine) merge(project workitems.Workspace, item workitems.Item) (returnErr error) {
	lock, err := e.ticketLock(item.ID)
	if errors.Is(err, worklock.ErrLocked) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	runtime, err := library.Ensure(e.ctx, e.options.Directory)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, runtime.Close()) }()
	c := runtime.Client()
	a, err := c.GetAttempt(e.ctx, item.ActiveAttemptID)
	if err != nil {
		return err
	}
	if a.ChangeRequest == nil {
		return errors.New("ticket has no pinned change request")
	}
	key := sha256.Sum256([]byte(a.ChangeRequest.RepositoryRoot + "\n" + a.ChangeRequest.BaseRef))
	repoLock, err := worklock.AcquireFile(e.options.Directory, filepath.Join("coordinator", "merge-locks", hex.EncodeToString(key[:])+".lock"), "q ticket merge")
	if errors.Is(err, worklock.ErrLocked) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, repoLock.Close()) }()
	a, err = c.ClaimMerge(e.ctx, a.ID, e.owner)
	if err != nil {
		return err
	}
	ctx, stop := e.heartbeat(c, a)
	manager := gitwork.Manager{Root: filepath.Join(e.options.Directory, "worktrees")}
	request := *a.ChangeRequest
	candidate := a.MergeCandidate
	var repairBase string
	var mergeErr error
	if a.Status == "closing" {
		request, mergeErr = manager.Close(ctx, request)
	} else {
		mergeErr = (workspace.Store{Root: request.RepositoryRoot}).EnsureQGitLocallyIgnored(ctx)
		if mergeErr == nil {
			for retry := 0; retry < 3; retry++ {
				if candidate == nil {
					var prepared change.MergeCandidate
					prepared, mergeErr = manager.PrepareCandidate(ctx, request)
					if mergeErr != nil {
						if errors.Is(mergeErr, gitwork.ErrMergeConflict) {
							repairBase = prepared.Target
						} else if (errors.Is(mergeErr, gitwork.ErrMergeTreeUnavailable) || errors.Is(mergeErr, gitwork.ErrMergeCommitHooks)) && prepared.Target == request.BaseCommit {
							request, mergeErr = manager.Merge(ctx, request.RepositoryRoot, request)
						} else if errors.Is(mergeErr, gitwork.ErrMergeCommitHooks) || errors.Is(mergeErr, gitwork.ErrMergeTreeUnavailable) {
							repairBase = prepared.Target
						} else if errors.Is(mergeErr, gitwork.ErrReviewAlreadyInTarget) {
							if adopted, err := manager.Merge(ctx, request.RepositoryRoot, request); !errors.Is(err, gitwork.ErrBaseMoved) {
								request, mergeErr = adopted, err
							}
						}
						break
					}
					candidate = &prepared
					_, mergeErr = c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, MergeCandidate: candidate})
					if mergeErr != nil {
						break
					}
				}
				request, mergeErr = manager.ApplyCandidate(ctx, request, *candidate)
				if !errors.Is(mergeErr, gitwork.ErrBaseMoved) || retry == 2 {
					break
				}
				candidate = nil
			}
		}
	}
	interrupted := ctx.Err() != nil
	stop()
	if interrupted && mergeErr != nil {
		return mergeErr // The expired lease resumes the recorded Git operation.
	}
	status := "accepted"
	landed := mergeErr != nil && request.Status == change.StatusMerged
	if mergeErr != nil {
		status = "merge_failed"
		// Keep the original pinned request so retry can adopt the exact merge
		// commit if Git succeeded but worktree cleanup or acknowledgement failed.
		request = *a.ChangeRequest
	}
	if a.Status == "closing" {
		status = "rejected"
		if mergeErr != nil {
			status = "close_failed"
		}
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = c.UpdateAttempt(cleanup, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: status, ChangeRequest: &request, OperationError: excerptBytes(errorText(mergeErr), 8000), RepairBase: repairBase, MergeLanded: landed})
	if err == nil && mergeErr == nil && candidate != nil {
		err = manager.DeleteCandidate(cleanup, request, *candidate)
	}
	return errors.Join(mergeErr, err)
}
func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
func (e *Engine) deliver(project workitems.Workspace, entry workitems.InboxEntry) (returnErr error) {
	lock, err := worklock.AcquireFile(e.options.Directory, filepath.Join("coordinator", "manager-locks", project.ID+".lock"), "q manager inbox")
	if errors.Is(err, worklock.ErrLocked) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	runtime, err := library.Ensure(e.ctx, e.options.Directory)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, runtime.Close()) }()
	c := runtime.Client()
	inbox, err := c.NextPendingInbox(e.ctx, project.ID)
	if err != nil {
		return err
	}
	// Recheck under the project lock: another host may have acknowledged or
	// deferred the notification since scan. Historical inbox rows stay unread.
	if len(inbox.Entries) == 0 || inbox.Entries[0].ID != entry.ID {
		return nil
	}
	entry = inbox.Entries[0]
	a, err := c.GetAttempt(e.ctx, entry.AttemptID)
	if err != nil {
		return err
	}
	if entry.AttemptStatus != "" && entry.AttemptStatus != a.Status {
		return c.AcknowledgeInbox(e.ctx, entry.ID, entry.ManagerSessionID)
	}
	if err := e.options.Deliver(e.ctx, project, entry, a); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return errors.Join(err, c.DeferInbox(cleanup, entry.ID, entry.ManagerSessionID, err.Error()))
	}
	return c.AcknowledgeInbox(e.ctx, entry.ID, entry.ManagerSessionID)
}
