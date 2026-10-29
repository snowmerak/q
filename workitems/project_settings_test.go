package workitems

import (
	"errors"
	"reflect"
	"testing"
)

func TestProjectDefaultsSnapshotAndAtomicCreation(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := t.Context()
	d, err := s.GetProjectDefaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d.AgentIDs = []string{"builtin/research", "global/developer"}
	d, err = s.UpdateProjectDefaults(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "First", WorkspaceRoots: []string{root}, Limits: &ProjectLimits{MaxParallelTickets: 1, MaxParallelSubagents: 2}})
	if err != nil {
		t.Fatal(err)
	}
	members, err := s.ListMembers(ctx, w.ID)
	if err != nil || len(members) != 2 {
		t.Fatalf("initial members: %+v %v", members, err)
	}
	for _, m := range members {
		if m.DefinitionRoot != root || m.ExecutionRoot != root || m.Disabled {
			t.Fatalf("binding: %+v", m)
		}
	}
	d.AgentIDs = []string{"builtin/research"}
	if _, err = s.UpdateProjectDefaults(ctx, d); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := s.ListMembers(ctx, w.ID)
	if !reflect.DeepEqual(members, unchanged) {
		t.Fatal("default update changed existing members")
	}
	empty := []MemberInput{}
	w2, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Empty", WorkspaceRoots: []string{t.TempDir()}, InitialMembers: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.ListMembers(ctx, w2.ID); err != nil || len(got) != 0 {
		t.Fatalf("explicit empty: %+v %v", got, err)
	}
	updated, err := s.UpdateWorkspace(ctx, w.ID, WorkspaceInput{Name: "Renamed", WorkspaceRoots: []string{root}})
	if err != nil || updated.Limits != w.Limits || updated.ManagerSessionID != w.ManagerSessionID || updated.CoordinatorSessionID != w.CoordinatorSessionID {
		t.Fatalf("old client update: %+v %v", updated, err)
	}
	for _, duplicate := range []bool{false, true} {
		root := t.TempDir()
		initial := []MemberInput{{AgentID: "builtin/research", DefinitionRoot: root, ExecutionRoot: root}}
		extra := initial[0]
		if !duplicate {
			extra.ExecutionRoot = t.TempDir()
		}
		initial = append(initial, extra)
		if _, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Rollback", WorkspaceRoots: []string{root}, InitialMembers: &initial}); err == nil {
			t.Fatal("invalid initial members succeeded")
		}
		if _, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: "Rollback", WorkspaceRoots: []string{root}, InitialMembers: &empty}); err != nil {
			t.Fatalf("failed creation left catalog rows: %v", err)
		}
		// Release the name for the next rollback case.
		all, _ := s.ListWorkspaces(ctx, false)
		for _, p := range all {
			if p.Name == "Rollback" {
				if err := s.ArchiveWorkspace(ctx, p.ID); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if _, err := s.UpdateProjectDefaults(ctx, d); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale defaults: %v", err)
	}
	for _, ids := range [][]string{{"workspace/local"}, {"global/"}, {"builtin/research", "builtin/research"}} {
		d.AgentIDs = ids
		if _, err := s.UpdateProjectDefaults(ctx, d); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid defaults: %v", err)
		}
	}
}

func TestProjectTicketCapacityAndHotChanges(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := t.Context()
	makeProject := func(name string) (Workspace, Member) {
		t.Helper()
		root := t.TempDir()
		w, err := s.CreateWorkspace(ctx, WorkspaceInput{Name: name, WorkspaceRoots: []string{root}, Limits: &ProjectLimits{MaxParallelTickets: 1}})
		if err != nil {
			t.Fatal(err)
		}
		m, err := s.PutMember(ctx, w.ID, MemberInput{AgentID: "builtin/research", DefinitionRoot: root, ExecutionRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		return w, m
	}
	newTicket := func(w Workspace) Item {
		t.Helper()
		item, err := s.CreateItem(ctx, CreateInput{WorkspaceID: w.ID, Kind: Ticket, Title: "Research", Content: "Find evidence", Author: "user"})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	claim := func(item Item, m Member, global int) (Attempt, error) {
		return s.ClaimTicket(ctx, item.ID, ClaimInput{ParallelLimit: global, Revision: item.Revision, MemberID: m.ID, MemberRevision: m.Revision, Owner: "host", DefinitionDigest: "profile", RequestID: item.ID})
	}
	a, ma := makeProject("A")
	b, mb := makeProject("B")
	first, second, other := newTicket(a), newTicket(a), newTicket(b)
	active, err := claim(first, ma, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claim(second, ma, 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("project cap: %v", err)
	}
	if _, err := claim(other, mb, 2); err != nil {
		t.Fatalf("other project blocked: %v", err)
	}
	if _, err := s.UpdateWorkspace(ctx, a.ID, WorkspaceInput{Name: a.Name, WorkspaceRoots: a.WorkspaceRoots, Limits: &ProjectLimits{MaxParallelTickets: 5}}); err != nil {
		t.Fatal(err)
	}
	if _, err := claim(second, ma, 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("global cap: %v", err)
	}
	if _, err := s.UpdateAttempt(ctx, active.ID, AttemptUpdate{Owner: active.Owner, Token: active.Token, Generation: active.Generation, Status: "awaiting_review", Result: []byte(`{"outcome":"succeeded"}`)}); err != nil {
		t.Fatal(err)
	}
	next, err := claim(second, ma, 2)
	if err != nil {
		t.Fatalf("review occupied capacity: %v", err)
	}
	if _, err := s.UpdateWorkspace(ctx, a.ID, WorkspaceInput{Name: a.Name, WorkspaceRoots: a.WorkspaceRoots, Limits: &ProjectLimits{MaxParallelTickets: 1}}); err != nil {
		t.Fatal(err)
	}
	preserved, err := s.GetAttempt(ctx, next.ID)
	if err != nil || preserved.Status != "reserved" || preserved.StopRequested {
		t.Fatalf("lowering interrupted active work: %+v %v", preserved, err)
	}
	if _, err := claim(newTicket(a), ma, 8); !errors.Is(err, ErrConflict) {
		t.Fatalf("hot lowered cap: %v", err)
	}
}

func TestProjectSettingsMigrationPreservesCatalog(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.CreateWorkspace(t.Context(), WorkspaceInput{Name: "Existing", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `DROP TABLE workspace_settings; DROP TABLE workspace_defaults; PRAGMA user_version=6;`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, dir)
	all, err := s.ListWorkspaces(t.Context(), false)
	if err != nil || len(all) != 1 || !reflect.DeepEqual(w, all[0]) {
		t.Fatalf("migration changed catalog: %+v %v", all, err)
	}
	var version int
	if err := s.db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("schema: %d %v", version, err)
	}
}
