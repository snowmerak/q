package workitems

import (
	"errors"
	"testing"
	"time"

	"github.com/snowmerak/q/change"
)

func TestChildReviewHandoffFencesPriorWorker(t *testing.T) {
	s, project, senior := dependencyFixture(t)
	ctx := t.Context()
	join := func(agent string) Member {
		t.Helper()
		m, err := s.PutMember(ctx, project.ID, MemberInput{AgentID: agent, DefinitionRoot: senior.DefinitionRoot, ExecutionRoot: senior.ExecutionRoot})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	junior, replacement := join("builtin/junior-developer"), join("builtin/senior-developer")
	parent, err := s.CreateItem(ctx, CreateInput{WorkspaceID: project.ID, Kind: Ticket, Title: "Parent", Content: "Delegate", Author: "user", ExecutionMode: "git", RequestedMemberID: senior.ID})
	if err != nil {
		t.Fatal(err)
	}
	claim := func(id string, member Member, key string) (Attempt, error) {
		t.Helper()
		item, err := s.GetItem(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return s.ClaimTicket(ctx, id, ClaimInput{Revision: item.Revision, MemberID: member.ID, MemberRevision: member.Revision, Owner: "host", DefinitionDigest: member.ID, RequestID: key, ParallelLimit: 1})
	}
	update := func(a Attempt, status string, request *change.Request) Attempt {
		t.Helper()
		next, err := s.UpdateAttempt(ctx, a.ID, AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: status, ChangeRequest: request})
		if err != nil {
			t.Fatal(err)
		}
		return next
	}
	active, err := claim(parent.ID, senior, "claim-parent")
	if err != nil {
		t.Fatal(err)
	}
	request := change.Request{ID: active.WorktreeID, RepositoryRoot: senior.ExecutionRoot, WorktreePath: "temporary-parent", BaseRef: "main", BaseCommit: "base", HeadRef: "q/delegate/parent", Status: change.StatusWorking, Version: change.Version, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	active = update(active, "running", &request)
	issuer := active.Binding()
	childInput := CreateInput{ParentWorker: &issuer, WorkspaceID: project.ID, Kind: Ticket, Title: "Child", Content: "Implement", Author: "agent", ExecutionMode: "git", RequestID: "create-child"}
	child, err := s.CreateItem(ctx, childInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claim(child.ID, junior, "before-yield"); !errors.Is(err, ErrConflict) {
		t.Fatalf("early child claim: %v", err)
	}
	update(active, WaitingChildren, &request)
	if replay, err := s.CreateItem(ctx, childInput); err != nil || replay.ID != child.ID {
		t.Fatalf("child creation replay: %+v %v", replay, err)
	}
	worker, err := claim(child.ID, junior, "claim-child")
	if err != nil {
		t.Fatal(err)
	}
	childRequest := request
	childRequest.Status, childRequest.HeadCommit = change.StatusOpen, "reviewed-head"
	worker = update(worker, "awaiting_review", &childRequest)
	firstReviewer, err := claim(parent.ID, senior, "first-review")
	if err != nil || firstReviewer.ReviewChildAttemptID != worker.ID || firstReviewer.WorkerSessionID != active.WorkerSessionID {
		t.Fatalf("review reservation: %+v %v", firstReviewer, err)
	}
	firstReviewer = update(firstReviewer, "running", &request)
	update(firstReviewer, "blocked", &request)
	parent, err = s.GetItem(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ControlTicket(ctx, parent.ID, TicketAction{Action: "handoff", Revision: parent.Revision, MemberID: junior.ID, RequestID: "self-handoff"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("handoff to child worker: %v", err)
	}
	if _, err := s.ControlTicket(ctx, parent.ID, TicketAction{Action: "handoff", Revision: parent.Revision, MemberID: replacement.ID, RequestID: "handoff"}); err != nil {
		t.Fatal(err)
	}
	newReviewer, err := claim(parent.ID, replacement, "new-reviewer")
	if err != nil || newReviewer.WorkerSessionID == active.WorkerSessionID || newReviewer.WorktreeID != active.WorktreeID || newReviewer.ReviewChildAttemptID != worker.ID {
		t.Fatalf("handed-off reviewer: %+v %v", newReviewer, err)
	}
	newReviewer = update(newReviewer, "running", &request)
	stale := firstReviewer.Binding()
	if _, err := s.ReviewAttempt(ctx, worker.ID, ReviewInput{Reviewer: &stale, Decision: "accept", RequestID: "stale-review"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale review: %v", err)
	}
	current := newReviewer.Binding()
	childInput.ParentWorker = &current
	if replay, err := s.CreateItem(ctx, childInput); err != nil || replay.ID != child.ID {
		t.Fatalf("child creation replay after handoff: %+v %v", replay, err)
	}
	if _, err := s.ReviewAttempt(ctx, worker.ID, ReviewInput{Reviewer: &current, Decision: "accept", RequestID: "approved"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimMerge(ctx, worker.ID, "merge"); !errors.Is(err, ErrConflict) {
		t.Fatalf("merge while parent reviewing: %v", err)
	}
	update(newReviewer, WaitingChildren, &request)
	if _, err := s.ClaimMerge(ctx, worker.ID, "merge"); err != nil {
		t.Fatal(err)
	}
}
