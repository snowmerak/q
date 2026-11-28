package qdriver_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIChromeLaunchInputRestartAndCleanup(t *testing.T) {
	executable := chromeExecutable(t)
	base := buildCLI(t)
	f := namedCLI(t, base.binary, t.TempDir(), "launched")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Managed Chrome</title><label for="name">Name</label><input id="name"><button onclick="document.querySelector('output').textContent=document.querySelector('input').value">Save</button><output></output>`)
	}))
	t.Cleanup(server.Close)
	f.ok(t, "--chrome", executable, "--headless", "page", "new", "--name", "main", server.URL)
	snapshot := f.ok(t, "page", "snapshot", "main")
	f.ok(t, "page", "fill", "--ref", cliRef(t, snapshot, "Name"), "main", "Chrome 자동 준비")
	f.ok(t, "page", "click", "--ref", cliRef(t, snapshot, "Save"), "main")
	var output string
	if err := json.Unmarshal(f.ok(t, "page", "eval", "main", "document.querySelector('output').textContent"), &output); err != nil || output != "Chrome 자동 준비" {
		t.Fatalf("managed browser input did not reach app: %s %v", output, err)
	}
	f.ok(t, "page", "eval", "main", `localStorage.setItem('retained','yes')`)
	before := f.ok(t, "status")
	var status struct {
		Browser struct {
			PID               int
			Managed           bool
			Profile, Endpoint string
		}
	}
	if err := json.Unmarshal(before, &status); err != nil || !status.Browser.Managed || status.Browser.PID <= 0 || status.Browser.Endpoint == "" {
		t.Fatalf("missing browser lifecycle information: %s %v", before, err)
	}
	profile := filepath.Join(f.dataDir, "sessions", f.session, "chrome-profile")
	if status.Browser.Profile != profile {
		t.Fatalf("browser profile escaped selected session: %+v", status.Browser)
	}
	f.ok(t, "start")
	if jsonField(t, before, "instance") != jsonField(t, f.ok(t, "status"), "instance") {
		t.Fatal("reused session launched a second browser")
	}
	f.failed(t, "session_conflict", "--headless=false", "page", "list")
	f.failed(t, "session_conflict", "--endpoint", status.Browser.Endpoint, "page", "list")
	f.failed(t, "invalid_argument", "--chrome", executable, "--endpoint", status.Browser.Endpoint, "page", "list")
	f.ok(t, "stop")
	assertBrowserEndpointClosed(t, status.Browser.Endpoint)
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.state), "browser.lock")); !os.IsNotExist(err) {
		t.Fatalf("normal stop retained browser lock: %v", err)
	}
	if _, err := os.Stat(profile); err != nil {
		t.Fatalf("stop deleted persistent profile: %v", err)
	}
	f.ok(t, "page", "new", "--name", "main", server.URL) // Saved path/mode, no endpoint.
	if got := string(f.ok(t, "page", "eval", "main", `localStorage.getItem('retained')`)); got != `"yes"` {
		t.Fatalf("restart lost session profile: %s", got)
	}
	if jsonField(t, before, "instance") == jsonField(t, f.ok(t, "status"), "instance") {
		t.Fatal("restart retained old runtime generation")
	}
	f.ok(t, "stop")
}

func TestCLIAutoChromeDiscoveryAndSavedSettings(t *testing.T) {
	executable := chromeExecutable(t)
	base := buildCLI(t)
	f := namedCLI(t, base.binary, t.TempDir(), "auto-chrome")
	t.Setenv("QDRIVER_CHROME", executable)
	f.ok(t, "--headless", "page", "new", "--name", "main", "data:text/html,<title>Auto Chrome</title><button>Ready</button>")
	if title := string(f.ok(t, "page", "title", "main")); title != `"Auto Chrome"` {
		t.Fatalf("automatic browser setup did not load page: %s", title)
	}
	t.Setenv("QDRIVER_CHROME", filepath.Join(f.dataDir, "now-missing.exe"))
	f.ok(t, "page", "snapshot", "main") // Saved configuration outranks discovery.
	f.ok(t, "stop")
	f.ok(t, "session", "ensure")
	f.ok(t, "stop")
	// Exercise PATH lookup without an environment-selected Chrome. Reuse the
	// actual installation directory so the discovered browser has its resources.
	t.Setenv("QDRIVER_CHROME", "")
	t.Setenv("PATH", filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if name := filepath.Base(executable); name == "msedge.exe" || name == "msedge" {
		return // Edge is supported explicitly but is not an automatic Chrome choice.
	}
	other := namedCLI(t, base.binary, t.TempDir(), "path-chrome")
	other.ok(t, "--headless", "session", "ensure")
	other.ok(t, "stop")
}

func TestCLIChromeHeadedLaunchAndProfileIsolation(t *testing.T) {
	if os.Getenv("QDRIVER_HEADED_INTEGRATION") != "1" {
		t.Skip("set QDRIVER_HEADED_INTEGRATION=1 on a desktop to exercise visible Chrome")
	}
	executable := chromeExecutable(t)
	base := buildCLI(t)
	root := t.TempDir()
	a, b := namedCLI(t, base.binary, root, "visible"), namedCLI(t, base.binary, root, "headless")
	a.ok(t, "--chrome", executable, "page", "new", "--name", "main", "data:text/html,<title>Visible Chrome</title>")
	b.ok(t, "--chrome", executable, "--headless", "page", "new", "--name", "main")
	if got := string(a.ok(t, "page", "eval", "main", "navigator.webdriver")); got != "false" {
		t.Fatalf("visible Chrome advertises webdriver automation: %s", got)
	}
	if got := string(b.ok(t, "page", "eval", "main", "!Object.hasOwn(navigator,'webdriver') && Function.prototype.toString.call(Object.getOwnPropertyDescriptor(Navigator.prototype,'webdriver').get).includes('[native code]')")); got != "true" {
		t.Fatalf("webdriver should retain the native getter: %s", got)
	}
	var infoA, infoB struct {
		Browser struct {
			Profile  string
			Headless bool
		}
	}
	if err := json.Unmarshal(a.ok(t, "session", "info"), &infoA); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b.ok(t, "session", "info"), &infoB); err != nil {
		t.Fatal(err)
	}
	if infoA.Browser.Profile == "" || infoA.Browser.Profile == infoB.Browser.Profile || infoA.Browser.Headless || !infoB.Browser.Headless {
		t.Fatalf("browser launch modes/profile isolation: %+v %+v", infoA, infoB)
	}
	a.ok(t, "stop")
	b.ok(t, "page", "snapshot", "main")
	b.ok(t, "stop")
	// A stopped session may explicitly change its saved launch mode.
	a.ok(t, "--headless", "session", "ensure")
	a.ok(t, "stop")
}

func assertBrowserEndpointClosed(t *testing.T, endpoint string) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint+"/json/version", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err == nil {
		_ = response.Body.Close()
		t.Fatal("stop left its owned browser endpoint running")
	}
}

func TestCLIInputModePersistenceAndConflict(t *testing.T) {
	chrome := startChrome(t)
	base := buildCLI(t)
	f := namedCLI(t, base.binary, t.TempDir(), "input-mode")
	f.failed(t, "invalid_argument", "--input-mode", "unknown", "page", "list")
	f.ok(t, "--endpoint", chrome.endpoint, "start", "--input-mode", "fast")
	if got := jsonField(t, f.ok(t, "status"), "inputMode"); got != "fast" {
		t.Fatalf("fast mode was not selected: %s", got)
	}
	f.failed(t, "session_conflict", "--input-mode", "paced", "page", "list")
	f.ok(t, "stop")
	f.ok(t, "session", "ensure")
	if got := jsonField(t, f.ok(t, "status"), "inputMode"); got != "fast" {
		t.Fatalf("restart lost saved input mode: %s", got)
	}
	f.ok(t, "page", "new", "--name", "main", `data:text/html,<input id="text"><script>window.keys=0;document.addEventListener('keydown',()=>keys++)</script>`)
	f.ok(t, "page", "fill", "--css", "#text", "main", "abc")
	if got := string(f.ok(t, "page", "eval", "main", "keys")); got != "3" {
		t.Fatalf("fast mode skipped physical key events: %s", got)
	}
	f.ok(t, "stop")
	f.ok(t, "--input-mode", "paced", "session", "ensure")
	if got := jsonField(t, f.ok(t, "session", "info"), "inputMode"); got != "paced" {
		t.Fatalf("explicit pacing change was not persisted: %s", got)
	}
	f.ok(t, "stop")
}

func TestCLIChromeConcurrentLaunchAndFailureCleanup(t *testing.T) {
	executable := chromeExecutable(t)
	base := buildCLI(t)
	f := namedCLI(t, base.binary, t.TempDir(), "concurrent-chrome")
	results := make(chan cliReply, 2)
	failures := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		go func() {
			reply, err := f.command(t.Context(), "--chrome", executable, "--headless", "page", "new", "--name", name)
			results <- reply
			failures <- err
		}()
	}
	for range 2 {
		reply := <-results
		if err := <-failures; err != nil || !reply.OK {
			t.Fatalf("concurrent Chrome startup: %v %+v", err, reply.Error)
		}
	}
	var pages []struct{ ID string }
	if err := json.Unmarshal(f.ok(t, "page", "list"), &pages); err != nil || len(pages) != 2 {
		t.Fatalf("concurrent launch lost commands: %+v %v", pages, err)
	}
	f.ok(t, "stop")
	bad := namedCLI(t, base.binary, t.TempDir(), "bad-chrome")
	bad.failed(t, "invalid_argument", "--chrome", filepath.Join(bad.dataDir, "missing.exe"), "page", "list")
	bad.failed(t, "invalid_argument", "--headless", "--endpoint", "http://127.0.0.1:1", "page", "list")
	bad.failed(t, "operation_failed", "start", "--chrome", base.binary, "--headless") // Executable, but not Chrome.
	for _, path := range []string{bad.state, filepath.Join(filepath.Dir(bad.state), "browser.lock")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed startup retained runtime/lock %s: %v", path, err)
		}
	}
	bad.ok(t, "--chrome", executable, "--headless", "session", "ensure")
	bad.ok(t, "stop")
	timed := namedCLI(t, base.binary, t.TempDir(), "timed-chrome")
	reply, err := timed.command(t.Context(), "--timeout", "1ms", "--chrome", executable, "--headless", "session", "ensure")
	if err != nil || reply.OK {
		t.Fatalf("startup deadline did not fail: %v %+v", err, reply)
	}
	for _, path := range []string{timed.state, filepath.Join(filepath.Dir(timed.state), "browser.lock")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("startup deadline retained runtime/lock %s: %v", path, err)
		}
	}
	timed.ok(t, "--chrome", executable, "--headless", "session", "ensure")
	timed.ok(t, "stop")
}
