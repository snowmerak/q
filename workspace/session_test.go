package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/thinker"
)

func TestStoreRoundTripAndClear(t *testing.T) {
	store := Store{Root: t.TempDir()}
	startedAt := time.Date(2026, time.August, 20, 12, 30, 0, 0, time.UTC)
	want := Session{
		Title:      "Workspace session",
		UpdatedAt:  &startedAt,
		Transcript: []client.Message{{Role: client.RoleUser, Content: "question"}},
		Context:    []client.Message{{Role: client.RoleSystem, Name: "q_context_summary", Content: "summary"}},
		ActiveTask: &ActiveTask{
			Objective: "finish the durable task", CompletionCriteria: []string{"tests pass"}, StartedAt: startedAt,
		},
		Learning: thinker.LearningState{
			Version: 1, StreamID: "stream", NextCursor: 2,
			Events: []thinker.LearningEvent{{Cursor: 1, Message: client.Message{Role: client.RoleUser, Content: "durable decision"}}},
		},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != CurrentVersion || got.Title != "Workspace session" || got.UpdatedAt == nil || !got.UpdatedAt.Equal(startedAt) ||
		len(got.Transcript) != 1 || got.Transcript[0].Content != "question" ||
		len(got.Context) != 1 || got.Context[0].Content != "summary" ||
		got.Learning.StreamID != "stream" || len(got.Learning.Events) != 1 || got.Learning.Events[0].Message.Content != "durable decision" ||
		got.ActiveTask == nil || got.ActiveTask.Objective != "finish the durable task" ||
		!reflect.DeepEqual(got.ActiveTask.CompletionCriteria, []string{"tests pass"}) || !got.ActiveTask.StartedAt.Equal(startedAt) {
		t.Fatalf("loaded session = %#v", got)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("session permissions are too broad: %o", info.Mode().Perm())
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load() after Clear() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, DirectoryName)); err != nil {
		t.Fatalf("Clear removed the workspace directory: %v", err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), []byte(`{"version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load accepted an unknown field")
	}
}

func TestLoomReferencesAtUsesCurrentSessionProjection(t *testing.T) {
	root := t.TempDir()
	first := loom.Ref("loom://0123456789abcdef0123456789abcdef")
	second := loom.Ref("loom://fedcba9876543210fedcba9876543210")
	store := Store{Root: root}
	if err := store.Save(Session{
		Transcript: []client.Message{{Role: client.RoleTool, Content: `{"loom_ref":"` + first.String() + `"}`}},
		Context:    []client.Message{{Role: client.RoleSystem, Content: "parent " + second.String()}},
	}); err != nil {
		t.Fatal(err)
	}

	refs, err := LoomReferencesAt(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(refs, []loom.Ref{first, second}) {
		t.Fatalf("Loom refs = %#v", refs)
	}
}

func TestLoomReferencesAtAllowsMissingSession(t *testing.T) {
	refs, err := LoomReferencesAt(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("Loom refs = %#v", refs)
	}
}
