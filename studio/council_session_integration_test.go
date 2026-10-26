package studio

import (
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/snowmerak/q/council"
	"github.com/snowmerak/q/workspace"
)

// Only the provider is substituted. Nested participant addresses must survive
// the real SessionHost's prepare, cache, save and release paths across rounds.
func TestCouncilRealHostPersistsParticipantsInsideParent(t *testing.T) {
	fixture := newStudioIntegrationConfigured(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			studioTestModel(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"A considered answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}, nil)
	var c council.Council
	input := councilInput{
		StorageRoot: fixture.root, Name: "Nested native context", Scope: council.Independent, Rounds: 2,
		Members: []council.Seat{{Model: "default/test-model"}, {Model: "default/test-model-2"}}, Chair: council.Seat{Model: "default/test-model"},
	}
	if status := councilAPI(t, fixture.handler, http.MethodPost, "/api/v1/councils", input, &c); status != http.StatusCreated {
		t.Fatalf("create=%d %+v", status, c)
	}
	var turn council.Turn
	if status := councilAPI(t, fixture.handler, http.MethodPost, "/api/v1/councils/"+c.ID+"/turns", councilTurnInput{Prompt: "Refine this design"}, &turn); status != http.StatusAccepted {
		t.Fatalf("run=%d %+v", status, turn)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		councilAPI(t, fixture.handler, http.MethodGet, "/api/v1/councils/"+c.ID+"/runs/"+turn.ID, nil, &turn)
		if turn.Status == "completed" || turn.Status == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if turn.Status != "completed" || turn.Final != "A considered answer" {
		t.Fatalf("result=%+v", turn)
	}
	parent := workspace.Store{Root: fixture.root, SessionID: c.ID}
	for _, id := range append(turn.MemberSessions, turn.ChairSession) {
		child, err := parent.CouncilParticipant(id)
		if err != nil {
			t.Fatal(err)
		}
		value, err := child.Load()
		if err != nil || value.ID != id || value.Type != workspace.Interaction || len(value.Transcript) < 2 {
			t.Fatalf("native participant=%+v %v", value, err)
		}
		if _, err := os.Stat((workspace.Store{Root: fixture.root, SessionID: id}).Path()); !os.IsNotExist(err) {
			t.Fatalf("participant escaped parent: %s %v", id, err)
		}
	}
	entries, err := (workspace.Store{Root: fixture.root}).ListSessions()
	if err != nil || len(entries) != 2 {
		t.Fatalf("root sessions=%+v %v", entries, err)
	}
}
