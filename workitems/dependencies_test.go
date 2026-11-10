package workitems

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func dependencyFixture(t *testing.T) (*Store, Workspace, Member) {
	t.Helper()
	s := openTestStore(t, t.TempDir())
	root := t.TempDir()
	w, err := s.CreateWorkspace(t.Context(), WorkspaceInput{Name: "Dependencies", WorkspaceRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.PutMember(t.Context(), w.ID, MemberInput{AgentID: "builtin/research", DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	return s, w, m
}

func dependencyTicket(t *testing.T, s *Store, w Workspace, title string, deps ...string) Item {
	t.Helper()
	item, err := s.CreateItem(t.Context(), CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: title, Content: "Inspect the source", Author: "user", DependsOn: deps})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func dependencyClaim(t *testing.T, s *Store, m Member, id string) Attempt {
	t.Helper()
	item, err := s.GetItem(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.ClaimTicket(t.Context(), id, ClaimInput{Revision: item.Revision, MemberID: m.ID, MemberRevision: m.Revision, DefinitionDigest: "profile", Owner: "host", RequestID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func dependencyResult(t *testing.T, s *Store, a Attempt, status string) {
	t.Helper()
	if _, err := s.UpdateAttempt(t.Context(), a.ID, AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: status, Result: []byte(`{"outcome":"succeeded"}`)}); err != nil {
		t.Fatal(err)
	}
}

func dependencyReview(t *testing.T, s *Store, w Workspace, a Attempt, decision string) {
	t.Helper()
	if _, err := s.ReviewAttempt(t.Context(), a.ID, ReviewInput{ManagerSessionID: w.ManagerSessionID, Decision: decision, RequestID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
}

func TestDependencyGraphReleasesOnlyAfterAllPrerequisitesAreDone(t *testing.T) {
	s, w, m := dependencyFixture(t)
	a := dependencyTicket(t, s, w, "Plan")
	b := dependencyTicket(t, s, w, "Implementation one", a.ID)
	c := dependencyTicket(t, s, w, "Implementation two", a.ID)
	d := dependencyTicket(t, s, w, "Integrate", b.ID, c.ID)
	e := dependencyTicket(t, s, w, "Independent")
	assertQueue := func(want ...string) {
		t.Helper()
		page, err := s.ListTicketQueue(t.Context(), w.ID, "assignment", "", 100)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, item := range page.Items {
			got = append(got, item.ID)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("ready queue = %v; want %v", got, want)
		}
	}
	assertQueue(a.ID, e.ID)
	if _, err := s.ClaimTicket(t.Context(), b.ID, ClaimInput{Revision: b.Revision, MemberID: m.ID, MemberRevision: m.Revision, DefinitionDigest: "profile", Owner: "bypass", RequestID: "bypass"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("direct claim bypassed dependencies: %v", err)
	}
	plan := dependencyClaim(t, s, m, a.ID)
	dependencyResult(t, s, plan, "awaiting_review")
	assertQueue(e.ID)
	dependencyReview(t, s, w, plan, "accept")
	assertQueue(b.ID, c.ID, e.ID)
	first, second := dependencyClaim(t, s, m, b.ID), dependencyClaim(t, s, m, c.ID)
	dependencyResult(t, s, first, "awaiting_review")
	dependencyReview(t, s, w, first, "accept")
	current, err := s.GetItem(t.Context(), d.ID)
	if err != nil || !slices.Equal(current.WaitingOn, []string{c.ID}) {
		t.Fatalf("join did not retain unmet prerequisite: %+v %v", current, err)
	}
	dependencyResult(t, s, second, "awaiting_review")
	assertQueue(e.ID)
	dependencyReview(t, s, w, second, "accept")
	assertQueue(d.ID, e.ID)
	current, err = s.GetItem(t.Context(), d.ID)
	if err != nil || len(current.WaitingOn) != 0 || current.Revision != d.Revision {
		t.Fatalf("readiness changed source revision: %+v %v", current, err)
	}
}

func TestDependenciesRemainBlockedByUnsuccessfulPrerequisites(t *testing.T) {
	for _, status := range []string{"cancelled", "rejected", "blocked", "failed", "unknown"} {
		t.Run(status, func(t *testing.T) {
			s, w, m := dependencyFixture(t)
			a := dependencyTicket(t, s, w, "Prerequisite")
			b := dependencyTicket(t, s, w, "Dependent", a.ID)
			attempt := dependencyClaim(t, s, m, a.ID)
			if status == "rejected" {
				dependencyResult(t, s, attempt, "awaiting_review")
				dependencyReview(t, s, w, attempt, "reject")
			} else {
				dependencyResult(t, s, attempt, status)
			}
			page, err := s.ListTicketQueue(t.Context(), w.ID, "assignment", "", 100)
			if err != nil || len(page.Items) != 0 {
				t.Fatalf("%s released dependent: %+v %v", status, page, err)
			}
			current, err := s.GetItem(t.Context(), b.ID)
			if err != nil || !slices.Equal(current.WaitingOn, []string{a.ID}) {
				t.Fatalf("waiting reason lost: %+v %v", current, err)
			}
			if _, err := s.ClaimTicket(t.Context(), b.ID, ClaimInput{Revision: b.Revision, MemberID: m.ID, MemberRevision: m.Revision, DefinitionDigest: "profile", Owner: "bypass", RequestID: "bypass"}); !errors.Is(err, ErrConflict) {
				t.Fatalf("%s bypassed by claim: %v", status, err)
			}
			a, err = s.GetItem(t.Context(), a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ControlTicket(t.Context(), a.ID, TicketAction{Revision: a.Revision, Action: "retry"}); err != nil {
				t.Fatal(err)
			}
			retry := dependencyClaim(t, s, m, a.ID)
			dependencyResult(t, s, retry, "awaiting_review")
			dependencyReview(t, s, w, retry, "accept")
			page, err = s.ListTicketQueue(t.Context(), w.ID, "assignment", "", 100)
			if err != nil || len(page.Items) != 1 || page.Items[0].ID != b.ID {
				t.Fatalf("successful retry did not release dependent: %+v %v", page, err)
			}
		})
	}
}

func TestDependencyEditsValidateGraphAndPreserveOmittedFields(t *testing.T) {
	s, w, m := dependencyFixture(t)
	a := dependencyTicket(t, s, w, "A")
	b := dependencyTicket(t, s, w, "B", a.ID)
	c := dependencyTicket(t, s, w, "C", b.ID)
	update := func(item Item, ids *[]string) (Item, error) {
		return s.UpdateItem(t.Context(), item.ID, UpdateInput{Revision: item.Revision, Title: item.Title, State: item.State, DependsOn: ids})
	}
	for _, ids := range [][]string{{a.ID}, {c.ID}, {b.ID, b.ID}, {uuid.NewString()}} {
		if _, err := update(a, &ids); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid graph %v accepted: %v", ids, err)
		}
	}
	b, err := update(b, nil)
	if err != nil || !slices.Equal(b.DependsOn, []string{a.ID}) {
		t.Fatalf("omitted field erased edges: %+v %v", b, err)
	}
	ids := []string{}
	b, err = update(b, &ids)
	if err != nil || len(b.DependsOn) != 0 {
		t.Fatalf("explicit clear failed: %+v %v", b, err)
	}
	ids = []string{c.ID}
	a, err = update(a, &ids)
	if err != nil {
		t.Fatalf("valid reordered graph rejected: %v", err)
	}
	// A newer prerequisite must be admitted ahead of an older dependent.
	page, err := s.ListTicketQueue(t.Context(), w.ID, "assignment", "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != b.ID {
		t.Fatalf("queue used creation order instead of readiness: %+v %v", page, err)
	}
	_ = dependencyClaim(t, s, m, b.ID)
	b, err = s.GetItem(t.Context(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids = []string{a.ID}
	if _, err := update(b, &ids); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed running work contract: %v", err)
	}
}

func TestDependencyReferencesCannotDisappearOrBecomeRecurringTemplates(t *testing.T) {
	s, w, m := dependencyFixture(t)
	a := dependencyTicket(t, s, w, "A")
	_ = dependencyTicket(t, s, w, "B", a.ID)
	for _, update := range []UpdateInput{
		{Revision: a.Revision, Title: a.Title, State: a.State, Deleted: true},
		{Revision: a.Revision, Title: a.Title, State: a.State, Schedule: &TicketSchedule{Every: "1h"}},
	} {
		if _, err := s.UpdateItem(t.Context(), a.ID, update); !errors.Is(err, ErrConflict) {
			t.Fatalf("invalidated prerequisite: %v", err)
		}
	}
	attempt := dependencyClaim(t, s, m, a.ID)
	dependencyResult(t, s, attempt, "awaiting_review")
	dependencyReview(t, s, w, attempt, "accept")
	a, err := s.GetItem(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateItem(t.Context(), a.ID, UpdateInput{Revision: a.Revision, Title: a.Title, State: a.State, Deleted: true}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListTicketQueue(t.Context(), w.ID, "assignment", "", 100)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("historical completed prerequisite stopped satisfying edge: %+v %v", page, err)
	}
}

func TestRecurringRunsInheritDependenciesAndCannotChangeThem(t *testing.T) {
	s, w, _ := dependencyFixture(t)
	a := dependencyTicket(t, s, w, "A")
	parent, err := s.CreateItem(t.Context(), CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Repeat", Content: "Inspect", Author: "user", DependsOn: []string{a.ID}, Schedule: &TicketSchedule{Every: "1h"}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.DispatchTicketSchedules(t.Context(), w.ID, time.Now())
	if err != nil || len(page.Items) != 1 || !slices.Equal(page.Items[0].DependsOn, []string{a.ID}) {
		t.Fatalf("schedule lost dependencies: %+v %v", page, err)
	}
	run := page.Items[0]
	ids := []string{}
	if _, err := s.UpdateItem(t.Context(), run.ID, UpdateInput{Revision: run.Revision, Title: run.Title, State: run.State, DependsOn: &ids}); !errors.Is(err, ErrConflict) {
		t.Fatalf("run contract changed: %v", err)
	}
	if _, err := s.CreateItem(t.Context(), CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Invalid", Content: "Inspect", Author: "user", DependsOn: []string{parent.ID}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("used unfinishable template: %v", err)
	}
	ready, err := s.ListTicketQueue(t.Context(), w.ID, "assignment", "", 100)
	if err != nil || len(ready.Items) != 1 || ready.Items[0].ID != a.ID {
		t.Fatalf("run bypassed dependency: %+v %v", ready, err)
	}
}

func TestDependencyCatalogSurvivesVersionEightUpgradeAndReopen(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Upgrade", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	a := dependencyTicket(t, s, w, "Legacy")
	if _, err := s.db.ExecContext(ctx, `DROP TABLE ticket_dependencies; PRAGMA user_version=8`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	b := dependencyTicket(t, s, w, "Dependent", a.ID)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, dir)
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != 9 {
		t.Fatalf("version: %d %v", version, err)
	}
	current, err := s.GetItem(ctx, b.ID)
	if err != nil || !slices.Equal(current.DependsOn, []string{a.ID}) || !slices.Equal(current.WaitingOn, []string{a.ID}) {
		t.Fatalf("lost durable graph: %+v %v", current, err)
	}
	page, err := s.ListTicketQueue(ctx, w.ID, "assignment", "", 100)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != a.ID {
		t.Fatalf("reopen changed readiness: %+v %v", page, err)
	}
}

func TestInvalidDependenciesDoNotCommitSourceOrOutboxChanges(t *testing.T) {
	s, w, _ := dependencyFixture(t)
	other, err := s.CreateWorkspace(t.Context(), WorkspaceInput{Name: "Other", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	foreign := dependencyTicket(t, s, other, "Foreign")
	issue, err := s.CreateItem(t.Context(), CreateInput{WorkspaceID: w.ID, Kind: Issue, Title: "Issue", Content: "Evidence", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	deleted := dependencyTicket(t, s, w, "Deleted")
	if _, err := s.UpdateItem(t.Context(), deleted.ID, UpdateInput{Revision: deleted.Revision, Title: deleted.Title, State: deleted.State, Deleted: true}); err != nil {
		t.Fatal(err)
	}
	before, err := s.LatestChange(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{{foreign.ID}, {issue.ID}, {deleted.ID}, {"invalid"}, make([]string, 65)} {
		if _, err := s.CreateItem(t.Context(), CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Invalid", Content: "Inspect", Author: "user", DependsOn: ids}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid dependency accepted: %v", err)
		}
	}
	after, err := s.LatestChange(t.Context())
	if err != nil || after != before {
		t.Fatalf("rejected graph committed partial source/outbox: %d -> %d %v", before, after, err)
	}
}

func TestConcurrentDependencyEditsCannotCommitACycle(t *testing.T) {
	s, w, _ := dependencyFixture(t)
	a, b := dependencyTicket(t, s, w, "A"), dependencyTicket(t, s, w, "B")
	start := make(chan struct{})
	results := make(chan error, 2)
	var writers sync.WaitGroup
	for _, edge := range [][2]Item{{a, b}, {b, a}} {
		writers.Go(func() {
			<-start
			ids := []string{edge[1].ID}
			_, err := s.UpdateItem(t.Context(), edge[0].ID, UpdateInput{Revision: edge[0].Revision, Title: edge[0].Title, State: edge[0].State, DependsOn: &ids})
			results <- err
		})
	}
	close(start)
	writers.Wait()
	close(results)
	committed, rejected := 0, 0
	for err := range results {
		if err == nil {
			committed++
		} else if errors.Is(err, ErrInvalidInput) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if committed != 1 || rejected != 1 {
		t.Fatalf("concurrent cycle: committed=%d rejected=%d", committed, rejected)
	}
}

func TestDependencyWaitsDoNotHideReadyTicketsBehindThePageLimit(t *testing.T) {
	s, w, _ := dependencyFixture(t)
	parent, err := s.CreateItem(t.Context(), CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Paused prerequisite", Content: "Inspect", Author: "user", Schedule: &TicketSchedule{Paused: true}})
	if err != nil {
		t.Fatal(err)
	}
	for range 101 {
		_ = dependencyTicket(t, s, w, "Waiting", parent.ID)
	}
	ready := dependencyTicket(t, s, w, "Independent")
	page, err := s.ListTicketQueue(t.Context(), w.ID, "assignment", "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != ready.ID || page.NextCursor != "" {
		t.Fatalf("waiting history hid eligible work: %+v %v", page, err)
	}
}
