package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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
		cursor := ""
		var tickets []workitems.Item
		for {
			page, err := c.ListItems(ctx, project.ID, workitems.Ticket, cursor, 100)
			if err != nil {
				return err
			}
			tickets = append(tickets, page.Items...)
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
		parallel := 3
		if e.options.Parallel != nil {
			parallel = max(1, e.options.Parallel())
		}
		active := 0
		e.mu.Lock()
		for _, item := range tickets {
			if item.ExecutionState == "running" || item.ExecutionState == "reserved" || item.ExecutionState == "stopping" || (needsAssignment(item) && e.active[item.ID]) {
				active++
			}
		}
		e.mu.Unlock()
		remaining := workitems.EffectiveParallel(project.Limits.MaxParallelTickets, parallel) - active
		for _, item := range tickets {
			if item.Deleted {
				continue
			}
			// Filter full projects before occupying a host worker slot. Otherwise
			// their queued assignments can starve projects that still have room.
			if remaining > 0 && needsAssignment(item) && !time.Now().Before(item.NextAssignmentAt) {
				if e.launch(item.ID, func() { e.log(e.execute(project, item)) }) {
					remaining--
				}
			}
			if item.ExecutionState == "merge_queued" || item.ExecutionState == "close_queued" {
				e.launch(item.ID, func() { e.log(e.merge(project, item)) })
			}
		}
		if e.options.Deliver != nil {
			inbox, err := c.ListInbox(ctx, project.ID)
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
	return !item.Deleted && (item.ExecutionState == "queued" || item.ExecutionState == "assignment_waiting" ||
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
	candidates, err := e.options.Candidates(e.ctx, project, item, members.Members)
	if err != nil {
		return waiting(err)
	}
	parallel := 3
	if e.options.Parallel != nil {
		parallel = max(1, e.options.Parallel())
	}
	active := 0
	loads := map[string]int{}
	cursor := ""
	for {
		page, err := c.ListItems(e.ctx, project.ID, workitems.Ticket, cursor, 100)
		if err != nil {
			return waiting(err)
		}
		for _, ticket := range page.Items {
			if ticket.ExecutionState == "running" || ticket.ExecutionState == "reserved" || ticket.ExecutionState == "stopping" {
				loads[ticket.AssignedMemberID]++
				active++
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if active >= workitems.EffectiveParallel(project.Limits.MaxParallelTickets, parallel) {
		return nil
	}
	for i := range candidates {
		candidates[i].ActiveAttempts = loads[candidates[i].Member.ID]
	}
	body, err := c.ReadContent(e.ctx, item.BodyPostID, "", 8192)
	if err != nil {
		return waiting(err)
	}
	decisionContext, cancelDecision := context.WithTimeout(e.ctx, assignmentTimeout)
	candidate, err := SelectMember(decisionContext, systemoneconfig.Store{Dir: e.options.Directory}, item, body.Post.Content, candidates)
	cancelDecision()
	if err != nil {
		return waiting(err)
	}
	attempt, err := c.ClaimTicket(e.ctx, item.ID, workitems.ClaimInput{Revision: item.Revision, MemberID: candidate.Member.ID, MemberRevision: candidate.Member.Revision, DefinitionDigest: candidate.Digest, Owner: e.owner, RequestID: uuid.NewString(), ParallelLimit: parallel})
	if err != nil {
		return err
	}
	prompt := fmt.Sprintf("Workspace %s (%s)\nTicket #%d: %s\nTicket ID: %s; body post: %s\nExecution mode: %s\nAcceptance criteria:\n%s\n\nBody excerpt:\n%s\n\nRead the ticket and source posts through the workspace tools when more context is needed. Record findings and lessons as issue comments. Complete with evidence and a structured result for the workspace manager.", project.Name, project.ID, item.Number, item.Title, item.ID, item.BodyPostID, item.ExecutionMode, excerptBytes(strings.Join(item.Acceptance, "\n"), 8000), excerptBytes(body.Post.Content, 12000))
	if attempt.Feedback != "" {
		prompt += "\n\nManager feedback:\n" + excerptBytes(attempt.Feedback, 4000)
	}
	if attempt.PreviousStatus == "unknown" || attempt.PreviousStatus == "cancelled" {
		prompt += "\n\nA previous attempt may have produced side effects with an unknown result. Do not replay pending operations or assume failure. Inspect saved outcomes and the current workspace before deciding what remains."
	}
	update := workitems.AttemptUpdate{Owner: attempt.Owner, Token: attempt.Token, Generation: attempt.Generation, Status: "running"}
	attempt, err = c.UpdateAttempt(e.ctx, attempt.ID, update)
	if err != nil {
		return err
	}
	runContext, stop := e.heartbeat(c, attempt)
	result, runErr := e.options.Run(runContext, c, project, item, candidate, attempt, prompt)
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
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, saveErr := c.UpdateAttempt(cleanup, attempt.ID, update)
	return errors.Join(runErr, saveErr)
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
	var mergeErr error
	if a.Status == "closing" {
		request, mergeErr = manager.Close(ctx, request)
	} else {
		mergeErr = (workspace.Store{Root: request.RepositoryRoot}).EnsureQGitLocallyIgnored(ctx)
		if mergeErr == nil {
			request, mergeErr = manager.Merge(ctx, request.RepositoryRoot, request)
		}
	}
	stop()
	status := "accepted"
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
	_, err = c.UpdateAttempt(cleanup, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: status, ChangeRequest: &request, OperationError: errorText(mergeErr)})
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
	inbox, err := c.ListInbox(e.ctx, project.ID)
	if err != nil {
		return err
	}
	for _, current := range inbox.Entries {
		if current.ID == entry.ID && current.Delivered {
			return nil
		}
	}
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
