package qdriver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func namedCLI(t *testing.T, binary, root, id string) *cliFixture {
	t.Helper()
	f := &cliFixture{binary: binary, dataDir: root, session: id, state: filepath.Join(root, "sessions", id, "runtime.json")}
	t.Cleanup(func() {
		if _, err := os.Stat(f.state); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			reply, err := f.command(ctx, "stop")
			if err != nil || !reply.OK {
				t.Errorf("stop named session %s: %v %+v", id, err, reply.Error)
			}
		}
	})
	return f
}

func TestCLINamedSessionsAutoCreateGroupAndReuse(t *testing.T) {
	chrome := startChrome(t)
	baseline := chrome.pageIDs(t)
	base := buildCLI(t)
	root := t.TempDir()
	a, b := namedCLI(t, base.binary, root, "work-a"), namedCLI(t, base.binary, root, "work-b")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Named session</title><label for="name">Name</label><input id="name">`)
	}))
	t.Cleanup(server.Close)
	mainA := a.ok(t, "--endpoint", chrome.endpoint, "page", "new", "--name", "main", server.URL)
	mainB := b.ok(t, "--endpoint", chrome.endpoint, "page", "new", "--name", "main", server.URL)
	idA, idB := jsonField(t, mainA, "id"), jsonField(t, mainB, "id")
	if idA == idB || jsonField(t, mainA, "session") != "work-a" || jsonField(t, mainB, "session") != "work-b" {
		t.Fatalf("sessions shared page identity: %s %s", mainA, mainB)
	}
	for _, tc := range []struct {
		f  *cliFixture
		id string
	}{{a, idA}, {b, idB}} {
		var pages []struct{ ID, Session string }
		if err := json.Unmarshal(tc.f.ok(t, "page", "list"), &pages); err != nil {
			t.Fatal(err)
		}
		if len(pages) != 1 || pages[0].ID != tc.id || pages[0].Session != tc.f.session {
			t.Fatalf("page list escaped session %s: %+v", tc.f.session, pages)
		}
	}
	var all []struct{ ID string }
	if err := json.Unmarshal(a.ok(t, "page", "list", "--all"), &all); err != nil {
		t.Fatal(err)
	}
	if len(all) != len(baseline)+2 {
		t.Fatalf("--all omitted external browser pages: %+v", all)
	}
	ref := cliRef(t, a.ok(t, "page", "snapshot", "main"), "Name")
	a.ok(t, "page", "fill", "--ref", ref, "main", "Alpha")
	b.failed(t, "stale_ref", "page", "fill", "--ref", ref, "main", "wrong session")
	if got := string(b.ok(t, "page", "eval", "main", `document.querySelector('input').value`)); got != `""` {
		t.Fatalf("other session received input: %s", got)
	}
	a.ok(t, "task", "new", "group")
	group := a.ok(t, "page", "new", "--task", "group", "--name", "group-tab")
	idGroup := jsonField(t, group, "id")
	metadataPath := filepath.Join(root, "sessions", "work-a", "session.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		ID, State, Endpoint string
		Tasks               []string
		Pages               []struct{ Name, ID, Task, Session string }
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.ID != "work-a" || metadata.State != "running" || metadata.Endpoint != chrome.endpoint || len(metadata.Pages) != 2 || !slices.Contains(metadata.Tasks, "group") {
		t.Fatalf("app data did not persist grouped tabs: %s", data)
	}
	info := a.ok(t, "session", "info")
	if !strings.Contains(string(info), idA) || !strings.Contains(string(info), "group-tab") {
		t.Fatalf("session info lost saved grouping: %s", info)
	}
	var sessions []struct {
		ID      string
		Running bool
	}
	if err := json.Unmarshal(a.ok(t, "session", "list"), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || !sessions[0].Running || !sessions[1].Running {
		t.Fatalf("named session inventory: %+v", sessions)
	}
	before := a.ok(t, "status")
	ensured := a.ok(t, "session", "ensure")
	var ensureResult struct{ Created bool }
	if err := json.Unmarshal(ensured, &ensureResult); err != nil || ensureResult.Created {
		t.Fatalf("existing session was recreated: %s %v", ensured, err)
	}
	if jsonField(t, before, "instance") != jsonField(t, a.ok(t, "status"), "instance") {
		t.Fatal("normal invocation replaced running session")
	}
	a.ok(t, "start") // Named start is idempotent, unlike legacy explicit-file start.
	a.failed(t, "session_conflict", "start", "--command-timeout", "1s")
	a.failed(t, "session_conflict", "--endpoint", "http://127.0.0.1:1", "page", "new", "--name", "must-not-exist")
	a.ok(t, "stop")
	ids := chrome.pageIDs(t)
	if slices.Contains(ids, idA) || slices.Contains(ids, idGroup) || !slices.Contains(ids, idB) || !slices.Contains(ids, baseline[0]) {
		t.Fatalf("stopping one session crossed cleanup ownership: %v", ids)
	}
	stopped := a.ok(t, "session", "info")
	var stoppedInfo struct {
		Running bool
		State   string
	}
	if err := json.Unmarshal(stopped, &stoppedInfo); err != nil || stoppedInfo.Running || stoppedInfo.State != "stopped" {
		t.Fatalf("stop did not preserve session configuration: %s %v", stopped, err)
	}
	a.failed(t, "not_found", "status") // status must not auto-start a stopped session.
	a.ok(t, "page", "list")            // Saved endpoint restarts the absent runtime.
	if jsonField(t, before, "instance") == jsonField(t, a.ok(t, "status"), "instance") {
		t.Fatal("restart reused the old ref generation")
	}
	a.ok(t, "page", "new", "--name", "main", server.URL)
	a.failed(t, "stale_ref", "page", "fill", "--ref", ref, "main", "old generation")
	b.ok(t, "stop")
	a.ok(t, "stop")
	if ids := chrome.pageIDs(t); !slices.Equal(ids, baseline) {
		t.Fatalf("named session cleanup leaked pages: %v", ids)
	}
}

func TestCLINamedSessionConcurrentFirstUse(t *testing.T) {
	chrome := startChrome(t)
	base := buildCLI(t)
	f := namedCLI(t, base.binary, t.TempDir(), "parallel")
	start := make(chan struct{})
	results := make(chan cliReply, 2)
	failures := make(chan error, 2)
	for _, name := range []string{"first", "second"} {
		go func() {
			<-start
			reply, err := f.command(t.Context(), "--endpoint", chrome.endpoint, "page", "new", "--name", name)
			results <- reply
			failures <- err
		}()
	}
	close(start)
	ids := make(map[string]bool)
	for range 2 {
		reply := <-results
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
		if !reply.OK {
			t.Fatalf("concurrent ensure failed: %+v", reply.Error)
		}
		ids[jsonField(t, reply.Result, "id")] = true
	}
	if len(ids) != 2 {
		t.Fatal("two first-use commands were duplicated or lost")
	}
	var pages []struct{ ID string }
	if err := json.Unmarshal(f.ok(t, "page", "list"), &pages); err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("first-use clients did not converge on one runtime: %+v", pages)
	}
	var metadata struct{ Pages []struct{ ID string } }
	data, err := os.ReadFile(filepath.Join(f.dataDir, "sessions", f.session, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &metadata); err != nil || len(metadata.Pages) != 2 {
		t.Fatalf("concurrent grouping metadata: %s %v", data, err)
	}
	f.ok(t, "stop")
}

func TestCLINamedSessionValidationDoesNotStartProcesses(t *testing.T) {
	base := buildCLI(t)
	root := t.TempDir()
	f := namedCLI(t, base.binary, root, "absent")
	t.Setenv("QDRIVER_CHROME", filepath.Join(root, "missing-chrome.exe"))
	f.failed(t, "invalid_argument", "page", "list")
	f.failed(t, "invalid_argument", "--session", "../escape", "page", "list")
	f.failed(t, "invalid_argument", "--state", filepath.Join(root, "ambiguous.json"), "page", "list")
	if _, err := os.Stat(filepath.Join(root, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("failed validation created session state: %v", err)
	}
	if got := strings.TrimSpace(string(f.ok(t, "session", "list"))); got != "[]" {
		t.Fatalf("read-only list created sessions: %s", got)
	}
}
