// Package contracttest provides shared behavioral tests across RDBStore,
// SearchIndex, and BlobStore implementations.
package contracttest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
)

func RunRDBContractTests(t *testing.T, newStore func(t *testing.T) storage.RDBStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("persona_character_optimistic_conflict", func(t *testing.T) {
		st := newStore(t)
		defer func() { _ = st.Close() }()

		p, err := st.Personas().Create(ctx, domain.Persona{Name: "Player", Description: "Traveler", IsDefault: true})
		if err != nil {
			t.Fatalf("Create persona: %v", err)
		}
		p.Name = "Player 2"
		updated, err := st.Personas().Update(ctx, p)
		if err != nil {
			t.Fatalf("Update persona: %v", err)
		}
		// Stale version update must fail with ErrConflict.
		if _, err := st.Personas().Update(ctx, p); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("expected ErrConflict on stale persona update, got %v", err)
		}
		got, err := st.Personas().Get(ctx, updated.ID)
		if err != nil || got.Name != "Player 2" || got.Version != 2 {
			t.Fatalf("Get updated persona: %+v err=%v", got, err)
		}

		c, err := st.Characters().Create(ctx, domain.Character{
			Name:          "Aria",
			Personality:   "Curious",
			InitialValues: []string{"Keep promises"},
		})
		if err != nil {
			t.Fatalf("Create character: %v", err)
		}
		if c.Version != 1 {
			t.Fatalf("expected version 1, got %d", c.Version)
		}
	})

	t.Run("room_turn_idempotency_and_messages", func(t *testing.T) {
		st := newStore(t)
		defer func() { _ = st.Close() }()

		p, _ := st.Personas().Create(ctx, domain.Persona{Name: "User"})
		c, _ := st.Characters().Create(ctx, domain.Character{Name: "Bot"})

		rm, err := st.Rooms().Create(ctx, domain.Room{
			Title:           "Test Room",
			Mode:            domain.RoomModeDirect,
			PersonaID:       p.ID,
			PersonaSnapshot: p,
		}, []domain.RoomMember{{
			CharacterID:       c.ID,
			CharacterSnapshot: c,
			Active:            true,
		}})
		if err != nil {
			t.Fatalf("Create room: %v", err)
		}

		req := domain.Turn{
			RoomID:    rm.ID,
			RequestID: "req-1",
			InputHash: domain.HashInput("hello"),
			Status:    domain.TurnStatusRunning,
		}
		t1, created, err := st.Turns().CreateOrGetIdempotent(ctx, req)
		if err != nil || !created {
			t.Fatalf("first CreateOrGetIdempotent: created=%v err=%v", created, err)
		}
		t2, created, err := st.Turns().CreateOrGetIdempotent(ctx, req)
		if err != nil || created || t2.ID != t1.ID {
			t.Fatalf("repeat CreateOrGetIdempotent: created=%v id=%s/%s err=%v", created, t2.ID, t1.ID, err)
		}
		reqDiff := req
		reqDiff.InputHash = domain.HashInput("different")
		if _, _, err := st.Turns().CreateOrGetIdempotent(ctx, reqDiff); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("expected ErrConflict on mismatched InputHash, got %v", err)
		}

		if err := st.Tx(ctx, func(tx storage.RDBTx) error {
			m1, err := tx.Messages().Append(ctx, domain.Message{
				RoomID:     rm.ID,
				TurnID:     t1.ID,
				Role:       domain.MessageRoleUser,
				AuthorID:   p.ID,
				AuthorName: p.Name,
				Content:    "hello",
				ObservedBy: []string{p.ID, c.ID},
			})
			if err != nil {
				return err
			}
			_, err = tx.Outbox().Enqueue(ctx, domain.OutboxEntry{
				EntityType:  "message",
				EntityID:    m1.ID,
				Op:          domain.OutboxOpUpsert,
				Revision:    1,
				PayloadJSON: `{"scope":"room:` + rm.ID + `","text":"hello"}`,
			})
			return err
		}); err != nil {
			t.Fatalf("Tx append+outbox: %v", err)
		}

		msgs, err := st.Messages().List(ctx, rm.ID, 0, 10)
		if err != nil || len(msgs) != 1 || msgs[0].Seq != 1 {
			t.Fatalf("List messages: %+v err=%v", msgs, err)
		}
		pending, err := st.Outbox().ListPending(ctx, domain.NowMS()+1000, 10)
		if err != nil || len(pending) != 1 {
			t.Fatalf("ListPending outbox: %+v err=%v", pending, err)
		}
	})
}

func RunSearchContractTests(t *testing.T, newIndex func(t *testing.T) storage.SearchIndex) {
	t.Helper()
	ctx := context.Background()

	t.Run("observer_isolation_hybrid_and_semantic_unavailable", func(t *testing.T) {
		idx := newIndex(t)
		defer func() { _ = idx.Close() }()

		// Semantic search without configured vectors must return ErrUnavailable.
		if _, err := idx.Search(ctx, storage.SearchQuery{
			Text:   "secret",
			Vector: []float32{1, 0, 0},
			Mode:   "semantic",
		}); !errors.Is(err, storage.ErrUnavailable) {
			t.Fatalf("expected ErrUnavailable for semantic without vectors, got %v", err)
		}

		if err := idx.ConfigureVector(ctx, "test-emb", 3); err != nil {
			t.Fatalf("ConfigureVector: %v", err)
		}
		err := idx.UpsertBatch(ctx, []storage.SearchDocument{
			{
				ID: "m1", EntityType: "memory", Scope: "room:r1", OwnerID: "aria",
				ObservedBy: []string{"aria", "player"}, Text: "secret garden key",
				OccurredAt: 1000, Revision: 1, Vector: []float32{1, 0, 0},
			},
			{
				ID: "m2", EntityType: "memory", Scope: "room:r1", OwnerID: "kai",
				ObservedBy: []string{"kai"}, Text: "secret dungeon map",
				OccurredAt: 2000, Revision: 1, Vector: []float32{0.9, 0.1, 0},
			},
		})
		if err != nil {
			t.Fatalf("UpsertBatch: %v", err)
		}

		// ObserverID="aria" must only see m1, never m2.
		hits, err := idx.Search(ctx, storage.SearchQuery{
			Text:       "secret",
			Vector:     []float32{1, 0, 0},
			Mode:       "hybrid",
			ObserverID: "aria",
			Limit:      10,
		})
		if err != nil {
			t.Fatalf("Search hybrid: %v", err)
		}
		if len(hits) != 1 || hits[0].ID != "m1" {
			t.Fatalf("expected only m1 for observer aria, got %+v", hits)
		}

		// Hybrid with nil vector falls back to lexical.
		hitsLex, err := idx.Search(ctx, storage.SearchQuery{
			Text:       "dungeon",
			Mode:       "hybrid",
			ObserverID: "kai",
			Limit:      10,
		})
		if err != nil || len(hitsLex) != 1 || hitsLex[0].ID != "m2" {
			t.Fatalf("expected lexical fallback hit m2, got %+v err=%v", hitsLex, err)
		}

		// Rebuild from channel preserves documents.
		ch := make(chan storage.SearchDocument, 1)
		ch <- storage.SearchDocument{
			ID: "m1", EntityType: "memory", Scope: "room:r1", OwnerID: "aria",
			ObservedBy: []string{"aria"}, Text: "rebuilt garden key",
			OccurredAt: 3000, Revision: 2, Vector: []float32{0, 1, 0},
		}
		close(ch)
		if err := idx.ResetAndRebuild(ctx, ch); err != nil {
			t.Fatalf("ResetAndRebuild: %v", err)
		}
		docs, vecs, err := idx.Counts(ctx)
		if err != nil || docs != 1 || vecs != 1 {
			t.Fatalf("Counts after rebuild = (%d, %d, %v), want (1, 1, nil)", docs, vecs, err)
		}
	})
}

func RunBlobContractTests(t *testing.T, newStore func(t *testing.T) storage.BlobStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("put_open_delete_and_traversal_guard", func(t *testing.T) {
		bs := newStore(t)
		defer func() { _ = bs.Close() }()

		if _, _, err := bs.Put(ctx, "../escape.txt", bytes.NewBufferString("bad"), "text/plain"); !errors.Is(err, storage.ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput on traversal key, got %v", err)
		}

		size, sum, err := bs.Put(ctx, "loom/abc123", bytes.NewBufferString("hello loom"), "text/plain")
		if err != nil || size != 10 || sum == "" {
			t.Fatalf("Put: size=%d sum=%q err=%v", size, sum, err)
		}
		rc, err := bs.Open(ctx, "loom/abc123")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		got, _ := io.ReadAll(rc)
		_ = rc.Close()
		if string(got) != "hello loom" {
			t.Fatalf("Open content = %q", string(got))
		}
		if err := bs.Delete(ctx, "loom/abc123"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := bs.Open(ctx, "loom/abc123"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("expected ErrNotFound after delete, got %v", err)
		}
	})
}
