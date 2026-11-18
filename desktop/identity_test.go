package main

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"regexp"
	"testing"
)

func TestProfileIDsRespectDBusNamesAndPreserveWorkingInstallations(t *testing.T) {
	busName := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z_][A-Za-z0-9_-]*)+$`)
	seen := map[string]bool{}
	changed, unchanged := 0, 0
	for i := range 64 {
		home := filepath.Join("profiles", fmt.Sprintf("profile-%d", i))
		digest := sha256.Sum256([]byte(filepath.Clean(home)))
		legacy := fmt.Sprintf("ne.saturday.q-studio.%x", digest[:12])
		id := singleInstanceID(home, "linux")
		if !busName.MatchString(id) || len(id) > 255 {
			t.Fatalf("invalid D-Bus name for %q: %q", home, id)
		}
		if seen[id] {
			t.Fatalf("profiles share the same instance ID: %q", id)
		}
		seen[id] = true
		if busName.MatchString(legacy) {
			unchanged++
			if id != legacy {
				t.Fatalf("changed a working Linux identity: %q -> %q", legacy, id)
			}
		} else {
			changed++
		}
		for _, platform := range []string{"windows", "darwin"} {
			if actual := singleInstanceID(home, platform); actual != legacy {
				t.Fatalf("changed %s identity: %q -> %q", platform, legacy, actual)
			}
		}
		if actual := singleInstanceID(filepath.Join(home, "child", ".."), "linux"); actual != id {
			t.Fatal("cleaning the same home directory changed its identity")
		}
	}
	if changed == 0 || unchanged == 0 {
		t.Fatal("did not cover both valid and invalid legacy identities")
	}
}
