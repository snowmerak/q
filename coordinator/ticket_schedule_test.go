package coordinator

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
)

func TestScanDispatchesRecurringTicketsAndHonorsStartAndPause(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if err := (library.ConfigStore{Dir: dir}).Save(library.Config{Host: "127.0.0.1", Port: port}); err != nil {
		t.Fatal(err)
	}
	runtime, err := library.Ensure(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	c := runtime.Client()
	p, err := c.CreateWorkspace(ctx, workitems.WorkspaceInput{Name: "Scheduled project", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	create := func(title string, schedule *workitems.TicketSchedule) workitems.Item {
		t.Helper()
		item, err := c.CreateItem(ctx, workitems.CreateInput{WorkspaceID: p.ID, Kind: workitems.Ticket, Title: title, Content: "Inspect sources", Author: "user", Schedule: schedule})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	parent := create("Periodic", &workitems.TicketSchedule{Every: "1h"})
	future := create("Future", &workitems.TicketSchedule{NotBefore: time.Now().Add(time.Hour)})
	paused := create("Paused", &workitems.TicketSchedule{Every: "1h", Paused: true})
	due := create("Due", &workitems.TicketSchedule{NotBefore: time.Now().Add(-time.Minute)})
	seen := make(chan workitems.Item, 10)
	e := &Engine{ctx: ctx, owner: "schedule-test", active: map[string]bool{}, options: EngineOptions{Directory: dir, Candidates: func(_ context.Context, _ workitems.Workspace, item workitems.Item, _ []workitems.Member) ([]Candidate, error) {
		seen <- item
		return nil, nil
	}}}
	if err := e.scan(ctx); err != nil {
		t.Fatal(err)
	}
	e.jobs.Wait()
	close(seen)
	foundDue, foundRun, n := false, false, 0
	for item := range seen {
		n++
		foundDue = foundDue || item.ID == due.ID
		foundRun = foundRun || item.ScheduleID == parent.ID
	}
	if n != 2 || !foundDue || !foundRun {
		t.Fatalf("assignment candidates: count=%d due=%v recurring=%v", n, foundDue, foundRun)
	}
	for _, id := range []string{future.ID, paused.ID, parent.ID} {
		item, err := c.GetItem(ctx, id)
		if err != nil || item.ActiveAttemptID != "" || item.ExecutionState == "assignment_waiting" {
			t.Fatalf("ineligible item was assigned: %+v %v", item, err)
		}
	}
}
