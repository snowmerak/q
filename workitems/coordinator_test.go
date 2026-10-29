package workitems

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTicketReservationReviewFeedbackAndFencing(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := t.Context()
	root := t.TempDir()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Q", WorkspaceRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.PutMember(ctx, w.ID, MemberInput{AgentID: "builtin/research", DefinitionRoot: root, ExecutionRoot: root, RequestID: "join"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Investigate", Content: "Find evidence", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	item, err = s.ControlTicket(ctx, item.ID, TicketAction{Action: "queue", Revision: item.Revision, RequestID: "queue"})
	if err != nil {
		t.Fatal(err)
	}
	claim := ClaimInput{Revision: item.Revision, MemberID: m.ID, MemberRevision: m.Revision, Owner: "host-a", DefinitionDigest: "profile-v1", RequestID: "claim"}
	a, err := s.ClaimTicket(ctx, item.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.ClaimTicket(ctx, item.ID, claim)
	if err != nil || replay.ID != a.ID {
		t.Fatalf("claim receipt: %+v %v", replay, err)
	}
	claim.RequestID = "second"
	if _, err := s.ClaimTicket(ctx, item.ID, claim); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate reservation: %v", err)
	}
	update := AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "running"}
	a, err = s.UpdateAttempt(ctx, a.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	update.Status = "awaiting_review"
	update.Result = []byte(`{"outcome":"succeeded"}`)
	if _, err := s.UpdateAttempt(ctx, a.ID, update); err != nil {
		t.Fatal(err)
	}
	inbox, err := s.ListInbox(ctx, w.ID)
	if err != nil || len(inbox) != 1 || inbox[0].ManagerSessionID != w.ManagerSessionID {
		t.Fatalf("inbox %+v %v", inbox, err)
	}
	if err := s.DeferInbox(ctx, inbox[0].ID, "other-manager", "busy"); !errors.Is(err, ErrConflict) {
		t.Fatalf("inbox retry authority: %v", err)
	}
	if err := s.DeferInbox(ctx, inbox[0].ID, w.ManagerSessionID, "manager is busy"); err != nil {
		t.Fatal(err)
	}
	deferred, err := s.ListInbox(ctx, w.ID)
	if err != nil || len(deferred) != 1 || deferred[0].Delivered || deferred[0].OperationID != inbox[0].OperationID || deferred[0].DeliveryError == "" || deferred[0].DeliveryAttempts != 1 || !deferred[0].NextDeliveryAt.After(time.Now()) {
		t.Fatalf("durable inbox retry: %+v %v", deferred, err)
	}
	if err := s.AcknowledgeInbox(ctx, inbox[0].ID, w.ManagerSessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewAttempt(ctx, a.ID, ReviewInput{ManagerSessionID: "other", Decision: "accept", RequestID: "wrong-manager"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("manager authority: %v", err)
	}
	if _, err := s.ReviewAttempt(ctx, a.ID, ReviewInput{ManagerSessionID: w.CoordinatorSessionID, Decision: "accept", RequestID: "coordinator-cannot-review"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Coordinator acquired Integrator review authority: %v", err)
	}
	a, err = s.ReviewAttempt(ctx, a.ID, ReviewInput{ManagerSessionID: w.ManagerSessionID, Decision: "request_changes", Feedback: "Include the source links", RequestID: "review"})
	if err != nil {
		t.Fatal(err)
	}
	item, err = s.GetItem(ctx, item.ID)
	if err != nil || item.State != "in_progress" || item.ExecutionState != "queued" {
		t.Fatalf("feedback ticket %+v %v", item, err)
	}
	claim.Revision = item.Revision
	claim.RequestID = "followup"
	next, err := s.ClaimTicket(ctx, item.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	if next.WorkerSessionID != a.WorkerSessionID || next.Generation <= a.Generation || next.Feedback == "" {
		t.Fatalf("followup lost checkpoint identity: %+v", next)
	}
	if _, err := s.UpdateAttempt(ctx, a.ID, update); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale worker completed: %v", err)
	}
	update = AttemptUpdate{Owner: next.Owner, Token: next.Token, Generation: next.Generation, Status: "awaiting_review", Result: []byte(`{"outcome":"succeeded"}`)}
	if _, err := s.UpdateAttempt(ctx, next.ID, update); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewAttempt(ctx, next.ID, ReviewInput{ManagerSessionID: w.ManagerSessionID, Decision: "accept", RequestID: "accepted"}); err != nil {
		t.Fatal(err)
	}
	item, err = s.GetItem(ctx, item.ID)
	if err != nil || item.State != "done" {
		t.Fatalf("accepted report %+v %v", item, err)
	}
}

func TestExpiredAttemptBecomesUnknownAndDoesNotReplay(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := t.Context()
	root := t.TempDir()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Q", WorkspaceRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.PutMember(ctx, w.ID, MemberInput{AgentID: "builtin/research", DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Uncertain", Content: "Operation", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	item, err = s.ControlTicket(ctx, item.ID, TicketAction{Action: "queue", Revision: item.Revision})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.ClaimTicket(ctx, item.ID, ClaimInput{Revision: item.Revision, MemberID: m.ID, MemberRevision: m.Revision, Owner: "dead-host", DefinitionDigest: "v1", RequestID: "claim"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.LeaseUntil = time.Now().Add(-time.Minute)
	if err := saveAttempt(ctx, tx, a, false); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	item, err = s.GetItem(ctx, item.ID)
	if err != nil || item.ExecutionState != "unknown" {
		t.Fatalf("expired item %+v %v", item, err)
	}
	if _, err := s.ClaimTicket(ctx, item.ID, ClaimInput{Revision: item.Revision, MemberID: m.ID, MemberRevision: m.Revision, Owner: "new-host", DefinitionDigest: "v1", RequestID: "implicit-replay"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown attempt replayed: %v", err)
	}
}

func TestMembersBindSameNamedProfilesToDistinctProjectRoots(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := t.Context()
	roots := []string{t.TempDir(), t.TempDir()}
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Multi-root", WorkspaceRoots: roots})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, root := range roots {
		member, err := s.PutMember(ctx, w.ID, MemberInput{AgentID: "workspace/reader", DefinitionRoot: root, ExecutionRoot: root})
		if err != nil || ids[member.ID] {
			t.Fatalf("root binding %+v %v", member, err)
		}
		ids[member.ID] = true
		if _, err := s.PutMember(ctx, w.ID, MemberInput{AgentID: "workspace/reader", DefinitionRoot: root, ExecutionRoot: root}); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate root binding: %v", err)
		}
	}
	members, err := s.ListMembers(ctx, w.ID)
	if err != nil || len(members) != 2 || members[0].DefinitionRoot == members[1].DefinitionRoot {
		t.Fatalf("members %+v %v", members, err)
	}
}

func TestWorkerCapacityAndLateResultsRespectStopAndMembership(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx, root := t.Context(), t.TempDir()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Fences", WorkspaceRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.PutMember(ctx, w.ID, MemberInput{AgentID: "builtin/research", DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	queue := func(title string) Item {
		t.Helper()
		item, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: title, Content: "inspect", Author: "user"})
		if err == nil {
			item, err = s.ControlTicket(ctx, item.ID, TicketAction{Action: "queue", Revision: item.Revision})
		}
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	claim := func(item Item, owner string) (Attempt, error) {
		return s.ClaimTicket(ctx, item.ID, ClaimInput{ParallelLimit: 1, Revision: item.Revision, MemberID: m.ID, MemberRevision: m.Revision, Owner: owner, DefinitionDigest: "profile", RequestID: owner})
	}
	first, second := queue("Stop"), queue("Disable")
	a, err := claim(first, "host-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claim(second, "host-b-busy"); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-host capacity: %v", err)
	}
	first, err = s.GetItem(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ControlTicket(ctx, first.ID, TicketAction{Action: "cancel", Revision: first.Revision}); err != nil {
		t.Fatal(err)
	}
	result := []byte(`{"outcome":"succeeded","summary":"late result"}`)
	a, err = s.UpdateAttempt(ctx, a.ID, AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "awaiting_review", Result: result})
	if err != nil || a.Status != "cancelled" || string(a.Result) != string(result) {
		t.Fatalf("late result after stop: %+v %v", a, err)
	}
	b, err := claim(second, "host-b-ready")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutMember(ctx, w.ID, MemberInput{ID: m.ID, AgentID: m.AgentID, DefinitionRoot: root, ExecutionRoot: root, Disabled: true, Revision: m.Revision}); err != nil {
		t.Fatal(err)
	}
	b, err = s.UpdateAttempt(ctx, b.ID, AttemptUpdate{Owner: b.Owner, Token: b.Token, Generation: b.Generation, Status: "awaiting_review", Result: result})
	if err != nil || b.Status != "blocked" || b.OperationError == "" || string(b.Result) != string(result) {
		t.Fatalf("late result after member disabled: %+v %v", b, err)
	}
}
