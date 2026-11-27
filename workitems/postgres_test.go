package workitems

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPostgresDialectRebind(t *testing.T) {
	d := postgresDialect{}
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   `SELECT * FROM items WHERE id=?`,
			want: `SELECT * FROM items WHERE id=$1`,
		},
		{
			in:   `SELECT 'is it?' || '?' AS q, id FROM items WHERE workspace_id=? AND kind=?`,
			want: `SELECT 'is it?' || '?' AS q, id FROM items WHERE workspace_id=$1 AND kind=$2`,
		},
		{
			in:   `SELECT 'can''t?stop' FROM items WHERE a=? AND b=?`,
			want: `SELECT 'can''t?stop' FROM items WHERE a=$1 AND b=$2`,
		},
	}
	for _, tc := range cases {
		if got := d.rebind(tc.in); got != tc.want {
			t.Fatalf("rebind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestOpenAndPingPostgresValidation(t *testing.T) {
	ctx := t.Context()
	if _, err := OpenPostgres(ctx, "   "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("OpenPostgres(empty) = %v, want ErrInvalidInput", err)
	}
	if err := PingPostgres(ctx, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("PingPostgres(empty) = %v, want ErrInvalidInput", err)
	}
}

func TestPostgresRepositoryContract(t *testing.T) {
	dsn := os.Getenv("Q_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("Q_TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	if err := PingPostgres(ctx, dsn); err != nil {
		t.Fatalf("PingPostgres: %v", err)
	}
	store, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Verify idempotent re-initialization on an already-initialized DB.
	store2, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenPostgres second open: %v", err)
	}
	_ = store2.Close()

	// Ensure Repository interface is satisfied and test end-to-end contract.
	var repo Repository = store

	defaults, err := repo.GetProjectDefaults(ctx)
	if err != nil {
		t.Fatalf("GetProjectDefaults: %v", err)
	}
	if _, err := repo.UpdateProjectDefaults(ctx, ProjectDefaults{
		AgentIDs: []string{"builtin/worker"},
		Revision: defaults.Revision,
	}); err != nil {
		t.Fatalf("UpdateProjectDefaults: %v", err)
	}

	root := t.TempDir()
	wsName := "pg-ws-" + filepath.Base(root)
	ws, err := repo.CreateWorkspace(ctx, WorkspaceInput{
		Name:           wsName,
		WorkspaceRoots: []string{root},
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	defer func() { _ = repo.ArchiveWorkspace(ctx, ws.ID) }()

	// Duplicate workspace root should return ErrConflict.
	if _, err := repo.CreateWorkspace(ctx, WorkspaceInput{
		Name:           wsName + "-dup",
		WorkspaceRoots: []string{root},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreateWorkspace duplicate root = %v, want ErrConflict", err)
	}

	workspaces, err := repo.ListWorkspaces(ctx, false)
	if err != nil || len(workspaces) == 0 {
		t.Fatalf("ListWorkspaces: workspaces=%d err=%v", len(workspaces), err)
	}

	// PutAsset / GetAsset.
	asset, err := repo.PutAsset(ctx, ws.ID, []byte("hello postgres asset"), false)
	if err != nil {
		t.Fatalf("PutAsset: %v", err)
	}
	gotAsset, err := repo.GetAsset(ctx, ws.ID, asset.ID)
	if err != nil || string(gotAsset.Data) != "hello postgres asset" {
		t.Fatalf("GetAsset: %+v err=%v", gotAsset, err)
	}

	// Create issue + idempotent receipt + dependent tickets.
	issue, err := repo.CreateItem(ctx, CreateInput{
		WorkspaceID: ws.ID,
		Kind:        Issue,
		Title:       "PG Contract Issue",
		Content:     "Track Postgres contract",
		Author:      "tester",
		Labels:      []string{"postgres"},
		RequestID:   "req-issue-1",
	})
	if err != nil {
		t.Fatalf("CreateItem(issue): %v", err)
	}
	issueReplay, err := repo.CreateItem(ctx, CreateInput{
		WorkspaceID: ws.ID,
		Kind:        Issue,
		Title:       "PG Contract Issue",
		Content:     "Track Postgres contract",
		Author:      "tester",
		Labels:      []string{"postgres"},
		RequestID:   "req-issue-1",
	})
	if err != nil || issueReplay.ID != issue.ID {
		t.Fatalf("CreateItem replay mismatch: replay=%q orig=%q err=%v", issueReplay.ID, issue.ID, err)
	}

	prereq, err := repo.CreateItem(ctx, CreateInput{
		WorkspaceID:   ws.ID,
		Kind:          Ticket,
		Title:         "Prerequisite Ticket",
		Content:       "Run first",
		Author:        "tester",
		LinkedIssueID: issue.ID,
		RequestID:     "req-t-1",
	})
	if err != nil {
		t.Fatalf("CreateItem(prereq): %v", err)
	}
	blocked, err := repo.CreateItem(ctx, CreateInput{
		WorkspaceID:   ws.ID,
		Kind:          Ticket,
		Title:         "Dependent Ticket",
		Content:       "Run second",
		Author:        "tester",
		LinkedIssueID: issue.ID,
		DependsOn:     []string{prereq.ID},
		RequestID:     "req-t-2",
	})
	if err != nil {
		t.Fatalf("CreateItem(blocked): %v", err)
	}
	blockedFetched, err := repo.GetItem(ctx, blocked.ID)
	if err != nil {
		t.Fatalf("GetItem(blocked): %v", err)
	}
	if len(blockedFetched.WaitingOn) != 1 || blockedFetched.WaitingOn[0] != prereq.ID {
		t.Fatalf("WaitingOn = %v, want [%s]", blockedFetched.WaitingOn, prereq.ID)
	}

	// Board & queue checks.
	board, err := repo.ListBoardTickets(ctx, ws.ID, "active", "", 20)
	if err != nil || len(board.Items) != 2 {
		t.Fatalf("ListBoardTickets(active): count=%d err=%v", len(board.Items), err)
	}
	queue, err := repo.ListTicketQueue(ctx, ws.ID, "assignment", "", 20)
	if err != nil || len(queue.Items) != 1 || queue.Items[0].ID != prereq.ID {
		t.Fatalf("ListTicketQueue(assignment): items=%+v err=%v", queue.Items, err)
	}

	// Thread & post revisions.
	reply, err := repo.AddPost(ctx, prereq.ID, PostInput{
		Content:   "Initial reply",
		Author:    "tester",
		RequestID: "req-reply-1",
	})
	if err != nil {
		t.Fatalf("AddPost: %v", err)
	}
	thread, err := repo.ReadThread(ctx, prereq.ID, "", 10)
	if err != nil || len(thread.Posts) != 2 {
		t.Fatalf("ReadThread: posts=%d err=%v", len(thread.Posts), err)
	}
	if _, err := repo.UpdatePost(ctx, reply.ID, PostInput{
		Content:  "Updated reply",
		Author:   "tester",
		Revision: reply.Revision,
	}); err != nil {
		t.Fatalf("UpdatePost: %v", err)
	}

	prereq, err = repo.GetItem(ctx, prereq.ID)
	if err != nil {
		t.Fatalf("GetItem(prereq after post): %v", err)
	}
	// Worker claim, question/answer, review/accept lifecycle.
	members, err := repo.ListMembers(ctx, ws.ID)
	if err != nil || len(members) == 0 {
		t.Fatalf("ListMembers: %d err=%v", len(members), err)
	}
	member := members[0]
	attempt, err := repo.ClaimTicket(ctx, prereq.ID, ClaimInput{
		Revision:         prereq.Revision,
		MemberID:         member.ID,
		MemberRevision:   member.Revision,
		DefinitionDigest: "digest-v1",
		Owner:            "worker-1",
		RequestID:        "claim-1",
	})
	if err != nil {
		t.Fatalf("ClaimTicket: %v", err)
	}
	attempt, err = repo.UpdateAttempt(ctx, attempt.ID, AttemptUpdate{
		Owner:      "worker-1",
		Token:      attempt.Token,
		Generation: attempt.Generation,
		Status:     "running",
		WorkerKind: "native",
	})
	if err != nil {
		t.Fatalf("UpdateAttempt(running): %v", err)
	}
	load, err := repo.GetTicketLoad(ctx, ws.ID)
	if err != nil || load.Active != 1 {
		t.Fatalf("GetTicketLoad: %+v err=%v", load, err)
	}
	guidance, err := repo.QueueTicketGuidance(ctx, attempt.ID, TicketGuidanceInput{
		Content:   "Check edge cases",
		RequestID: "guide-1",
	})
	if err != nil {
		t.Fatalf("QueueTicketGuidance: %v", err)
	}
	attempt, err = repo.UpdateAttempt(ctx, attempt.ID, AttemptUpdate{
		Owner:       "worker-1",
		Token:       attempt.Token,
		Generation:  attempt.Generation,
		GuidanceAck: guidance.ID,
		Status:      "awaiting_review",
		Result:      json.RawMessage(`{"summary":"done"}`),
	})
	if err != nil {
		t.Fatalf("UpdateAttempt(awaiting_review): %v", err)
	}
	pendingInbox, err := repo.NextPendingInbox(ctx, ws.ID)
	if err != nil || len(pendingInbox) != 1 {
		t.Fatalf("NextPendingInbox: %+v err=%v", pendingInbox, err)
	}
	humanInbox, err := repo.HumanInbox(ctx, ws.ID)
	if err != nil || len(humanInbox.Entries) != 1 {
		t.Fatalf("HumanInbox: %+v err=%v", humanInbox, err)
	}
	if _, err := repo.ReviewAttempt(ctx, attempt.ID, ReviewInput{
		ManagerSessionID: ws.ManagerSessionID,
		Decision:         "accept",
		RequestID:        "review-1",
	}); err != nil {
		t.Fatalf("ReviewAttempt(accept): %v", err)
	}
	if err := repo.AcknowledgeInbox(ctx, pendingInbox[0].ID, ws.ManagerSessionID); err != nil {
		t.Fatalf("AcknowledgeInbox: %v", err)
	}

	// Prerequisite done -> dependent ticket now unblocked in assignment queue.
	unblocked, err := repo.GetItem(ctx, blocked.ID)
	if err != nil {
		t.Fatalf("GetItem(blocked): %v", err)
	}
	if len(unblocked.WaitingOn) != 0 {
		t.Fatalf("expected unblocked WaitingOn empty, got %v", unblocked.WaitingOn)
	}
	queue2, err := repo.ListTicketQueue(ctx, ws.ID, "assignment", "", 20)
	if err != nil || len(queue2.Items) != 1 || queue2.Items[0].ID != blocked.ID {
		t.Fatalf("ListTicketQueue after prereq done: %+v err=%v", queue2.Items, err)
	}

	// Recurring schedule dispatch.
	past := time.Now().UTC().Add(-2 * time.Minute)
	if _, err := repo.CreateItem(ctx, CreateInput{
		WorkspaceID: ws.ID,
		Kind:        Ticket,
		Title:       "Scheduled Ticket",
		Content:     "Run periodically",
		Author:      "tester",
		Schedule:    &TicketSchedule{NotBefore: past, Every: "1m"},
		RequestID:   "req-sched-1",
	}); err != nil {
		t.Fatalf("CreateItem(scheduled): %v", err)
	}
	dispatched, err := repo.DispatchTicketSchedules(ctx, ws.ID, time.Now().UTC())
	if err != nil || len(dispatched.Items) != 1 {
		t.Fatalf("DispatchTicketSchedules: items=%d err=%v", len(dispatched.Items), err)
	}

	// Outbox & projection receipt.
	latest, err := repo.LatestChange(ctx)
	if err != nil || latest == 0 {
		t.Fatalf("LatestChange: %d err=%v", latest, err)
	}
	changes, err := repo.Changes(ctx, 0, 50)
	if err != nil || len(changes) == 0 {
		t.Fatalf("Changes: %d err=%v", len(changes), err)
	}
	if err := repo.ApplyReceipt(ctx, "pg-test-proj", latest); err != nil {
		t.Fatalf("ApplyReceipt: %v", err)
	}
	applied, err := repo.Applied(ctx, "pg-test-proj")
	if err != nil || applied != latest {
		t.Fatalf("Applied = %d, want %d (err=%v)", applied, latest, err)
	}
}
