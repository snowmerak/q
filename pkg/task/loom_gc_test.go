package task

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/blob/localfs"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlite"
)

func openTestLoomEnv(t *testing.T, opts LoomOptions) (*sqlite.Store, *localfs.Store, *LoomStore, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	rdb, err := sqlite.Open(context.Background(), filepath.Join(dir, "q.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	blobs, err := localfs.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("open blob store: %v", err)
	}
	t.Cleanup(func() { _ = blobs.Close() })

	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	loom := NewLoomStoreWithOptions(rdb, blobs, opts)
	loom.nowFn = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	return rdb, blobs, loom, &now
}

func TestLoomGCDryRunRootsParentsFKAndMediaSafety(t *testing.T) {
	ctx := context.Background()
	rdb, blobs, loom, now := openTestLoomEnv(t, LoomOptions{
		MaxArtifactBytes: 64 << 20,
		MaxStoreBytes:    256 << 20,
		AutoGC:           true,
		GCTriggerRatio:   0.80,
		GCTargetRatio:    0.60,
		GCGracePeriod:    1 * time.Hour,
	})

	p, _ := rdb.Personas().Create(ctx, domain.Persona{Name: "Player"})
	ch, _ := rdb.Characters().Create(ctx, domain.Character{Name: "Aria"})
	liveRoom, err := rdb.Rooms().Create(ctx, domain.Room{
		Title: "Live Room", Mode: domain.RoomModeDirect, PersonaID: p.ID, PersonaSnapshot: p,
	}, []domain.RoomMember{{CharacterID: ch.ID, CharacterSnapshot: ch, Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	deletedRoom, err := rdb.Rooms().Create(ctx, domain.Room{
		Title: "Deleted Room", Mode: domain.RoomModeDirect, PersonaID: p.ID, PersonaSnapshot: p,
	}, []domain.RoomMember{{CharacterID: ch.ID, CharacterSnapshot: ch, Active: true}})
	if err != nil {
		t.Fatal(err)
	}

	// Create 6 artifacts at T=0, then advance clock by 2 hours so grace expires for them.
	artDone, err := loom.Put(ctx, strings.Repeat("a", 100), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	artActiveInv, err := loom.Put(ctx, strings.Repeat("b", 120), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	artParent, err := loom.Put(ctx, strings.Repeat("c", 140), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	artChildMsg, err := loom.Put(ctx, strings.Repeat("d", 160), "text/plain", map[string]any{
		"parents": []string{"loom://" + artParent.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	artCheckpoint, err := loom.Put(ctx, strings.Repeat("e", 180), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	artDeletedRoom, err := loom.Put(ctx, strings.Repeat("f", 200), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	artPinned, err := loom.Put(ctx, strings.Repeat("p", 220), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Mark artPinned as pinned directly via Tx/create replacement? Let's insert a pinned artifact too:
	_ = artPinned
	pinnedID := strings.ReplaceAll(domain.NewID(), "-", "")
	if sz, sha, putErr := blobs.Put(ctx, "loom/"+pinnedID, strings.NewReader(strings.Repeat("P", 240)), "text/plain"); putErr != nil {
		t.Fatal(putErr)
	} else if err := rdb.Artifacts().Create(ctx, domain.Artifact{
		ID: pinnedID, Kind: domain.ArtifactKindLoom, MediaType: "text/plain", ByteSize: sz, SHA256: sha,
		BlobKey: "loom/" + pinnedID, Pinned: true, CreatedAt: now.UnixMilli(), LastAccessedAt: now.UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	// Non-Loom media artifact must NEVER be counted or collected.
	mediaID := domain.NewID()
	if sz, sha, putErr := blobs.Put(ctx, "media/"+mediaID, strings.NewReader("media-bytes"), "image/png"); putErr != nil {
		t.Fatal(putErr)
	} else if err := rdb.Artifacts().Create(ctx, domain.Artifact{
		ID: mediaID, Kind: domain.ArtifactKindMedia, MediaType: "image/png", ByteSize: sz, SHA256: sha,
		BlobKey: "media/" + mediaID, CreatedAt: now.UnixMilli(), LastAccessedAt: now.UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	// Completed turn pointing to artDone via tool_invocations.artifact_id (must be FK-safely cleared and swept).
	doneTurn, _, err := rdb.Turns().CreateOrGetIdempotent(ctx, domain.Turn{
		RoomID: liveRoom.ID, RequestID: "done-req", InputHash: domain.HashInput("done"), Status: domain.TurnStatusCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.Tools().CreateInvocation(ctx, domain.ToolInvocation{
		TurnID: doneTurn.ID, RoomID: liveRoom.ID, ActorID: ch.ID, CallID: "c1", ToolName: "run_command",
		ToolSource: domain.ToolSourceBuiltin, GateMode: domain.ApprovalBypass, Status: domain.ToolStatusCompleted,
		ArtifactID: artDone.ID,
	}); err != nil {
		t.Fatal(err)
	}

	// Active turn pointing to artActiveInv and artCheckpoint in checkpoint_json.
	activeTurn, _, err := rdb.Turns().CreateOrGetIdempotent(ctx, domain.Turn{
		RoomID: liveRoom.ID, RequestID: "active-req", InputHash: domain.HashInput("active"), Status: domain.TurnStatusAwaitingUser,
		CheckpointJSON: `{"note":"see loom://` + artCheckpoint.ID + `"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.Tools().CreateInvocation(ctx, domain.ToolInvocation{
		TurnID: activeTurn.ID, RoomID: liveRoom.ID, ActorID: ch.ID, CallID: "c2", ToolName: "run_command",
		ToolSource: domain.ToolSourceBuiltin, GateMode: domain.ApprovalBypass, Status: domain.ToolStatusRunning,
		ArtifactID: artActiveInv.ID,
	}); err != nil {
		t.Fatal(err)
	}

	// Live room message pointing to artChildMsg (keeps artChildMsg AND artParent).
	if _, err := rdb.Messages().Append(ctx, domain.Message{
		RoomID: liveRoom.ID, TurnID: doneTurn.ID, Role: domain.MessageRoleCharacter, AuthorID: ch.ID, AuthorName: ch.Name,
		Content: "Inspect loom://" + artChildMsg.ID + " for full trace.", ObservedBy: []string{p.ID, ch.ID},
	}); err != nil {
		t.Fatal(err)
	}

	// Deleted room message pointing to artDeletedRoom (must NOT root it).
	delTurn, _, _ := rdb.Turns().CreateOrGetIdempotent(ctx, domain.Turn{
		RoomID: deletedRoom.ID, RequestID: "del-req", InputHash: domain.HashInput("del"), Status: domain.TurnStatusCompleted,
	})
	if _, err := rdb.Messages().Append(ctx, domain.Message{
		RoomID: deletedRoom.ID, TurnID: delTurn.ID, Role: domain.MessageRoleCharacter, AuthorID: ch.ID, AuthorName: ch.Name,
		Content: "Deleted room ref loom://" + artDeletedRoom.ID, ObservedBy: []string{p.ID, ch.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Rooms().Delete(ctx, deletedRoom.ID, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}

	// Advance clock by 2 hours so grace period expires on those artifacts, then create 1 fresh unrooted artifact inside grace window.
	*now = now.Add(2 * time.Hour)
	artRecent, err := loom.Put(ctx, strings.Repeat("r", 260), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Dry-run preview must match actual sweep counts and keep everything intact.
	dryRes, err := loom.Collect(ctx, LoomGCOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry-run Collect: %v", err)
	}
	// Expect unrooted outside grace: artDone (100) + artDeletedRoom (200) + artPinned (the unpinned one before pinnedID: 220) = 3 artifacts, 520 bytes!
	if dryRes.ArtifactsRemoved != 3 || dryRes.BlobsRemoved != 3 || dryRes.BytesReclaimed != 520 {
		t.Fatalf("unexpected dry-run result: %+v", dryRes)
	}
	if _, err := rdb.Artifacts().Get(ctx, artDone.ID); err != nil {
		t.Fatalf("dry-run deleted artDone: %v", err)
	}

	realRes, err := loom.Collect(ctx, LoomGCOptions{DryRun: false})
	if err != nil {
		t.Fatalf("real Collect: %v", err)
	}
	if realRes.ArtifactsRemoved != dryRes.ArtifactsRemoved || realRes.BlobsRemoved != dryRes.BlobsRemoved || realRes.BytesReclaimed != dryRes.BytesReclaimed {
		t.Fatalf("real Collect %+v != dry-run %+v", realRes, dryRes)
	}

	// Verify removed artifacts + cleared FK reference on tool_invocations.
	for _, removedID := range []string{artDone.ID, artDeletedRoom.ID, artPinned.ID} {
		if _, err := rdb.Artifacts().Get(ctx, removedID); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("expected %s to be deleted, got %v", removedID, err)
		}
		if _, err := blobs.Open(ctx, "loom/"+removedID); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("expected blob loom/%s deleted, got %v", removedID, err)
		}
	}
	invs, err := rdb.Tools().ListInvocationsByTurn(ctx, doneTurn.ID)
	if err != nil || len(invs) != 1 || invs[0].ArtifactID != "" {
		t.Fatalf("expected tool invocation artifact_id cleared, got %+v err=%v", invs, err)
	}

	// Verify preserved artifacts: active invocation, checkpoint ref, msg child + parent, pinned, grace-protected recent, and media.
	for _, keepID := range []string{artActiveInv.ID, artCheckpoint.ID, artChildMsg.ID, artParent.ID, pinnedID, artRecent.ID, mediaID} {
		if _, err := rdb.Artifacts().Get(ctx, keepID); err != nil {
			t.Fatalf("expected %s preserved, got %v", keepID, err)
		}
	}
}

func TestLoomSharedBlobRetentionAndTransientLease(t *testing.T) {
	ctx := context.Background()
	rdb, blobs, loom, now := openTestLoomEnv(t, LoomOptions{
		MaxArtifactBytes: 64 << 20,
		MaxStoreBytes:    256 << 20,
		AutoGC:           false,
		GCGracePeriod:    1 * time.Hour,
	})

	sharedKey := "loom/sharedbody0000000000000000000001"
	sz, sha, err := blobs.Put(ctx, sharedKey, strings.NewReader(strings.Repeat("x", 300)), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	id1 := strings.ReplaceAll(domain.NewID(), "-", "")
	id2 := strings.ReplaceAll(domain.NewID(), "-", "")
	for _, id := range []string{id1, id2} {
		if err := rdb.Artifacts().Create(ctx, domain.Artifact{
			ID: id, Kind: domain.ArtifactKindLoom, MediaType: "text/plain", ByteSize: sz, SHA256: sha,
			BlobKey: sharedKey, CreatedAt: now.UnixMilli(), LastAccessedAt: now.UnixMilli(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	*now = now.Add(2 * time.Hour)
	// Hold a transient lease on id1; collect removes only id2 and MUST keep the shared blob.
	lease := loom.AcquireLease(5*time.Minute, "loom://"+id1)
	res1, err := loom.Collect(ctx, LoomGCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res1.ArtifactsRemoved != 1 || res1.BlobsRemoved != 0 || res1.BytesReclaimed != 0 {
		t.Fatalf("expected id2 removed while shared blob kept via lease on id1: %+v", res1)
	}
	rc, err := blobs.Open(ctx, sharedKey)
	if err != nil {
		t.Fatalf("expected shared blob still readable: %v", err)
	}
	_ = rc.Close()

	// Release lease and collect again: now id1 and shared blob are removed.
	lease.Release()
	res2, err := loom.Collect(ctx, LoomGCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res2.ArtifactsRemoved != 1 || res2.BlobsRemoved != 1 || res2.BytesReclaimed != 300 {
		t.Fatalf("expected final ref and shared blob removed: %+v", res2)
	}
}

func TestLoomAutoGCTriggerTargetAndHardStoreCap(t *testing.T) {
	ctx := context.Background()
	rdb, _, loom, now := openTestLoomEnv(t, LoomOptions{
		MaxArtifactBytes: 600,
		MaxStoreBytes:    1000,
		AutoGC:           true,
		GCTriggerRatio:   0.80, // 800 bytes
		GCTargetRatio:    0.40, // 400 bytes
		GCGracePeriod:    1 * time.Hour,
	})

	// Insert two old (expired grace) unrooted 350-byte artifacts (total 700 <= 800 trigger).
	a1, err := loom.Put(ctx, strings.Repeat("1", 350), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(10 * time.Second)
	a2, err := loom.Put(ctx, strings.Repeat("2", 350), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Expire grace window on a1 & a2, then Put 250 bytes (700+250=950 > 800 trigger).
	// Auto-GC should reclaim oldest artifact(s) down to <= 400 bytes target before writing 250 bytes.
	*now = now.Add(2 * time.Hour)
	a3, err := loom.Put(ctx, strings.Repeat("3", 250), "text/plain", nil)
	if err != nil {
		t.Fatalf("expected Put to trigger AutoGC and succeed: %v", err)
	}
	if _, err := rdb.Artifacts().Get(ctx, a1.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected oldest unrooted a1 collected by AutoGC, got %v", err)
	}
	if _, err := rdb.Artifacts().Get(ctx, a2.ID); err != nil {
		t.Fatalf("expected a2 kept because removing a1 reduced bytes to 350 <= target 400, got %v", err)
	}
	if _, err := rdb.Artifacts().Get(ctx, a3.ID); err != nil {
		t.Fatal(err)
	}

	// Now both a2 (350) and a3 (250) = 600 bytes, and a3 is inside grace period while a2 is outside grace;
	// however, if we pin a2 so nothing is collectable, putting 500 bytes (600+500 > 1000) must fail with max_store_bytes.
	p, _ := rdb.Personas().Create(ctx, domain.Persona{Name: "P"})
	rm, _ := rdb.Rooms().Create(ctx, domain.Room{Title: "R", Mode: domain.RoomModeDirect, PersonaID: p.ID, PersonaSnapshot: p}, nil)
	tr, _, _ := rdb.Turns().CreateOrGetIdempotent(ctx, domain.Turn{RoomID: rm.ID, RequestID: "r1", InputHash: "h1", Status: domain.TurnStatusCompleted})
	_, _ = rdb.Messages().Append(ctx, domain.Message{
		RoomID: rm.ID, TurnID: tr.ID, Role: domain.MessageRoleUser, AuthorID: p.ID, AuthorName: p.Name,
		Content: "keep loom://" + a2.ID, ObservedBy: []string{p.ID},
	})
	_ = json.Valid([]byte("{}"))

	if _, err := loom.Put(ctx, strings.Repeat("4", 500), "text/plain", nil); err == nil || !strings.Contains(err.Error(), "max_store_bytes") {
		t.Fatalf("expected max_store_bytes cap error when store full of rooted artifacts, got %v", err)
	}
}
