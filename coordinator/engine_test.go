package coordinator

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/systemoneconfig"
	"github.com/snowmerak/q/workitems"
)

func TestScanAdmitsLegacyTicketsAndPreservesExplicitStops(t *testing.T) {
	ctx, directory := t.Context(), t.TempDir()
	source, err := workitems.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	project, err := source.CreateWorkspace(ctx, workitems.WorkspaceInput{Name: "Legacy tickets", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := source.CreateItem(ctx, workitems.CreateInput{WorkspaceID: project.ID, Kind: workitems.Ticket, Title: "Previously issued", Content: "Inspect", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := source.CreateItem(ctx, workitems.CreateInput{WorkspaceID: project.ID, Kind: workitems.Ticket, Title: "Explicitly stopped", Content: "Do not resume", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ControlTicket(ctx, stopped.ID, workitems.TicketAction{Action: "cancel", Revision: stopped.Revision}); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	// Prepare the pre-fix source snapshot before the Library leader opens it.
	db, err := sql.Open("sqlite", filepath.Join(directory, "library", workitems.FileName))
	if err != nil {
		t.Fatal(err)
	}
	_, updateErr := db.ExecContext(ctx, `UPDATE items SET payload=json_remove(payload,'$.execution_state') WHERE id=?`, legacy.ID)
	closeErr := db.Close()
	if updateErr != nil || closeErr != nil {
		t.Fatalf("legacy fixture: %v %v", updateErr, closeErr)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (library.ConfigStore{Dir: directory}).Save(library.Config{Host: "127.0.0.1", Port: port}); err != nil {
		t.Fatal(err)
	}
	runtime, err := library.Ensure(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	engine := &Engine{ctx: ctx, owner: "legacy-test", active: map[string]bool{}, options: EngineOptions{Directory: directory, Candidates: func(context.Context, workitems.Workspace, workitems.Item, []workitems.Member) ([]Candidate, error) {
		return nil, nil
	}}}
	defer engine.jobs.Wait()
	if err := engine.scan(ctx); err != nil {
		t.Fatal(err)
	}
	engine.jobs.Wait()
	current, err := runtime.Client().GetItem(ctx, legacy.ID)
	if err != nil || current.ExecutionState != "assignment_waiting" || current.ExecutionReason != "no eligible joined subagent" {
		t.Fatalf("legacy issue was not picked up automatically: %+v %v", current, err)
	}
	current, err = runtime.Client().GetItem(ctx, stopped.ID)
	if err != nil || current.ExecutionState != "cancelled" || current.ActiveAttemptID != "" {
		t.Fatalf("explicit stop was reactivated: %+v %v", current, err)
	}
}

func TestFullProjectBacklogDoesNotOccupyOtherProjectsWorkerSlots(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	source, err := workitems.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	makeProject := func(name string) (workitems.Workspace, workitems.Member) {
		t.Helper()
		root := t.TempDir()
		p, err := source.CreateWorkspace(ctx, workitems.WorkspaceInput{Name: name, WorkspaceRoots: []string{root}, Limits: &workitems.ProjectLimits{MaxParallelTickets: 1}})
		if err != nil {
			t.Fatal(err)
		}
		m, err := source.PutMember(ctx, p.ID, workitems.MemberInput{AgentID: "builtin/research", DefinitionRoot: root, ExecutionRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		return p, m
	}
	first, member := makeProject("A full")
	second, _ := makeProject("B available")
	issue := func(p workitems.Workspace) workitems.Item {
		t.Helper()
		item, err := source.CreateItem(ctx, workitems.CreateInput{WorkspaceID: p.ID, Kind: workitems.Ticket, Title: "Research", Content: "Inspect", Author: "user"})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	active := issue(first)
	if _, err := source.ClaimTicket(ctx, active.ID, workitems.ClaimInput{ParallelLimit: 2, Revision: active.Revision, MemberID: member.ID, MemberRevision: member.Revision, Owner: "live", DefinitionDigest: "profile", RequestID: "reserve"}); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		issue(first)
	}
	other := issue(second)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (library.ConfigStore{Dir: dir}).Save(library.Config{Host: "127.0.0.1", Port: port}); err != nil {
		t.Fatal(err)
	}
	runtime, err := library.Ensure(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	seen := make(chan string, 8)
	release := make(chan struct{})
	e := &Engine{ctx: ctx, owner: "scheduler", active: map[string]bool{active.ID: true}, options: EngineOptions{Directory: dir, Parallel: func() int { return 2 }, Candidates: func(ctx context.Context, p workitems.Workspace, _ workitems.Item, _ []workitems.Member) ([]Candidate, error) {
		seen <- p.ID
		if p.ID == first.ID {
			<-release
		}
		return nil, nil
	}}}
	defer func() { close(release); e.jobs.Wait() }()
	if err := e.scan(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-seen:
		if id != second.ID {
			t.Fatalf("full project consumed assignment slot: %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("available project ticket %s was starved", other.ID)
	}
}

func TestManagerQuestionsAndEmbeddingDoNotConsumeWorkerCapacity(t *testing.T) {
	e := &Engine{active: map[string]bool{}, options: EngineOptions{Parallel: func() int { return 1 }}}
	release := make(chan struct{})
	defer func() { close(release); e.jobs.Wait() }()
	started := make(chan string, 4)
	for _, key := range []string{"manager/workspace", "embeddings", "ticket-one", "ticket-two"} {
		e.launch(key, func() { started <- key; <-release })
	}
	seen := map[string]bool{}
	for range 3 {
		select {
		case key := <-started:
			seen[key] = true
		case <-time.After(5 * time.Second):
			t.Fatal("a manager question or embedding job stalled a worker")
		}
	}
	if !seen["manager/workspace"] || !seen["embeddings"] || !seen["ticket-one"] || seen["ticket-two"] {
		t.Fatalf("worker capacity was not independent: %v", seen)
	}
}

func TestEngineAssignmentResultInboxAndNoDuplicateExecution(t *testing.T) {
	ctx := t.Context()
	directory := t.TempDir()
	main := config.Default()
	main.Provider.Model = "fixture"
	if err := (config.Store{Dir: directory}).Save(main); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if err := (library.ConfigStore{Dir: directory}).Save(library.Config{Host: "127.0.0.1", Port: port}); err != nil {
		t.Fatal(err)
	}
	runtime, err := library.Ensure(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	c := runtime.Client()
	root := t.TempDir()
	project, err := c.CreateWorkspace(ctx, workitems.WorkspaceInput{Name: "Engine", WorkspaceRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	member, err := c.PutMember(ctx, project.ID, workitems.MemberInput{AgentID: "builtin/research", DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	decisions := 0
	decision := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decisions++
		if r.Header.Get("Idempotency-Key") == "" {
			t.Error("assignment has no receipt key")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"assignee": map[string]string{"type": "choice", "choice": member.ID}}})
	}))
	defer decision.Close()
	settings := systemoneconfig.Default()
	settings.Providers = []systemoneconfig.ProviderConfig{{ID: "fixture", URI: decision.URL + "/systemone"}}
	settings.DefaultModel = "fixture/choice"
	if err := (systemoneconfig.Store{Dir: directory}).Save(settings); err != nil {
		t.Fatal(err)
	}
	calls, deliveries := 0, 0
	engine := &Engine{ctx: ctx, owner: "engine-test", options: EngineOptions{Directory: directory, Candidates: func(_ context.Context, _ workitems.Workspace, _ workitems.Item, members []workitems.Member) ([]Candidate, error) {
		out := []Candidate{}
		for _, member := range members {
			if !member.Disabled {
				out = append(out, Candidate{Member: member, Definition: subagent.AgentDefinition{Info: subagent.DelegateInfo{Name: member.AgentID, Kind: "inner", Role: config.AgentRoleResearch}}, Digest: "profile-v1"})
			}
		}
		return out, nil
	}, Run: func(ctx context.Context, c *library.Client, _ workitems.Workspace, item workitems.Item, _ Candidate, a workitems.Attempt, _ string) (subagent.TaskResult, error) {
		calls++
		current, err := c.GetItem(ctx, item.ID)
		if err != nil || current.State != "in_progress" || current.ActiveAttemptID != a.ID || a.WorkerSessionID == project.ManagerSessionID {
			t.Errorf("worker reservation %+v %+v %v", current, a, err)
		}
		return subagent.TaskResult{Outcome: "succeeded", Summary: "Verified evidence", Report: "Result from the existing runner boundary"}, nil
	}, Deliver: func(_ context.Context, p workitems.Workspace, entry workitems.InboxEntry, a workitems.Attempt) error {
		deliveries++
		if entry.ManagerSessionID != p.ManagerSessionID || entry.OperationID == "" || a.Status != "awaiting_review" {
			t.Errorf("inbox binding %+v %+v", entry, a)
		}
		return nil
	}}}
	item, err := c.CreateItem(ctx, workitems.CreateInput{WorkspaceID: project.ID, Kind: workitems.Ticket, Title: "Inspect", Content: "Verify", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if item.ExecutionState != "queued" {
		t.Fatalf("issued ticket was not admitted automatically: %+v", item)
	}
	lock, err := engine.ticketLock(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.execute(project, item); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || decisions != 0 {
		t.Fatal("OS ticket lock did not prevent a concurrent reservation")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := engine.execute(project, item); err != nil {
		t.Fatal(err)
	}
	if err := engine.execute(project, item); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || decisions != 1 {
		t.Fatalf("duplicate execution: calls=%d decisions=%d", calls, decisions)
	}
	item, err = c.GetItem(ctx, item.ID)
	if err != nil || item.State != "in_progress" || item.ExecutionState != "awaiting_review" {
		t.Fatalf("worker completed ticket early %+v %v", item, err)
	}
	thread, err := c.ReadThread(ctx, item.ID, "", 20)
	if err != nil || len(thread.Posts) != 2 {
		t.Fatalf("result reply did not commit with attempt %+v %v", thread, err)
	}
	inbox, err := c.ListInbox(ctx, project.ID)
	if err != nil || len(inbox.Entries) != 1 {
		t.Fatalf("inbox %+v %v", inbox, err)
	}
	if err := engine.deliver(project, inbox.Entries[0]); err != nil {
		t.Fatal(err)
	}
	if err := engine.deliver(project, inbox.Entries[0]); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 {
		t.Fatal("inbox was delivered twice")
	}
	if _, err := c.ReviewAttempt(ctx, item.ActiveAttemptID, workitems.ReviewInput{ManagerSessionID: project.ManagerSessionID, Decision: "accept", RequestID: "review"}); err != nil {
		t.Fatal(err)
	}
	item, err = c.GetItem(ctx, item.ID)
	if err != nil || item.State != "done" {
		t.Fatalf("report acceptance %+v %v", item, err)
	}
	if _, err := c.PutMember(ctx, project.ID, workitems.MemberInput{ID: member.ID, AgentID: member.AgentID, DefinitionRoot: root, ExecutionRoot: root, Disabled: true, Revision: member.Revision}); err != nil {
		t.Fatal(err)
	}
	waiting, err := c.CreateItem(ctx, workitems.CreateInput{WorkspaceID: project.ID, Kind: workitems.Ticket, Title: "Wait", Content: "No suitable member", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.execute(project, waiting); err == nil {
		t.Fatal("missing candidate reported execution success")
	}
	waiting, err = c.GetItem(ctx, waiting.ID)
	if err != nil || waiting.State != "todo" || waiting.ExecutionState != "assignment_waiting" || calls != 1 {
		t.Fatalf("assignment fallback %+v %v calls=%d", waiting, err, calls)
	}
}
