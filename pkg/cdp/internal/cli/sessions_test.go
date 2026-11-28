package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSessionIDsAndApplicationDataPaths(t *testing.T) {
	for _, id := range []string{"default", "work-a", "0", "a_b", "nul-project", strings.Repeat("a", 64)} {
		if !validSessionID(id) {
			t.Errorf("valid ID rejected: %q", id)
		}
	}
	for _, id := range []string{"", "../other", "a/b", `a\b`, ".", "..", "UPPER", "-bad", "a b", "a.b", "nul", "con", "prn", "aux", "com1", "lpt9", strings.Repeat("a", 65)} {
		if validSessionID(id) {
			t.Errorf("unsafe/nonportable ID accepted: %q", id)
		}
	}
	home, local, xdg := t.TempDir(), t.TempDir(), t.TempDir()
	for _, tc := range []struct{ goos, local, xdg, want string }{
		{"windows", local, "", filepath.Join(local, "cdp-q-driver")},
		{"windows", "", "", filepath.Join(home, "AppData", "Local", "cdp-q-driver")},
		{"darwin", "", "", filepath.Join(home, "Library", "Application Support", "cdp-q-driver")},
		{"linux", "", xdg, filepath.Join(xdg, "cdp-q-driver")},
		{"linux", "", "relative-xdg", filepath.Join(home, ".local", "share", "cdp-q-driver")},
	} {
		got, err := platformDataRoot(tc.goos, home, tc.local, tc.xdg)
		if err != nil || got != tc.want {
			t.Errorf("%s data root=%q want %q: %v", tc.goos, got, tc.want, err)
		}
	}
	o := appOptions{Session: "work-a", DataDir: home}
	if err := configureSession(&o, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if o.State != filepath.Join(home, "sessions", "work-a", "runtime.json") || o.Metadata != filepath.Join(home, "sessions", "work-a", "session.json") {
		t.Fatalf("session paths escaped namespace: %+v", o)
	}
	legacy := appOptions{State: filepath.Join(home, "legacy.json"), Session: "work-a"}
	if err := configureSession(&legacy, map[string]bool{"session": true}); err == nil {
		t.Fatal("ambiguous --state and --session accepted")
	}
}

func TestSavedSessionConfigurationAndLastObservedInventory(t *testing.T) {
	o := appOptions{Named: true, Session: "work", Metadata: filepath.Join(t.TempDir(), "session.json")}
	serve := serveOptions{Endpoint: "http://user:private@127.0.0.1:9222?token=secret", CommandTimeout: 3 * time.Second, CleanupTimeout: 2 * time.Second}
	store, err := newSessionStore(o, serve, "instance-one")
	if err != nil {
		t.Fatal(err)
	}
	pages := []pageView{{Name: "main", ID: "target", Task: "default", Session: "work", Managed: true, Owned: true}}
	if err := store.update([]string{"default"}, pages); err != nil {
		t.Fatal(err)
	}
	saved, err := readMetadata(o.Metadata, o.Session)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != "work" || saved.Endpoint != serve.Endpoint || !reflect.DeepEqual(saved.Pages, pages) {
		t.Fatalf("saved session lost configuration/grouping: %+v", saved)
	}
	resolved, err := resolveServe(o, serveOptions{})
	if err != nil || resolved.Endpoint != serve.Endpoint || resolved.CommandTimeout != serve.CommandTimeout || resolved.CleanupTimeout != serve.CleanupTimeout {
		t.Fatalf("saved options not reused: %+v %v", resolved, err)
	}
	if got := publicEndpoint(saved.Endpoint); strings.Contains(got, "private") || strings.Contains(got, "secret") || strings.Contains(got, "user") {
		t.Fatalf("public inventory leaked endpoint credentials: %s", got)
	}
	if err := store.finish(map[string]any{"Closed": []string{"target"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.update([]string{"late"}, nil); err != nil {
		t.Fatal(err)
	}
	stopped, err := readMetadata(o.Metadata, o.Session)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != "stopped" || stopped.Cleanup == nil || !reflect.DeepEqual(stopped.Pages, pages) {
		t.Fatalf("late observation overwrote shutdown metadata: %+v", stopped)
	}
	if _, err := os.Stat(o.Metadata); err != nil {
		t.Fatal("stopping deleted saved configuration")
	}
}
