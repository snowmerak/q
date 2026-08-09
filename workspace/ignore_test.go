package workspace

import (
	"os"
	"testing"
)

func TestIgnoreRoundTrip(t *testing.T) {
	store := Store{Root: t.TempDir()}
	content, err := store.LoadIgnore()
	if err != nil || content != "" {
		t.Fatalf("missing ignore = %q, %v", content, err)
	}
	if err := store.SaveIgnore(".gradle/\nbuild/"); err != nil {
		t.Fatal(err)
	}
	content, err = store.LoadIgnore()
	if err != nil || content != ".gradle/\nbuild/\n" {
		t.Fatalf("saved ignore = %q, %v", content, err)
	}
	if _, err := os.Stat(store.IgnorePath()); err != nil {
		t.Fatal(err)
	}
}
