package workitems

import (
	"errors"
	"testing"
)

func TestStartApprovalAndTicketQuestionReleaseWorkerSlot(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	ctx := t.Context()
	root := t.TempDir()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Decisions", WorkspaceRoots: []string{root}, Limits: &ProjectLimits{RequireTicketApproval: true}})
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.PutMember(ctx, w.ID, MemberInput{AgentID: "global/worker", DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Make a decision", Content: "Change the API", Author: "user"})
	if err != nil || ticket.ExecutionState != WaitingApproval {
		t.Fatalf("ticket=%+v err=%v", ticket, err)
	}
	claim := func(store *Store, item Item, request string) (Attempt, error) {
		return store.ClaimTicket(ctx, item.ID, ClaimInput{
			Revision: item.Revision, MemberID: member.ID, MemberRevision: member.Revision,
			Owner: "host", DefinitionDigest: "worker-v1", RequestID: request, ParallelLimit: 1,
		})
	}
	if _, err := claim(s, ticket, "unapproved"); !errors.Is(err, ErrConflict) {
		t.Fatalf("unapproved ticket claimed: %v", err)
	}
	approve := func(store *Store, item Item, request string) Item {
		t.Helper()
		item, err := store.ControlTicket(ctx, item.ID, TicketAction{Action: "approve_start", Revision: item.Revision, RequestID: request})
		if err != nil || item.ExecutionState != "queued" {
			t.Fatalf("approved ticket=%+v err=%v", item, err)
		}
		return item
	}
	ticket = approve(s, ticket, "approve-v1")
	if _, err := s.UpdatePost(ctx, ticket.BodyPostID, PostInput{Revision: 1, Content: "Change the API and update docs", Author: "user"}); err != nil {
		t.Fatal(err)
	}
	ticket, err = s.GetItem(ctx, ticket.ID)
	if err != nil || ticket.ExecutionState != WaitingApproval {
		t.Fatalf("changed ticket=%+v err=%v", ticket, err)
	}
	ticket = approve(s, ticket, "approve-v2")
	attempt, err := claim(s, ticket, "start")
	if err != nil {
		t.Fatal(err)
	}
	update := AttemptUpdate{Owner: attempt.Owner, Token: attempt.Token, Generation: attempt.Generation, Status: WaitingUser, UserQuestion: &UserQuestion{Question: "Keep the old API?", Choices: []string{"Keep it", "Remove it"}}}
	attempt, err = s.UpdateAttempt(ctx, attempt.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Independent work", Content: "Check unrelated docs", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	other = approve(s, other, "approve-independent")
	independent, err := claim(s, other, "independent")
	if err != nil {
		t.Fatalf("waiting ticket retained the worker slot: %v", err)
	}
	if _, err := s.UpdateAttempt(ctx, independent.ID, AttemptUpdate{Owner: independent.Owner, Token: independent.Token, Generation: independent.Generation, Status: "awaiting_review"}); err != nil {
		t.Fatal(err)
	}
	// A new store has no process-local worker or question state.
	restarted := openTestStore(t, dir)
	inbox, err := restarted.HumanInbox(ctx, w.ID)
	if err != nil || len(inbox.Entries) != 2 {
		t.Fatalf("inbox=%+v err=%v", inbox, err)
	}
	if _, err := restarted.AnswerQuestion(ctx, attempt.ID, AnswerInput{QuestionID: "other", Answer: "Keep it", RequestID: "bad"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("resolved wrong question: %v", err)
	}
	in := AnswerInput{QuestionID: attempt.UserQuestion.ID, Answer: "Keep it", RequestID: "answer"}
	ticket, err = restarted.AnswerQuestion(ctx, attempt.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.AnswerQuestion(ctx, attempt.ID, in); err != nil {
		t.Fatalf("retry lost the receipt: %v", err)
	}
	in.RequestID = "duplicate"
	if _, err := restarted.AnswerQuestion(ctx, attempt.ID, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("second answer accepted: %v", err)
	}
	resumed, err := claim(restarted, ticket, "resume")
	if err != nil || resumed.WorkerSessionID != attempt.WorkerSessionID || resumed.WorktreeID != attempt.WorktreeID || resumed.PreviousAnswer == nil || resumed.PreviousAnswer.Answer != "Keep it" {
		t.Fatalf("resume=%+v err=%v", resumed, err)
	}
}
