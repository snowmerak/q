//go:build windows

package main

import (
	"crypto/rand"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestLoginRegistrationQuotesPathsAndHonorsOSDisable(t *testing.T) {
	root := `Software\QStudioTest-` + rand.Text()
	store := startupRegistry{run: root + `\Run`, approved: root + `\Approved`, name: "Q Studio", executable: filepath.Join(t.TempDir(), "with spaces", "Q Studio.exe")}
	t.Cleanup(func() {
		_ = registry.DeleteKey(registry.CURRENT_USER, store.run)
		_ = registry.DeleteKey(registry.CURRENT_USER, store.approved)
		_ = registry.DeleteKey(registry.CURRENT_USER, root)
	})
	if err := store.set(true); err != nil {
		t.Fatal(err)
	}
	if on, err := store.enabled(); err != nil || !on {
		t.Fatalf("registration: %v %v", on, err)
	}
	approved, _, err := registry.CreateKey(registry.CURRENT_USER, store.approved, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	if err = approved.SetBinaryValue(store.name, []byte{3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	_ = approved.Close()
	if on, err := store.enabled(); err != nil || on {
		t.Fatalf("OS disable: %v %v", on, err)
	}
	if err = store.set(true); err != nil {
		t.Fatal(err)
	}
	if on, err := store.enabled(); err != nil || !on {
		t.Fatalf("explicit re-enable: %v %v", on, err)
	}
	if err = store.set(false); err != nil {
		t.Fatal(err)
	}
	if on, err := store.enabled(); err != nil || on {
		t.Fatalf("remove registration: %v %v", on, err)
	}
}
