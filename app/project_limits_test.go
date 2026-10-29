package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
)

func TestProjectDelegationBudgetWaitsCancelsAndSeparatesParents(t *testing.T) {
	var limit atomic.Int32
	limit.Store(2)
	checks := make(chan struct{}, 32)
	d := &delegationDispatcher{parallel: func(context.Context) (int, error) {
		select {
		case checks <- struct{}{}:
		default:
		}
		return int(limit.Load()), nil
	}}
	first, err := d.acquireDelegation(t.Context(), "parent")
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.acquireDelegation(t.Context(), "parent")
	if err != nil {
		t.Fatal(err)
	}
	limit.Store(1)
	// A nested child uses its own budget even while its parent holds a slot.
	child := &delegationDispatcher{coordinator: d}
	nested, err := child.acquireDelegation(t.Context(), "child")
	if err != nil {
		t.Fatal(err)
	}
	nested()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	for len(checks) > 0 {
		<-checks
	}
	go func() {
		release, err := child.acquireDelegation(ctx, "parent")
		if err == nil {
			release()
		}
		result <- err
	}()
	select {
	case <-checks:
	case <-time.After(time.Second):
		t.Fatal("admission did not check current limit")
	}
	first() // One still-active call occupies the lowered budget.
	select {
	case err := <-result:
		t.Fatalf("admitted above lowered budget: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait ignored cancellation")
	}
	// Raising the limit wakes a queued call through periodic settings reload.
	admitted := make(chan func(), 1)
	go func() {
		release, err := child.acquireDelegation(t.Context(), "parent")
		if err != nil {
			result <- err
			return
		}
		admitted <- release
	}()
	limit.Store(2)
	select {
	case release := <-admitted:
		release()
	case <-time.After(2 * time.Second):
		t.Fatal("raised budget was not observed")
	}
	second()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.active) != 0 {
		t.Fatalf("leaked admissions: %+v", d.active)
	}
}

func TestProjectDelegationLimitReloadsConfigAndLibrary(t *testing.T) {
	var projectLimit atomic.Int32
	projectLimit.Store(2)
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(library.WorkspaceList{Workspaces: []workitems.Workspace{{ID: "project", WorkspaceRoots: []string{root}, Limits: workitems.ProjectLimits{MaxParallelSubagents: int(projectLimit.Load())}}}})
	}))
	defer server.Close()
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Agents.MaxParallel = 4
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	m := newModel(t.Context(), store, nil)
	m.libraryClient = library.NewClient(server.URL+"/v1", "", time.Second)
	resolve := m.projectDelegationLimit(executionWorkspace{workspaceStateRoot: root})
	if n, err := resolve(t.Context()); err != nil || n != 2 {
		t.Fatalf("root lookup: %d %v", n, err)
	}
	m.studioWorkspaceContext = &SessionWorkspaceContext{ProjectID: "project"}
	resolve = m.projectDelegationLimit(executionWorkspace{workspaceStateRoot: t.TempDir()})
	projectLimit.Store(8)
	if n, err := resolve(t.Context()); err != nil || n != 4 {
		t.Fatalf("project cap: %d %v", n, err)
	}
	value.Agents.MaxParallel = 1
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	if n, err := resolve(t.Context()); err != nil || n != 1 {
		t.Fatalf("global reload: %d %v", n, err)
	}
	value.Agents.MaxParallel = 4
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	projectLimit.Store(0)
	if n, err := resolve(t.Context()); err != nil || n != 4 {
		t.Fatalf("inherit: %d %v", n, err)
	}
}
