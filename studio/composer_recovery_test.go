package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/snowmerak/q/composer"
	"github.com/snowmerak/q/workspace"
)

func TestComposerRecoveryReconcilesNativeCompletion(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation *workspace.SessionOperation
		status    string
		answer    string
	}{
		{"completed", &workspace.SessionOperation{ID: "current-turn", Prompt: "Explore this idea", Completed: true, Result: "저장된 최종 답변"}, "completed", "저장된 최종 답변"},
		{"pending", &workspace.SessionOperation{ID: "current-turn", Prompt: "Explore this idea"}, "interrupted", "Partial answer"},
		{"different turn", &workspace.SessionOperation{ID: "another-turn", Prompt: "Explore this idea", Completed: true, Result: "Unrelated answer"}, "interrupted", "Partial answer"},
		{"different prompt", &workspace.SessionOperation{ID: "current-turn", Prompt: "Another question", Completed: true, Result: "Unrelated answer"}, "interrupted", "Partial answer"},
		{"no operation", nil, "interrupted", "Partial answer"},
		{"empty completion", &workspace.SessionOperation{ID: "current-turn", Prompt: "Explore this idea", Completed: true}, "failed", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			original := composer.NewStore(dir)
			c, err := original.Create("Recovery", "default/test-model")
			if err != nil {
				t.Fatal(err)
			}
			c.Turns = []composer.Turn{
				{ID: "previous-turn", Prompt: "Previous question", Answer: "Previous answer", Status: "completed"},
				{ID: "current-turn", Prompt: "Explore this idea", Answer: "Partial answer", Status: "running", Error: "Stale error"},
			}
			c.Question = &composer.Question{CallID: "stale-question", Question: "Continue?"}
			if err := original.Save(c); err != nil {
				t.Fatal(err)
			}
			store := original.SessionStore(c)
			native, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			native.Operation = test.operation
			if err := store.Save(native); err != nil {
				t.Fatal(err)
			}

			// A new service must recover from disk without invoking a model.
			service := newComposerService(t.Context(), dir, nil)
			t.Cleanup(func() {
				if err := service.Close(); err != nil {
					t.Error(err)
				}
			})
			for range 2 {
				request := httptest.NewRequest(http.MethodGet, "/api/v1/composers/"+c.ID, nil)
				request.SetPathValue("composer", c.ID)
				response := httptest.NewRecorder()
				service.serveItem(response, request)
				if response.Code != http.StatusOK {
					t.Fatalf("recover = %d %s", response.Code, response.Body.String())
				}
				var recovered composer.Conversation
				if err := json.Unmarshal(response.Body.Bytes(), &recovered); err != nil {
					t.Fatal(err)
				}
				turn := recovered.Turns[1]
				if turn.Status != test.status || turn.Answer != test.answer {
					t.Fatalf("recovered turn = %+v", turn)
				}
				if (turn.Error == "") != (test.status == "completed") {
					t.Fatalf("recovered error = %q for %s", turn.Error, turn.Status)
				}
				if recovered.Question != nil || !reflect.DeepEqual(recovered.Turns[0], c.Turns[0]) {
					t.Fatalf("recovery changed unrelated history or retained a stale question: %+v", recovered)
				}
			}
			saved, err := original.Load(c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Turns[1].Status != test.status || saved.Turns[1].Answer != test.answer {
				t.Fatalf("recovery was not persisted: %+v", saved.Turns[1])
			}
		})
	}
}
