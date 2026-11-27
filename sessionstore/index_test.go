package sessionstore

import (
	"errors"
	"io"
	"sync/atomic"
	"testing"
)

type spyIndexCloser struct {
	closes atomic.Int32
	err    error
}

func (c *spyIndexCloser) Close() error {
	c.closes.Add(1)
	return c.err
}

func TestComposeIndexValidationDelegationAndCloserDedup(t *testing.T) {
	if NewLocalIndex(nil) != nil {
		t.Fatal("NewLocalIndex(nil) must return untyped nil")
	}

	raw, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	local := NewLocalIndex(raw)

	if _, err := ComposeIndex(nil, local, local, local); err == nil {
		t.Fatal("expected error for nil writer")
	}
	if _, err := ComposeIndex(local, nil, local, local); err == nil {
		t.Fatal("expected error for nil reader")
	}
	if _, err := ComposeIndex(local, local, nil, local); err == nil {
		t.Fatal("expected error for nil searcher")
	}
	if _, err := ComposeIndex(local, local, local, nil); err == nil {
		t.Fatal("expected error for nil vectors")
	}

	shared := &spyIndexCloser{}
	failing := &spyIndexCloser{err: errors.New("close err")}
	composed, err := ComposeIndex(local, local, local, local, nil, shared, shared, failing)
	if err != nil {
		t.Fatal(err)
	}

	saved, err := composed.Save(t.Context(), Record{Kind: KindMessage, Content: "composed index smoke"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := composed.Get(t.Context(), saved.ID)
	if err != nil || got.ID != saved.ID {
		t.Fatalf("Get = %#v, err = %v", got, err)
	}
	res, err := composed.Search(t.Context(), SearchOptions{Text: "composed"})
	if err != nil || len(res.Hits) != 1 {
		t.Fatalf("Search = %#v, err = %v", res, err)
	}
	docs, _, err := composed.IndexCounts(t.Context())
	if err != nil || docs != 1 {
		t.Fatalf("IndexCounts = (%d, %v), want 1", docs, err)
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

	var nilIdx *ComposedIndex
	if err := nilIdx.Close(); err != nil {
		t.Fatalf("nil ComposedIndex.Close = %v", err)
	}
	var _ io.Closer = composed
}
