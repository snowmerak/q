package library

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/internal/bundledskills"
	"github.com/snowmerak/q/internal/skillbundle"
)

func TestBundleHandshakeUploadsOnlyChangedResources(t *testing.T) {
	wanted, err := bundledskills.CDP().Manifest()
	if err != nil {
		t.Fatal(err)
	}
	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		uploads++
		if request.Method != "PUT" || request.URL.Path != "/v1/skills/bundled/cdp" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		writeJSON(writer, http.StatusOK, wanted)
	}))
	defer server.Close()
	c := NewClient(server.URL+"/v1", "", time.Second)
	for _, health := range []Health{
		{}, // A pre-feature server remains compatible.
		{BundledSkills: true, SkillBundleVersion: wanted.Version, SkillBundleDigest: wanted.Digest},
		{BundledSkills: true, SkillBundleVersion: "99999999999999.0001", SkillBundleDigest: "newer"},
	} {
		if err := c.syncBundledSkills(t.Context(), health); err != nil {
			t.Fatal(err)
		}
	}
	if uploads != 0 {
		t.Fatal("unchanged or newer bundle was uploaded again")
	}
	if err := c.syncBundledSkills(t.Context(), Health{BundledSkills: true, SkillBundleVersion: "20000101000000.0001", SkillBundleDigest: "old"}); err != nil {
		t.Fatal(err)
	}
	if uploads != 1 {
		t.Fatal("changed bundle was not uploaded")
	}
}

func TestBundledSkillUpdatesThroughRunningLibraryWithoutChangingID(t *testing.T) {
	dir := t.TempDir()
	options := testOptions(dir, testConfig(t))
	runtime, err := EnsureWithOptions(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	c := runtime.Client()
	initial, err := c.SearchSkills(t.Context(), SkillSearchRequest{Query: "q-cdp"})
	if err != nil || len(initial.Hits) != 1 {
		t.Fatalf("bundled search: %+v %v", initial, err)
	}
	id := initial.Hits[0].ID
	resource, err := c.GetSkill(t.Context(), id, "references/javascript.md")
	if err != nil || !strings.Contains(string(resource.Content), "q cdp") || resource.Skill.Source != agentskills.SourceBundled {
		t.Fatalf("resource: %+v %v", resource, err)
	}
	oldDirectory := resource.Skill.Directory
	userDirectory := filepath.Join(dir, "skills", "user-skill")
	if err := os.MkdirAll(userDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	userBody := []byte("---\nname: user-skill\ndescription: User owned skill.\n---\nUser content\n")
	if err := os.WriteFile(filepath.Join(userDirectory, "SKILL.md"), userBody, 0o600); err != nil {
		t.Fatal(err)
	}
	updated := bundledskills.CDP()
	updated.Version = "20261107143548.0002"
	updated.Files["SKILL.md"] += "\nChanged bundled browser guidance.\n"
	updated.Files["references/javascript.md"] += "\nChanged reference.\n"
	var manifest skillbundle.Manifest
	if err := c.doJSON(t.Context(), "PUT", "/skills/bundled/cdp", updated, &manifest); err != nil {
		t.Fatal(err)
	}
	health, err := c.Health(t.Context())
	if err != nil || health.SkillBundleVersion != updated.Version || health.SkillBundleDigest != manifest.Digest {
		t.Fatalf("health omitted installed bundle: %+v %v", health, err)
	}
	fresh, err := c.GetSkill(t.Context(), id, "SKILL.md")
	if err != nil || fresh.Skill.Directory == oldDirectory || fresh.Skill.ID != id || !strings.Contains(string(fresh.Content), "Changed bundled") {
		t.Fatalf("upgrade: %+v %v", fresh, err)
	}
	// A second process running the older binary joins without downgrading the bundle.
	joined, err := EnsureWithOptions(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = joined.Close() }()
	stillFresh, err := joined.Client().GetSkill(t.Context(), id, "references/javascript.md")
	if err != nil || !strings.Contains(string(stillFresh.Content), "Changed reference") {
		t.Fatalf("downgraded reference: %v", err)
	}
	before, err := skillbundle.Current(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.doJSON(t.Context(), "PUT", "/skills/bundled/cdp", updated, &manifest); err != nil {
		t.Fatal(err)
	}
	after, _ := skillbundle.Current(dir)
	if before != after {
		t.Fatal("identical bundle changed generation")
	}
	if data, err := os.ReadFile(filepath.Join(oldDirectory, "SKILL.md")); err != nil || strings.Contains(string(data), "Changed bundled") {
		t.Fatal("old generation was modified")
	}
	if data, err := os.ReadFile(filepath.Join(userDirectory, "SKILL.md")); err != nil || string(data) != string(userBody) {
		t.Fatal("user skill was modified")
	}
	invalid := bundledskills.CDP()
	invalid.Version = "20261107143548.0003"
	invalid.Files["../../outside"] = "bad"
	if err := c.doJSON(t.Context(), "PUT", "/skills/bundled/cdp", invalid, &manifest); err == nil {
		t.Fatal("path escape accepted")
	}
	if current, _ := skillbundle.Current(dir); current != after {
		t.Fatal("rejected bundle changed active generation")
	}
}
