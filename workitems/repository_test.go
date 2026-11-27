package workitems

import (
	"errors"
	"io"
	"sync/atomic"
	"testing"
)

type spyCloser struct {
	closes atomic.Int32
	err    error
}

func (c *spyCloser) Close() error {
	c.closes.Add(1)
	return c.err
}

type nonComparableCloser struct {
	closes *atomic.Int32
	fn     func()
}

func (c nonComparableCloser) Close() error {
	c.closes.Add(1)
	return nil
}

func TestComposeRepositoryValidationDelegationAndCloserDedup(t *testing.T) {
	store, err := Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if _, err := ComposeRepository(nil, store, store, store, store); err == nil {
		t.Fatal("expected error when workspace repository is nil")
	}
	if _, err := ComposeRepository(store, nil, store, store, store); err == nil {
		t.Fatal("expected error when item repository is nil")
	}
	if _, err := ComposeRepository(store, store, nil, store, store); err == nil {
		t.Fatal("expected error when asset repository is nil")
	}
	if _, err := ComposeRepository(store, store, store, nil, store); err == nil {
		t.Fatal("expected error when outbox repository is nil")
	}
	if _, err := ComposeRepository(store, store, store, store, nil); err == nil {
		t.Fatal("expected error when coordinator repository is nil")
	}

	shared := &spyCloser{}
	failing := &spyCloser{err: errors.New("boom")}
	var nonCompCount atomic.Int32
	nonComp := nonComparableCloser{closes: &nonCompCount, fn: func() {}}

	composed, err := ComposeRepository(store, store, store, store, store, nil, shared, shared, failing, nonComp)
	if err != nil {
		t.Fatal(err)
	}

	ws, err := composed.CreateWorkspace(t.Context(), WorkspaceInput{
		Name:           "composed",
		WorkspaceRoots: []string{t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := composed.CreateItem(t.Context(), CreateInput{
		WorkspaceID: ws.ID,
		Kind:        Issue,
		Title:       "Delegated via ComposedRepository",
		Content:     "Body",
		Author:      "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := composed.GetItem(t.Context(), item.ID)
	if err != nil || got.ID != item.ID {
		t.Fatalf("GetItem = %#v, err = %v", got, err)
	}

	err1 := composed.Close()
	err2 := composed.Close()
	if !errors.Is(err1, failing.err) || !errors.Is(err2, failing.err) {
		t.Fatalf("close errors = (%v, %v), want %v", err1, err2, failing.err)
	}
	if got := shared.closes.Load(); got != 1 {
		t.Fatalf("shared closer count = %d, want 1", got)
	}
	if got := failing.closes.Load(); got != 1 {
		t.Fatalf("failing closer count = %d, want 1", got)
	}
	if got := nonCompCount.Load(); got != 1 {
		t.Fatalf("non-comparable closer count = %d, want 1", got)
	}

	var nilRepo *ComposedRepository
	if err := nilRepo.Close(); err != nil {
		t.Fatalf("nil ComposedRepository.Close = %v", err)
	}
	var _ io.Closer = composed
}
