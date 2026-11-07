package workitems

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestDelayedTicketEligibilityAndClaimCannotBeBypassed(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx, root := t.Context(), t.TempDir()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Delayed", WorkspaceRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.PutMember(ctx, w.ID, MemberInput{AgentID: "builtin/research", DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	in := CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Later", Content: "Collect sources", Author: "user", Schedule: &TicketSchedule{NotBefore: time.Now().Add(time.Hour)}}
	item, err := s.CreateItem(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	claim := func(item Item) error {
		_, err := s.ClaimTicket(ctx, item.ID, ClaimInput{Revision: item.Revision, MemberID: m.ID, MemberRevision: m.Revision, Owner: "host", DefinitionDigest: "profile", RequestID: "claim"})
		return err
	}
	queue, err := s.ListTicketQueue(ctx, w.ID, "assignment", "", 100)
	if err != nil || len(queue.Items) != 0 {
		t.Fatalf("future ticket entered queue: %+v %v", queue, err)
	}
	if err := claim(item); !errors.Is(err, ErrConflict) {
		t.Fatalf("early claim accepted: %v", err)
	}
	item, err = s.ControlTicket(ctx, item.ID, TicketAction{Action: "retry", Revision: item.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err := claim(item); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry bypassed start time: %v", err)
	}
	update := UpdateInput{Revision: item.Revision, Title: item.Title, State: item.State, Schedule: &TicketSchedule{NotBefore: time.Now().Add(-time.Minute), Paused: true}}
	item, err = s.UpdateItem(ctx, item.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	queue, err = s.ListTicketQueue(ctx, w.ID, "assignment", "", 100)
	if err != nil || len(queue.Items) != 0 {
		t.Fatalf("paused ticket entered queue: %+v %v", queue, err)
	}
	if err := claim(item); !errors.Is(err, ErrConflict) {
		t.Fatalf("paused claim accepted: %v", err)
	}
	update.Revision, update.Schedule.Paused = item.Revision, false
	item, err = s.UpdateItem(ctx, item.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	queue, err = s.ListTicketQueue(ctx, w.ID, "assignment", "", 100)
	if err != nil || len(queue.Items) != 1 || queue.Items[0].ID != item.ID {
		t.Fatalf("due ticket not queued: %+v %v", queue, err)
	}
	if err := claim(item); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	update.Revision, update.State, update.Schedule = current.Revision, current.State, &TicketSchedule{Every: "1h"}
	if _, err := s.UpdateItem(ctx, item.ID, update); !errors.Is(err, ErrConflict) {
		t.Fatalf("running ticket became a schedule: %v", err)
	}
}

func TestRecurringTicketCoalescesAtomicallyAndSurvivesRestart(t *testing.T) {
	dir, ctx := t.TempDir(), t.Context()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	root := t.TempDir()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Recurring", WorkspaceRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.PutMember(ctx, w.ID, MemberInput{AgentID: "builtin/research", DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Hour).Add(-10 * time.Hour)
	parent, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "News", Content: "Read sources", Labels: []string{"news"}, Acceptance: []string{"Include links"}, RequestedMemberID: m.ID, ExecutionMode: "report", Author: "user", Schedule: &TicketSchedule{NotBefore: start, Every: "1h"}})
	if err != nil || parent.ExecutionState != "scheduled" {
		t.Fatalf("schedule creation: %+v %v", parent, err)
	}
	queue, err := s.ListTicketQueue(ctx, w.ID, "assignment", "", 100)
	if err != nil || len(queue.Items) != 0 {
		t.Fatalf("template entered assignment: %+v %v", queue, err)
	}
	if _, err := s.ControlTicket(ctx, parent.ID, TicketAction{Action: "retry", Revision: parent.Revision}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("template could be started directly: %v", err)
	}
	now := start.Add(10*time.Hour + 30*time.Minute)
	var wg sync.WaitGroup
	results := make(chan ItemPage, 4)
	errorsCh := make(chan error, 4)
	for range 4 {
		wg.Go(func() { page, err := s.DispatchTicketSchedules(ctx, w.ID, now); results <- page; errorsCh <- err })
	}
	wg.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	var runs []Item
	for page := range results {
		runs = append(runs, page.Items...)
	}
	if len(runs) != 1 {
		t.Fatalf("concurrent dispatch created %d runs", len(runs))
	}
	first := runs[0]
	if first.Schedule != nil || first.ScheduleID != parent.ID || !first.ScheduledFor.Equal(start.Add(10*time.Hour)) || first.ExecutionState != "queued" || first.RequestedMemberID != m.ID || !reflect.DeepEqual(first.Acceptance, parent.Acceptance) {
		t.Fatalf("run snapshot: %+v", first)
	}
	body, err := s.GetPost(ctx, first.BodyPostID, 0)
	if err != nil || body.Content != "Read sources" || body.ItemID != first.ID {
		t.Fatalf("run body snapshot: %+v %v", body, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.DispatchTicketSchedules(ctx, w.ID, now)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("restart duplicated occurrence: %+v %v", page, err)
	}
	page, err = s.DispatchTicketSchedules(ctx, w.ID, start.Add(11*time.Hour+5*time.Minute))
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("overlapping run: %+v %v", page, err)
	}
	// Completing the child through the real review contract releases the next occurrence.
	a, err := s.ClaimTicket(ctx, first.ID, ClaimInput{Revision: first.Revision, MemberID: m.ID, MemberRevision: m.Revision, Owner: "host", DefinitionDigest: "profile", RequestID: "claim-first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateAttempt(ctx, a.ID, AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "awaiting_review", Result: []byte(`{"outcome":"succeeded"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewAttempt(ctx, a.ID, ReviewInput{ManagerSessionID: w.ManagerSessionID, Decision: "accept", RequestID: "accept-first"}); err != nil {
		t.Fatal(err)
	}
	parent, err = s.GetItem(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, err = s.GetPost(ctx, parent.BodyPostID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdatePost(ctx, body.ID, PostInput{Revision: body.Revision, Content: "Read the updated sources", Author: "user"}); err != nil {
		t.Fatal(err)
	}
	page, err = s.DispatchTicketSchedules(ctx, w.ID, start.Add(12*time.Hour))
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("next occurrence: %+v %v", page, err)
	}
	body, err = s.GetPost(ctx, page.Items[0].BodyPostID, 0)
	if err != nil || body.Content != "Read the updated sources" {
		t.Fatalf("updated template not copied: %+v %v", body, err)
	}
	original, err := s.GetPost(ctx, first.BodyPostID, 0)
	if err != nil || original.Content != "Read sources" {
		t.Fatalf("old run was rewritten: %+v %v", original, err)
	}
}

func TestScheduleEditsPauseClearValidationAndRollback(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := t.Context()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Edits", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	in := CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Repeat", Content: "Sources", Author: "user"}
	for _, every := range []string{"invalid", "0s", "-1h", "59s"} {
		in.Schedule = &TicketSchedule{Every: every}
		if _, err := s.CreateItem(ctx, in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("accepted %q: %v", every, err)
		}
	}
	in.Kind, in.Schedule = Issue, &TicketSchedule{Every: "1h"}
	if _, err := s.CreateItem(ctx, in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("scheduled issue: %v", err)
	}
	start := time.Now().UTC().Add(-time.Hour)
	in.Kind, in.Schedule = Ticket, &TicketSchedule{NotBefore: start, Every: "1m", Paused: true}
	parent, err := s.CreateItem(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.DispatchTicketSchedules(ctx, w.ID, time.Now())
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("paused dispatch: %+v %v", page, err)
	}
	// Omitting the field preserves the schedule for older clients.
	parent, err = s.UpdateItem(ctx, parent.ID, UpdateInput{Revision: parent.Revision, Title: parent.Title, State: parent.State})
	if err != nil || parent.Schedule == nil || !parent.Schedule.Paused {
		t.Fatalf("omission erased schedule: %+v %v", parent, err)
	}
	parent, err = s.UpdateItem(ctx, parent.ID, UpdateInput{Revision: parent.Revision, Title: parent.Title, State: parent.State, Schedule: &TicketSchedule{NotBefore: start, Every: "1m"}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := s.GetPost(ctx, parent.BodyPostID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdatePost(ctx, body.ID, PostInput{Revision: body.Revision, Deleted: true}); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetItem(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DispatchTicketSchedules(ctx, w.ID, time.Now()); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted body dispatched: %v", err)
	}
	after, err := s.GetItem(ctx, parent.ID)
	if err != nil || after.Revision != before.Revision || !after.NextScheduledAt.Equal(before.NextScheduledAt) {
		t.Fatalf("failed dispatch advanced schedule: %+v %v", after, err)
	}
	parent, err = s.UpdateItem(ctx, parent.ID, UpdateInput{Revision: after.Revision, Title: parent.Title, State: parent.State, Schedule: &TicketSchedule{}})
	if err != nil || parent.Schedule != nil || parent.ExecutionState != "queued" || !parent.NextScheduledAt.IsZero() {
		t.Fatalf("clear schedule: %+v %v", parent, err)
	}
}

func TestHistoricalScheduleRunCannotRetryBesideANewerRun(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := t.Context()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Retry exclusion", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Hour)
	parent, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Repeat", Content: "Inspect", Author: "user", Schedule: &TicketSchedule{NotBefore: start, Every: "1m"}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.DispatchTicketSchedules(ctx, w.ID, start)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("first occurrence: %+v %v", page, err)
	}
	first := page.Items[0]
	first, err = s.ControlTicket(ctx, first.ID, TicketAction{Action: "cancel", Revision: first.Revision})
	if err != nil {
		t.Fatal(err)
	}
	page, err = s.DispatchTicketSchedules(ctx, w.ID, start.Add(time.Minute))
	if err != nil || len(page.Items) != 1 || page.Items[0].ScheduleID != parent.ID {
		t.Fatalf("new occurrence: %+v %v", page, err)
	}
	if _, err := s.ControlTicket(ctx, first.ID, TicketAction{Action: "retry", Revision: first.Revision}); !errors.Is(err, ErrConflict) {
		t.Fatalf("old run retried beside newer run: %v", err)
	}
	second := page.Items[0]
	if _, err := s.ControlTicket(ctx, second.ID, TicketAction{Action: "cancel", Revision: second.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ControlTicket(ctx, first.ID, TicketAction{Action: "retry", Revision: first.Revision}); err != nil {
		t.Fatalf("resolved newer run blocked retry: %v", err)
	}
}
