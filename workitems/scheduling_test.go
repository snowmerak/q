package workitems

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSchedulingQueuesUseIndexesAndFollowPayloadTransitions(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	p, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Queue", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Other", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	makeItem := func(workspace, kind, state string, deleted bool, next time.Time) Item {
		t.Helper()
		item, err := s.CreateItem(ctx, CreateInput{WorkspaceID: workspace, Kind: kind, Title: "Work", Content: "Evidence", Author: "user"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.db.ExecContext(ctx, `UPDATE items SET payload=json_set(payload,'$.execution_state',?,'$.deleted',json(?),'$.next_assignment_at',?) WHERE id=?`, state, strconv.FormatBool(deleted), stamp(next), item.ID)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	var expected []string
	for _, state := range []string{"queued", "assignment_waiting", ""} {
		item := makeItem(p.ID, Ticket, state, false, time.Now().Add(-time.Minute))
		expected = append(expected, item.ID)
		if state == "" {
			if _, err := s.db.ExecContext(ctx, `UPDATE items SET payload=json_remove(payload,'$.execution_state') WHERE id=?`, item.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, state := range []string{"reserved", "running", "stopping", "awaiting_review", "blocked", "cancelled", "unknown"} {
		makeItem(p.ID, Ticket, state, false, time.Time{})
	}
	for _, state := range []string{"queued", "assignment_waiting"} {
		makeItem(p.ID, Ticket, state, false, time.Now().Add(time.Hour))
	}
	makeItem(p.ID, Ticket, "queued", true, time.Time{})
	makeItem(other.ID, Ticket, "queued", false, time.Time{})
	makeItem(p.ID, Issue, "", false, time.Time{})
	legacyActive := makeItem(p.ID, Ticket, "", false, time.Time{})
	if _, err := s.db.ExecContext(ctx, `UPDATE items SET payload=json_set(payload,'$.active_attempt_id','old-worker') WHERE id=?`, legacyActive.ID); err != nil {
		t.Fatal(err)
	}
	completed := makeItem(p.ID, Ticket, "", false, time.Time{})
	// A completed historical payload need not be deserialized by the scheduler.
	if _, err := s.db.ExecContext(ctx, `UPDATE items SET payload=json_set(payload,'$.state','done','$.created_at','historical-invalid-time') WHERE id=?`, completed.ID); err != nil {
		t.Fatal(err)
	}
	merge := makeItem(p.ID, Ticket, "merge_queued", false, time.Time{})
	closeItem := makeItem(p.ID, Ticket, "close_queued", false, time.Time{})
	// Simulate upgrading an existing catalog: the indexes are backfilled at open.
	if _, err := s.db.ExecContext(ctx, `DROP INDEX ticket_assignment_queue; DROP INDEX ticket_merge_queue; DROP INDEX attempts_active; DROP INDEX attempts_lease_expiry; DROP INDEX inbox_pending;`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListTicketQueue(ctx, p.ID, "assignment", "", 2)
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != expected[0] || page.Items[1].ID != expected[1] || page.NextCursor == "" {
		t.Fatalf("first queue page: %+v %v", page, err)
	}
	if _, err := s.ListTicketQueue(ctx, other.ID, "assignment", page.NextCursor, 2); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-project cursor: %v", err)
	}
	if _, err := s.ListTicketQueue(ctx, p.ID, "merge", page.NextCursor, 2); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-queue cursor: %v", err)
	}
	last, err := s.ListTicketQueue(ctx, p.ID, "assignment", page.NextCursor, 2)
	if err != nil || len(last.Items) != 1 || last.Items[0].ID != expected[2] || last.NextCursor != "" {
		t.Fatalf("last queue page: %+v %v", last, err)
	}
	merges, err := s.ListTicketQueue(ctx, p.ID, "merge", "", 100)
	if err != nil || len(merges.Items) != 2 || merges.Items[0].ID != merge.ID || merges.Items[1].ID != closeItem.ID {
		t.Fatalf("merge queue: %+v %v", merges, err)
	}
	// Existing mutation paths automatically remove/reinsert partial-index entries.
	item, err := s.GetItem(ctx, expected[0])
	if err != nil {
		t.Fatal(err)
	}
	item, err = s.ControlTicket(ctx, item.ID, TicketAction{Action: "cancel", Revision: item.Revision})
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := s.ListTicketQueue(ctx, p.ID, "assignment", "", 100)
	if err != nil || len(filtered.Items) != 2 {
		t.Fatalf("cancel left index membership: %+v %v", filtered, err)
	}
	if _, err = s.ControlTicket(ctx, item.ID, TicketAction{Action: "retry", Revision: item.Revision}); err != nil {
		t.Fatal(err)
	}
	filtered, err = s.ListTicketQueue(ctx, p.ID, "assignment", "", 100)
	if err != nil || len(filtered.Items) != 3 {
		t.Fatalf("retry missing from index: %+v %v", filtered, err)
	}
	assertPlan := func(query, index string, args ...any) {
		t.Helper()
		rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
		if err != nil {
			t.Fatal(err)
		}
		var details []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			details = append(details, detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(details, "\n"), index) {
			t.Fatalf("query missed partial index %s: %v", index, details)
		}
	}
	assertPlan(`SELECT payload FROM items INDEXED BY ticket_assignment_queue WHERE `+assignmentQueue+` AND workspace_id=? AND seq>? AND seq<=? AND `+assignmentDue+`<=julianday(?) ORDER BY seq LIMIT 101`, "SEARCH items USING INDEX ticket_assignment_queue", p.ID, 0, 10000, stamp(time.Now()))
	assertPlan(`SELECT payload FROM items INDEXED BY ticket_merge_queue WHERE `+mergeQueue+` AND workspace_id=? AND seq>? ORDER BY seq LIMIT 101`, "SEARCH items USING INDEX ticket_merge_queue", p.ID, 0)
	assertPlan(`SELECT count(*) FROM ticket_attempts WHERE `+activeAttempts+` AND workspace_id=?`, "attempts_active", p.ID)
	assertPlan(`SELECT payload FROM ticket_attempts WHERE `+leasedAttempts+` AND julianday(json_extract(payload,'$.lease_until'))<julianday(?)`, "SEARCH ticket_attempts USING INDEX attempts_lease_expiry", stamp(time.Now()))
	assertPlan(`SELECT payload FROM manager_inbox_events WHERE `+pendingInbox+` AND workspace_id=? ORDER BY julianday(json_extract(payload,'$.created_at')),id LIMIT 1`, "SEARCH manager_inbox_events USING INDEX inbox_pending", p.ID)
}

func TestSchedulerLoadsPendingInboxAndExpiredRecovery(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := t.Context()
	root := t.TempDir()
	p, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Live", WorkspaceRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.PutMember(ctx, p.ID, MemberInput{AgentID: "builtin/research", DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	claim := func() Attempt {
		t.Helper()
		i, err := s.CreateItem(ctx, CreateInput{WorkspaceID: p.ID, Kind: Ticket, Title: "Research", Content: "Inspect", Author: "user"})
		if err != nil {
			t.Fatal(err)
		}
		a, err := s.ClaimTicket(ctx, i.ID, ClaimInput{Revision: i.Revision, MemberID: m.ID, MemberRevision: m.Revision, Owner: "host", DefinitionDigest: "profile", RequestID: i.ID})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a, b := claim(), claim()
	load, err := s.GetTicketLoad(ctx, p.ID)
	if err != nil || load.Active != 2 || load.Members[m.ID] != 2 {
		t.Fatalf("active loads: %+v %v", load, err)
	}
	complete := func(a Attempt) {
		t.Helper()
		if _, err := s.UpdateAttempt(ctx, a.ID, AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "awaiting_review", Result: []byte(`{"outcome":"succeeded"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	complete(a)
	load, err = s.GetTicketLoad(ctx, p.ID)
	if err != nil || load.Active != 1 || load.Members[m.ID] != 1 {
		t.Fatalf("completed occupied load: %+v %v", load, err)
	}
	inbox, err := s.NextPendingInbox(ctx, p.ID)
	if err != nil || len(inbox) != 1 || inbox[0].AttemptID != a.ID {
		t.Fatalf("pending inbox: %+v %v", inbox, err)
	}
	if err := s.DeferInbox(ctx, inbox[0].ID, p.ManagerSessionID, "busy"); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.NextPendingInbox(ctx, p.ID); err != nil || len(pending) != 0 {
		t.Fatalf("deferred inbox polled early: %+v %v", pending, err)
	}
	if err := s.AcknowledgeInbox(ctx, inbox[0].ID, p.ManagerSessionID); err != nil {
		t.Fatal(err)
	}
	// Completed history may have stale leases; it never enters recovery reads.
	if _, err := s.db.ExecContext(ctx, `UPDATE ticket_attempts SET payload=json_set(payload,'$.lease_until','old-invalid-time') WHERE id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE ticket_attempts SET payload=json_set(payload,'$.lease_until',?) WHERE id=?`, stamp(time.Now().Add(-time.Minute)), b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverExpired(ctx); err != nil {
		t.Fatal(err)
	}
	load, err = s.GetTicketLoad(ctx, p.ID)
	if err != nil || load.Active != 0 {
		t.Fatalf("expired occupied load: %+v %v", load, err)
	}
	inbox, err = s.NextPendingInbox(ctx, p.ID)
	if err != nil || len(inbox) != 1 || inbox[0].AttemptID != b.ID {
		t.Fatalf("expired notification: %+v %v", inbox, err)
	}
	if err := s.RecoverExpired(ctx); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListInbox(ctx, p.ID)
	if err != nil || len(all) != 2 {
		t.Fatalf("history or receipt changed: %+v %v", all, err)
	}
}
