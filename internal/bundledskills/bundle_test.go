package bundledskills

import (
	"os"
	"strings"
	"testing"
)

func TestBundleMatchesPinnedDriverAndUsesEmbeddedCommand(t *testing.T) {
	bundle := CDP()
	if _, err := bundle.Manifest(); err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	const version = "v0.0.0-20261107143548-ec463b2b34af"
	if !strings.Contains(string(mod), "github.com/snowmerak/cdp-q-driver "+version) {
		t.Fatal("refresh bundled skill when updating the pinned CDP module")
	}
	for path, body := range bundle.Files {
		if !strings.Contains(body, version) {
			t.Fatalf("%s missing upstream provenance", path)
		}
	}
	if !strings.Contains(bundle.Files["SKILL.md"], "q cdp --session research") || !strings.Contains(bundle.Files["references/javascript.md"], "q cdp") {
		t.Fatal("bundled examples require a separate driver")
	}
}
