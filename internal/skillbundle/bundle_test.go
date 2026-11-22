package skillbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLineEndingsDoNotChangeManifestOrInstalledGeneration(t *testing.T) {
	lf := Bundle{Version: "20261107143548.0001", Files: map[string]string{
		"SKILL.md":                 "---\nname: q-cdp\ndescription: Browser skill.\n---\nUse q cdp.\n",
		"references/javascript.md": "# JavaScript\nRun q cdp.\n",
	}}
	crlf := Bundle{Version: lf.Version, Files: make(map[string]string)}
	for name, body := range lf.Files {
		crlf.Files[name] = strings.ReplaceAll(body, "\n", "\r\n")
	}
	wanted, err := lf.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	// Preserve the digest already installed by existing LF-based binaries.
	data, _ := json.Marshal(lf)
	digest := sha256.Sum256(data)
	if wanted.Digest != hex.EncodeToString(digest[:]) {
		t.Fatal("canonical LF digest changed")
	}
	for _, order := range [][]Bundle{{lf, crlf}, {crlf, lf}} {
		dir := t.TempDir()
		for _, bundle := range order {
			installed, err := Install(dir, bundle)
			if err != nil || installed != wanted {
				t.Fatalf("cross-platform install: %+v %v", installed, err)
			}
			location, err := Location(dir)
			if err != nil {
				t.Fatal(err)
			}
			for name, wantedBody := range lf.Files {
				stored, err := os.ReadFile(filepath.Join(location, "q-cdp", filepath.FromSlash(name)))
				if err != nil || string(stored) != wantedBody {
					t.Fatalf("noncanonical installed resource %s: %q %v", name, stored, err)
				}
			}
		}
		changed := Bundle{Version: lf.Version, Files: map[string]string{"SKILL.md": lf.Files["SKILL.md"] + "Changed guidance.\n"}}
		if _, err := Install(dir, changed); err == nil {
			t.Fatal("accepted changed content without a version update")
		}
	}
	if !strings.Contains(crlf.Files["SKILL.md"], "\r\n") {
		t.Fatal("normalization mutated the caller's bundle")
	}
}

func TestCanonicalizationStillRejectsWrongSkillName(t *testing.T) {
	for _, name := range []string{"other", "q-cdp-extra"} {
		bundle := Bundle{Version: "20261107143548.0001", Files: map[string]string{"SKILL.md": "---\r\nname: " + name + "\r\n---\r\n"}}
		if _, err := bundle.Manifest(); err == nil {
			t.Fatalf("accepted skill name %q", name)
		}
	}
}
