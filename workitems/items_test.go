package workitems

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIssuingTicketsQueuesAssignmentInTheSourceTransaction(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := t.Context()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Auto assignment", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	in := CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Investigate", Content: "Collect evidence", Author: "user", RequestID: "issue-ticket"}
	ticket, err := s.CreateItem(ctx, in)
	if err != nil || ticket.State != "todo" || ticket.ExecutionState != "queued" || ticket.Revision != 1 || ticket.ActiveAttemptID != "" || ticket.AssignedMemberID != "" {
		t.Fatalf("automatic admission: %+v %v", ticket, err)
	}
	body, err := s.GetPost(ctx, ticket.BodyPostID, 0)
	if err != nil || body.Content != in.Content {
		t.Fatalf("body was not committed with admission: %+v %v", body, err)
	}
	compat, err := s.ControlTicket(ctx, ticket.ID, TicketAction{Action: "queue", Revision: ticket.Revision})
	if err != nil || compat.Revision != ticket.Revision {
		t.Fatalf("legacy start altered an already admitted ticket: %+v %v", compat, err)
	}
	stopped, err := s.ControlTicket(ctx, ticket.ID, TicketAction{Action: "cancel", Revision: ticket.Revision})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := s.CreateItem(ctx, in)
	if err != nil || ack.ID != ticket.ID || ack.ExecutionState != "queued" {
		t.Fatalf("creation receipt changed: %+v %v", ack, err)
	}
	current, err := s.GetItem(ctx, ticket.ID)
	if err != nil || current.ExecutionState != "cancelled" || current.Revision != stopped.Revision {
		t.Fatalf("creation retry reactivated a stopped ticket: %+v %v", current, err)
	}
	issue, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Issue, Title: "Decision", Content: "Record evidence", Author: "user"})
	if err != nil || issue.State != "open" || issue.ExecutionState != "" {
		t.Fatalf("issue creation scheduled execution: %+v %v", issue, err)
	}
}

func TestIssueSourceReceiptsSnapshotsAndContentRevisions(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := t.Context()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Q", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	in := CreateInput{WorkspaceID: w.ID, Kind: Issue, Title: "Decision", Content: strings.Repeat("한글🙂", 1000), Author: "user", RequestID: "create-1"}
	item, err := s.CreateItem(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateItem(ctx, in)
	if err != nil || again.ID != item.ID {
		t.Fatalf("receipt replay: %+v %v", again, err)
	}
	in.Title = "Different"
	if _, err := s.CreateItem(ctx, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("key reused: %v", err)
	}
	for n := 0; n < 4; n++ {
		if _, err := s.AddPost(ctx, item.ID, PostInput{Content: fmt.Sprint(n), Author: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ReadThread(ctx, item.ID, "", 2)
	if err != nil || len(first.Posts) != 2 || !first.Posts[0].ContentTruncated {
		t.Fatalf("thread: %+v %v", first, err)
	}
	if _, err := s.AddPost(ctx, item.ID, PostInput{Content: "after snapshot", Author: "user"}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	count := 0
	page := first
	for {
		for _, p := range page.Posts {
			if seen[p.ID] {
				t.Fatal("duplicate post")
			}
			seen[p.ID] = true
			count++
		}
		if page.NextCursor == "" {
			break
		}
		page, err = s.ReadThread(ctx, item.ID, page.NextCursor, 2)
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != 5 {
		t.Fatalf("snapshot included new reply or omitted an old one: %d", count)
	}
	content, err := s.ReadContent(ctx, item.BodyPostID, "", 333)
	if err != nil {
		t.Fatal(err)
	}
	old := content.Post.Content
	if _, err := s.UpdatePost(ctx, item.BodyPostID, PostInput{Content: "replacement", Author: "user", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	for content.NextCursor != "" {
		content, err = s.ReadContent(ctx, item.BodyPostID, content.NextCursor, 333)
		if err != nil {
			t.Fatal(err)
		}
		if content.Post.Revision != 1 {
			t.Fatal("content cursor changed revision")
		}
		old += content.Post.Content
	}
	if old != in.Content {
		t.Fatal("content page split or skipped UTF-8 runes")
	}
	if _, err := s.UpdatePost(ctx, item.BodyPostID, PostInput{Content: "stale", Author: "user", Revision: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	if _, err := s.ReadContent(ctx, "different", first.NextCursor, 100); err == nil {
		t.Fatal("cross-resource cursor accepted")
	}
}

func TestConcurrentRepliesHaveDistinctMonotoneSequences(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := t.Context()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Q", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Issue, Title: "Parallel", Content: "body", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for n := range 16 {
		group.Go(func() {
			_, err := s.AddPost(ctx, item.ID, PostInput{Content: fmt.Sprint(n), Author: "agent"})
			if err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	thread, err := s.ReadThread(ctx, item.ID, "", 100)
	if err != nil || len(thread.Posts) != 17 {
		t.Fatalf("replies: %d %v", len(thread.Posts), err)
	}
	for n, p := range thread.Posts {
		if p.Seq != int64(n+1) {
			t.Fatalf("sequence: %d", p.Seq)
		}
	}
	changes, err := s.Changes(ctx, 0, 100)
	if err != nil || len(changes) != 34 {
		t.Fatalf("source/outbox not committed together: %d %v", len(changes), err)
	}
}

func TestIssueDeletionRequiresLinkedTicketsToEndAndPreventsTheirRetry(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := t.Context()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Issue lifecycle", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Issue, Title: "Plan", Content: "Evidence", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Linked work", Content: "Inspect", Author: "user", LinkedIssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	deleteIssue := UpdateInput{Revision: issue.Revision, Title: issue.Title, State: issue.State, Deleted: true}
	if _, err := s.UpdateItem(ctx, issue.ID, deleteIssue); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted issue with queued work: %v", err)
	}
	current, err := s.GetItem(ctx, issue.ID)
	if err != nil || current.Deleted || current.Revision != issue.Revision {
		t.Fatalf("rejected deletion changed issue: %+v %v", current, err)
	}
	ticket, err = s.ControlTicket(ctx, ticket.ID, TicketAction{Revision: ticket.Revision, Action: "cancel"})
	if err != nil {
		t.Fatal(err)
	}
	// Unrelated queued work does not prevent this issue's deletion.
	if _, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Unrelated", Content: "Inspect", Author: "user"}); err != nil {
		t.Fatal(err)
	}
	issue, err = s.UpdateItem(ctx, issue.ID, deleteIssue)
	if err != nil || !issue.Deleted {
		t.Fatalf("resolved ticket prevented deletion: %+v %v", issue, err)
	}
	if _, err := s.ControlTicket(ctx, ticket.ID, TicketAction{Revision: ticket.Revision, Action: "retry"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("retried work linked to deleted issue: %v", err)
	}
	ticket, err = s.UpdateItem(ctx, ticket.ID, UpdateInput{Revision: ticket.Revision, Title: ticket.Title, State: ticket.State})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ControlTicket(ctx, ticket.ID, TicketAction{Revision: ticket.Revision, Action: "retry"}); err != nil {
		t.Fatalf("unlinked work cannot retry: %v", err)
	}
}

func TestIssueDeletionRequiresRecurringTemplateAndItsRunsToEnd(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := t.Context()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Recurring lifecycle", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Issue, Title: "Plan", Content: "Evidence", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Repeat", Content: "Inspect", Author: "user", LinkedIssueID: issue.ID, Schedule: &TicketSchedule{Every: "1m", Paused: true}})
	if err != nil {
		t.Fatal(err)
	}
	deleteIssue := UpdateInput{Revision: issue.Revision, Title: issue.Title, State: issue.State, Deleted: true}
	if _, err := s.UpdateItem(ctx, issue.ID, deleteIssue); !errors.Is(err, ErrConflict) {
		t.Fatalf("paused template allowed issue deletion: %v", err)
	}
	parent, err = s.UpdateItem(ctx, parent.ID, UpdateInput{Revision: parent.Revision, Title: parent.Title, State: parent.State, LinkedIssueID: issue.ID, Schedule: &TicketSchedule{Every: "1m"}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.DispatchTicketSchedules(ctx, w.ID, time.Now())
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("dispatch: %+v %v", page, err)
	}
	parent, err = s.GetItem(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateItem(ctx, parent.ID, UpdateInput{Revision: parent.Revision, Title: parent.Title, State: parent.State, LinkedIssueID: issue.ID, Deleted: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateItem(ctx, issue.ID, deleteIssue); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted template hid its unresolved run: %v", err)
	}
	run := page.Items[0]
	if _, err := s.ControlTicket(ctx, run.ID, TicketAction{Revision: run.Revision, Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateItem(ctx, issue.ID, deleteIssue); err != nil {
		t.Fatalf("ended schedule prevented deletion: %v", err)
	}
	page, err = s.DispatchTicketSchedules(ctx, w.ID, time.Now().Add(time.Hour))
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("ended schedule dispatched after issue deletion: %+v %v", page, err)
	}
}
