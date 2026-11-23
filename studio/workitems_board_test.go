package studio

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workitems"
)

func TestTicketBoardPaginatesDoneSeparately(t *testing.T) {
	dir := studioCatalogTestDirectory(t, t.TempDir())
	ctx := t.Context()
	store, err := workitems.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	root := t.TempDir()
	project, err := store.CreateWorkspace(ctx, workitems.WorkspaceInput{Name: "Board", WorkspaceRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.PutMember(ctx, project.ID, workitems.MemberInput{AgentID: "builtin/research", DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		item, err := store.CreateItem(ctx, workitems.CreateInput{
			WorkspaceID: project.ID, Kind: workitems.Ticket, Title: fmt.Sprintf("Board ticket %d", i+1), Content: "Inspect the work.", Author: "user",
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			continue
		}
		attempt, err := store.ClaimTicket(ctx, item.ID, workitems.ClaimInput{
			MemberID: member.ID, MemberRevision: member.Revision, Revision: item.Revision, DefinitionDigest: "research", Owner: "board-test", RequestID: "claim-" + item.ID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpdateAttempt(ctx, attempt.ID, workitems.AttemptUpdate{
			Owner: attempt.Owner, Token: attempt.Token, Generation: attempt.Generation, Status: "awaiting_review", Result: []byte(`{"outcome":"succeeded"}`),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReviewAttempt(ctx, attempt.ID, workitems.ReviewInput{
			ManagerSessionID: project.ManagerSessionID, Decision: "accept", RequestID: "review-" + item.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := newHandler(config.Store{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/work/items?kind=ticket&limit=2&workspace_id=" + project.ID
	page := func(view, cursor string, want []int64) string {
		t.Helper()
		response := serveJSON(t, handler, http.MethodGet, path+"&board="+view+"&cursor="+cursor, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("board page: %d %s", response.Code, response.Body.String())
		}
		var value workitems.ItemPage
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		numbers := make([]int64, 0, len(value.Items))
		for _, item := range value.Items {
			numbers = append(numbers, item.Number)
		}
		if !slices.Equal(numbers, want) {
			t.Fatalf("board %s numbers = %v, want %v", view, numbers, want)
		}
		return value.NextCursor
	}
	cursor := page("done", "", []int64{5, 4})
	if cursor == "" {
		t.Fatal("Done has no next page")
	}
	if page("done", cursor, []int64{3, 1}) != "" {
		t.Fatal("last Done page still has a cursor")
	}
	page("active", "", []int64{2})
	if response := serveJSON(t, handler, http.MethodGet, path+"&board=active&cursor="+cursor, nil); response.Code != http.StatusBadRequest {
		t.Fatalf("Done cursor used for active board: %d %s", response.Code, response.Body.String())
	}
}
