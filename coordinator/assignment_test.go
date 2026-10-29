package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/systemoneconfig"
	"github.com/snowmerak/q/workitems"
)

func TestLongerAssignmentBudgetStillHonorsCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	store := systemoneconfig.Store{Dir: t.TempDir()}
	value := systemoneconfig.Default()
	value.Providers = []systemoneconfig.ProviderConfig{{ID: "local", URI: server.URL + "/systemone"}}
	value.DefaultModel = "local/choice"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := SelectMember(ctx, store, workitems.Item{ID: "ticket"}, "Inspect", []Candidate{{Member: workitems.Member{ID: "member"}}})
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("assignment did not reach the provider")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation was lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("assignment ignored cancellation while using the longer HTTP budget")
	}
}

func TestAssignmentRequiresDecisionAndRejectsOutOfSetAnswers(t *testing.T) {
	choice := "member-1"
	calls := 0
	key := ""
	var request systemone.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		key = r.Header.Get("Idempotency-Key")
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"assignee": map[string]string{"type": "choice", "choice": choice}}})
	}))
	defer server.Close()
	store := systemoneconfig.Store{Dir: t.TempDir()}
	value := systemoneconfig.Default()
	value.Providers = []systemoneconfig.ProviderConfig{{ID: "local", URI: server.URL + "/systemone"}}
	value.DefaultModel = "local/choice"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	ticket := workitems.Item{ID: "ticket", Title: "Inspect", Revision: 1}
	candidate := Candidate{Member: workitems.Member{ID: "member-1"}, Definition: subagent.AgentDefinition{Info: subagent.DelegateInfo{Name: "workspace/research", Description: "Investigate"}}, Digest: "v1"}
	selected, err := SelectMember(context.Background(), store, ticket, "Evidence", []Candidate{candidate})
	if err != nil || selected.Member.ID != "member-1" || calls != 1 || key == "" {
		t.Fatalf("choice %+v %v calls=%d key=%s", selected, err, calls, key)
	}
	criteria, ok := request.Questions["assignee"].Criteria.(map[string]any)
	if !ok || len(criteria) != 2 || criteria["none"] == nil {
		t.Fatalf("criteria %+v", request)
	}
	previousKey := key
	choice = "not-joined"
	if _, err := SelectMember(context.Background(), store, ticket, "Evidence", []Candidate{candidate}); err == nil {
		t.Fatal("out-of-set choice accepted")
	}
	if key != previousKey {
		t.Fatal("same decision input lost idempotency key")
	}
	choice = "none"
	if _, err := SelectMember(context.Background(), store, ticket, "Evidence", []Candidate{candidate}); err == nil {
		t.Fatal("no suitable member silently fell back")
	}
	missing := systemoneconfig.Store{Dir: t.TempDir()}
	if _, err := SelectMember(context.Background(), missing, ticket, "Evidence", []Candidate{candidate}); err == nil {
		t.Fatal("missing settings used an implicit assignment provider")
	}
}
