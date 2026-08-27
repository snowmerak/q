// Command modulecheck checks distribution without publishing a tag or installing
// into the user's GOBIN. It builds a file module proxy from the current checkout,
// then runs a versioned go install with GOWORK=off in an isolated directory.
package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	qModule    = "github.com/snowmerak/q"
	sdkDir     = "third_party/acp-go-sdk"
	qVersion   = "v0.0.0-modulecheck"
	sdkVersion = "v0.0.0-modulecheck"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	rootBytes, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return err
	}
	root := strings.TrimSpace(string(rootBytes))
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	cmd.Dir = root
	listing, err := cmd.Output()
	if err != nil {
		return err
	}
	var files []string
	for _, name := range strings.Split(string(listing), "\x00") {
		if name == "" {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name)))
		if os.IsNotExist(err) {
			continue // Tracked deletion in the working tree.
		}
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	rootMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}
	if _, err := snapshotManifest("go.mod", rootMod); err != nil {
		return err
	}

	// All writes (including module cache, checksums and binaries) are disposable.
	// Never put unpublished module bytes in the user's shared module cache.
	tempDir, err := os.MkdirTemp("", "q-modulecheck-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	proxy := filepath.Join(tempDir, "proxy")
	for _, m := range []struct{ dir, module, version string }{
		{"", qModule, qVersion},
		{sdkDir, qModule + "/" + sdkDir, sdkVersion},
	} {
		if err := writeModule(proxy, root, m.dir, m.module, m.version, files); err != nil {
			return err
		}
	}
	goEnvBytes, err := exec.Command("go", "env", "-json", "GOPROXY", "GOMODCACHE", "GONOSUMDB").Output()
	if err != nil {
		return err
	}
	var goEnv map[string]string
	if err := json.Unmarshal(goEnvBytes, &goEnv); err != nil {
		return err
	}
	proxyChain := fileURL(proxy) + "|" + fileURL(filepath.Join(goEnv["GOMODCACHE"], "cache", "download"))
	if goEnv["GOPROXY"] != "" {
		proxyChain += "|" + goEnv["GOPROXY"]
	}
	env := childEnv(os.Environ(), map[string]string{
		"GOWORK": "off", "GOFLAGS": "-modcacherw", "GOTOOLCHAIN": "local",
		"GOOS": "", "GOARCH": "", // Install for this host, regardless of cross-build settings.
		"GOPROXY": proxyChain, "GONOPROXY": "none",
		"GONOSUMDB":  strings.Trim(goEnv["GONOSUMDB"]+","+qModule, ","),
		"GOMODCACHE": filepath.Join(tempDir, "modcache"),
		"GOBIN":      filepath.Join(tempDir, "bin"),
	})
	fmt.Printf("Checking versioned install without go.work: Q %s, SDK %s\n", qVersion, sdkVersion)
	install := exec.Command("go", "install", qModule+"/cmd/q@"+qVersion, qModule+"/cmd/q-mcp@"+qVersion)
	install.Dir, install.Env = tempDir, env
	install.Stdout, install.Stderr = os.Stdout, os.Stderr
	if err := install.Run(); err != nil {
		return fmt.Errorf("isolated versioned install: %w", err)
	}
	fmt.Println("PASS: q and q-mcp installed from module archives; nothing was published.")
	return nil
}

func fileURL(dir string) string {
	p := filepath.ToSlash(dir)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func childEnv(base []string, overrides map[string]string) []string {
	result := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, found := overrides[strings.ToUpper(key)]; !found {
			result = append(result, entry)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

// Module archives exclude nested modules, including the generator. Workspaces
// are a development aid, not a way to bundle another module into a release.
func moduleFiles(dir string, files []string) []string {
	var nested []string
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	for _, name := range files {
		if strings.HasPrefix(name, prefix) && path.Base(name) == "go.mod" && name != prefix+"go.mod" {
			nested = append(nested, path.Dir(name)+"/")
		}
	}
	var result []string
nextFile:
	for _, name := range files {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		for _, submodule := range nested {
			if strings.HasPrefix(name, submodule) {
				continue nextFile
			}
		}
		result = append(result, name)
	}
	return result
}

func writeModule(proxy, root, dir, module, version string, files []string) error {
	versionDir := filepath.Join(proxy, filepath.FromSlash(module), "@v")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		return err
	}
	mod, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), "go.mod"))
	if err != nil {
		return err
	}
	if module == qModule {
		mod, err = snapshotManifest("go.mod", mod)
		if err != nil {
			return err
		}
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for _, name := range moduleFiles(dir, files) {
		rel := name
		if dir != "" {
			rel = strings.TrimPrefix(name, dir+"/")
		}
		entry, err := zw.Create(module + "@" + version + "/" + rel)
		if err != nil {
			return err
		}
		if module == qModule && (rel == "go.mod" || rel == "go.sum") {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			if err != nil {
				return err
			}
			data, err = snapshotManifest(rel, data)
			if err != nil {
				return err
			}
			if _, err := entry.Write(data); err != nil {
				return err
			}
			continue
		}
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		_, err = io.Copy(entry, f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	info, err := json.Marshal(map[string]string{"Version": version, "Time": "2026-08-27T00:00:00Z"})
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{
		version + ".mod": mod, version + ".info": info,
		version + ".zip": archive.Bytes(), "list": []byte(version + "\n"),
	} {
		if err := os.WriteFile(filepath.Join(versionDir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot archives use a test-only SDK version, not a real published version
// with different contents. Rewrite manifests only in memory so the checkout's
// real pseudo-version and checksums remain intact.
func snapshotManifest(name string, data []byte) ([]byte, error) {
	module := qModule + "/" + sdkDir
	lines := strings.Split(string(data), "\n")
	var result []string
	found := false
	for _, line := range lines {
		fields := strings.Fields(line)
		if name == "go.sum" && len(fields) > 0 && fields[0] == module {
			continue
		}
		if name == "go.mod" {
			if len(fields) > 0 && fields[0] == "require" {
				fields = fields[1:]
			}
			if len(fields) >= 2 && fields[0] == module {
				line = strings.Replace(line, fields[1], sdkVersion, 1)
				found = true
			}
		}
		result = append(result, line)
	}
	if name == "go.mod" && !found {
		return nil, fmt.Errorf("go.mod must require a published SDK version; run go mod tidy after publishing the SDK")
	}
	return []byte(strings.Join(result, "\n")), nil
}
