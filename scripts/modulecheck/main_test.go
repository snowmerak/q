package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestModuleFilesExcludesNestedModules(t *testing.T) {
	files := []string{"go.mod", "go.work", "cmd/q/main.go", sdkDir + "/go.mod", sdkDir + "/agent.go", sdkDir + "/cmd/generate/go.mod", sdkDir + "/cmd/generate/main.go"}
	for _, tc := range []struct {
		dir  string
		want []string
	}{
		{"", files[:3]},
		{sdkDir, files[3:5]},
	} {
		if got := moduleFiles(tc.dir, files); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("module %q: got %v, want %v", tc.dir, got, tc.want)
		}
	}
}

func TestChildEnvReplacesWindowsNames(t *testing.T) {
	got := childEnv([]string{"GoWork=old", "Path=keep", "GOBIN=old"}, map[string]string{"GOWORK": "off", "GOBIN": "new"})
	values := make(map[string]bool)
	for _, value := range got {
		values[value] = true
	}
	if len(got) != 3 || !values["GOWORK=off"] || !values["GOBIN=new"] || !values["Path=keep"] {
		t.Fatal(got)
	}
}

func TestWriteModuleArchive(t *testing.T) {
	root, proxy := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, sdkDir), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"go.mod": "module " + qModule + "\nrequire " + qModule + "/" + sdkDir + " v0.0.0-20260827012000-abcdef123456\n", "main.go": "package main\n", sdkDir + "/go.mod": "module " + qModule + "/" + sdkDir + "\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeModule(proxy, root, "", qModule, qVersion, []string{"go.mod", "main.go", sdkDir + "/go.mod"}); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(filepath.Join(proxy, qModule, "@v", qVersion+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != 2 {
		t.Fatalf("nested module included: %v", zr.File)
	}
	if zr.File[0].Name != qModule+"@"+qVersion+"/go.mod" {
		t.Fatal(zr.File[0].Name)
	}
}

func TestSnapshotManifest(t *testing.T) {
	module := qModule + "/" + sdkDir
	for _, input := range []string{
		"require " + module + " v0.0.0-20260827012000-abcdef123456\n",
		"require (\n\t" + module + "\tv0.13.5-q.1 // pinned\n)\n",
	} {
		data, err := snapshotManifest("go.mod", []byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), sdkVersion) || strings.Contains(string(data), "abcdef123456") || strings.Contains(string(data), "v0.13.5-q.1") {
			t.Fatalf("snapshot requirement not replaced: %s", data)
		}
	}
	if _, err := snapshotManifest("go.mod", []byte("module "+qModule+"\n")); err == nil {
		t.Fatal("missing SDK requirement must be rejected")
	}
	keep := "example.com/keep v1.0.0 h1:keep\n"
	data, err := snapshotManifest("go.sum", []byte(module+" v0.0.0-old h1:old\n"+module+" v0.0.0-old/go.mod h1:oldmod\n"+keep))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != keep {
		t.Fatalf("wrong checksum filtering: %q", data)
	}
}
