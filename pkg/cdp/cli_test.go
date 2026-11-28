package qdriver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	qdriver "github.com/snowmerak/q/v2/pkg/cdp"
)

type cliReply struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code           string `json:"code"`
		Message        string `json:"message"`
		MayHaveEffects bool   `json:"mayHaveEffects"`
	} `json:"error"`
}
type cliFixture struct {
	binary, state, dataDir, session string
	prefix                          []string
}

func buildCLI(t *testing.T) *cliFixture {
	t.Helper()
	f := &cliFixture{binary: filepath.Join(t.TempDir(), "cdp-q-driver.exe"), state: filepath.Join(t.TempDir(), "session.json")}
	command := exec.CommandContext(t.Context(), "go", "build", "-o", f.binary, "github.com/snowmerak/q/v2/cmd/cdp-q-driver")
	hideWindow(command)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(f.state); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, f.binary, "--state", f.state, "stop")
			hideWindow(cmd)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("stop CLI fixture: %v\n%s", err, output)
			}
		}
	})
	return f
}
func (f *cliFixture) command(ctx context.Context, args ...string) (cliReply, error) {
	prefix := append(append([]string{}, f.prefix...), "--timeout", "8s")
	if f.session != "" {
		prefix = append(prefix, "--data-dir", f.dataDir, "--session", f.session)
	} else {
		prefix = append(prefix, "--state", f.state)
	}
	cmd := exec.CommandContext(ctx, f.binary, append(prefix, args...)...)
	hideWindow(cmd)
	output, runErr := cmd.CombinedOutput()
	var reply cliReply
	if err := json.Unmarshal(output, &reply); err != nil {
		return reply, fmt.Errorf("decode CLI %v: %w; process=%v; output=%s", args, err, runErr, output)
	}
	if (runErr == nil) != reply.OK {
		return reply, fmt.Errorf("CLI exit/output disagreed: process=%v output=%s", runErr, output)
	}
	return reply, nil
}
func (f *cliFixture) ok(t *testing.T, args ...string) json.RawMessage {
	t.Helper()
	reply, err := f.command(t.Context(), args...)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.OK {
		t.Fatalf("CLI %v failed: %+v", args, reply.Error)
	}
	return reply.Result
}
func (f *cliFixture) failed(t *testing.T, code string, args ...string) cliReply {
	t.Helper()
	reply, err := f.command(t.Context(), args...)
	if err != nil {
		t.Fatal(err)
	}
	if reply.OK || reply.Error == nil || reply.Error.Code != code {
		t.Fatalf("CLI %v expected %s: error=%+v result=%s", args, code, reply.Error, reply.Result)
	}
	return reply
}
func jsonField(t *testing.T, data json.RawMessage, key string) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := json.Unmarshal(fields[key], &value); err != nil {
		t.Fatalf("string field %s in %s: %v", key, data, err)
	}
	return value
}
func cliRef(t *testing.T, data json.RawMessage, name string) string {
	t.Helper()
	var snapshot struct{ Nodes []struct{ Name, Ref string } }
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, node := range snapshot.Nodes {
		if node.Name == name && node.Ref != "" {
			return node.Ref
		}
	}
	t.Fatalf("missing CLI ref for %s: %s", name, data)
	return ""
}

func TestCLIStatefulWorkflowAndRefProvenance(t *testing.T) {
	chrome := startChrome(t)
	baseline := chrome.pageIDs(t)
	cli := buildCLI(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/child" {
			_, _ = fmt.Fprint(w, `<!doctype html><label for="child">Child name</label><input id="child">`)
			return
		}
		_, _ = fmt.Fprint(w, `<!doctype html><title>CLI fixture</title><label for="name">Name</label><input id="name"><button id="save" onclick="document.querySelector('output').textContent=document.querySelector('input').value">Save</button><output></output><iframe name="local" src="/child"></iframe>`)
	}))
	t.Cleanup(server.Close)
	cli.ok(t, "start", "--endpoint", chrome.endpoint)
	cli.failed(t, "already_exists", "start", "--endpoint", chrome.endpoint)
	cli.ok(t, "task", "new", "demo")
	main := cli.ok(t, "page", "new", "--task", "demo", "--name", "main", server.URL)
	mainID := jsonField(t, main, "id")
	snapshot := cli.ok(t, "page", "snapshot", "--scope", "full_page", "main")
	name, save := cliRef(t, snapshot, "Name"), cliRef(t, snapshot, "Save")
	cli.ok(t, "page", "fill", "--ref", name, "main", "Ada 안녕")
	cli.ok(t, "page", "click", "--ref", save, "main")
	var filled string
	if err := json.Unmarshal(cli.ok(t, "page", "eval", "main", `document.querySelector('output').textContent`), &filled); err != nil || filled != "Ada 안녕" {
		t.Fatalf("CLI ref input result: %q %v", filled, err)
	}
	var frames []qdriver.FrameInfo
	if err := json.Unmarshal(cli.ok(t, "page", "frames", "main"), &frames); err != nil {
		t.Fatal(err)
	}
	for _, frame := range frames {
		if frame.Name == "local" {
			cli.ok(t, "page", "fill", "--css", "#child", "--frame", frame.ID, "main", "nested")
		}
	}
	if got := string(cli.ok(t, "page", "eval", "main", `document.querySelector('iframe').contentDocument.querySelector('input').value`)); got != `"nested"` {
		t.Fatal(got)
	}
	// Evaluate invalidates SDK refs; a delivered token must not fall back to
	// a similarly named control after that invalidation.
	cli.failed(t, "stale_ref", "page", "click", "--ref", save, "main")
	snapshot = cli.ok(t, "page", "snapshot", "main")
	save = cliRef(t, snapshot, "Save")
	second := cli.ok(t, "page", "new", "--task", "demo", "--name", "second", server.URL)
	secondID := jsonField(t, second, "id")
	cli.failed(t, "wrong_page", "page", "click", "--ref", save, "second")
	cli.failed(t, "stale_ref", "page", "click", "--ref", "@1", "second")
	oldRef := cliRef(t, cli.ok(t, "page", "snapshot", "second"), "Name")
	path := filepath.Join(t.TempDir(), "screenshot.png")
	cli.ok(t, "page", "screenshot", "main", path)
	image, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(image)); err != nil {
		t.Fatal(err)
	}
	cli.failed(t, "already_exists", "page", "screenshot", "main", path)
	cli.ok(t, "page", "adopt", "--task", "demo", "--name", "borrowed", baseline[0])
	cli.failed(t, "not_owned", "page", "close", "borrowed")
	released := cli.ok(t, "page", "release", "borrowed")
	var releasedView struct{ Managed bool }
	if err := json.Unmarshal(released, &releasedView); err != nil || releasedView.Managed {
		t.Fatalf("release reported a managed page: %s %v", released, err)
	}
	cli.ok(t, "page", "adopt", "--task", "demo", "--name", "borrowed", baseline[0])
	cli.ok(t, "page", "new", "--task", "demo", "--name", "temporary")
	closed := cli.ok(t, "page", "close", "temporary")
	if err := json.Unmarshal(closed, &releasedView); err != nil || releasedView.Managed {
		t.Fatalf("close reported a managed page: %s %v", closed, err)
	}
	cli.ok(t, "task", "finish", "--keep", "second", "demo")
	ids := chrome.pageIDs(t)
	if slices.Contains(ids, mainID) || !slices.Contains(ids, secondID) || !slices.Contains(ids, baseline[0]) {
		t.Fatalf("CLI cleanup provenance: %v", ids)
	}
	cli.ok(t, "stop")
	if _, err := os.Stat(cli.state); !os.IsNotExist(err) {
		t.Fatalf("CLI left its session file: %v", err)
	}
	cli.ok(t, "start", "--endpoint", chrome.endpoint)
	cli.ok(t, "page", "adopt", "--name", "second", secondID)
	cli.failed(t, "stale_ref", "page", "fill", "--ref", oldRef, "second", "wrong generation")
	cli.ok(t, "stop")
	if !slices.Contains(chrome.pageIDs(t), secondID) {
		t.Fatal("restart turned a borrowed kept page into an owned page")
	}
}

func TestCLIEventsDialogsAndFiles(t *testing.T) {
	chrome := startChrome(t)
	baseline := chrome.pageIDs(t)
	cli := buildCLI(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/file" {
			w.Header().Set("Content-Disposition", `attachment; filename="cli.txt"`)
			_, _ = fmt.Fprint(w, "CLI download 안녕")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/popup" {
			_, _ = fmt.Fprint(w, `<!doctype html><title>CLI popup</title><h1>Ready</h1>`)
			return
		}
		_, _ = fmt.Fprint(w, `<!doctype html><button id="popup" onclick="window.open('/popup','_blank')">Popup</button><button id="prompt" onclick="window.answer=prompt('Name?')">Prompt</button><button id="fetch" onclick="fetch('/request')">Fetch</button><input type="file" id="upload"><a id="download" href="/file">Download</a>`)
	}))
	t.Cleanup(server.Close)
	cli.ok(t, "start", "--endpoint", chrome.endpoint)
	cli.ok(t, "page", "new", "--name", "main", server.URL)
	popupSub := jsonField(t, cli.ok(t, "events", "arm", "--kind", "popup", "main"), "subscription")
	// A Next RPC remains independent of commands that cause its event.
	next := make(chan cliReply, 1)
	nextErr := make(chan error, 1)
	go func() { r, e := cli.command(t.Context(), "events", "next", popupSub); next <- r; nextErr <- e }()
	cli.ok(t, "page", "click", "--css", "#popup", "main")
	reply := <-next
	if err := <-nextErr; err != nil {
		t.Fatal(err)
	}
	if !reply.OK {
		t.Fatalf("pending popup event: %+v", reply.Error)
	}
	popupHandle := jsonField(t, reply.Result, "handle")
	cli.ok(t, "popup", "adopt", "--name", "popup", popupHandle)
	cli.ok(t, "page", "wait-selector", "--css", "h1", "popup")
	if got := string(cli.ok(t, "page", "title", "popup")); got != `"CLI popup"` {
		t.Fatal(got)
	}
	dialogSub := jsonField(t, cli.ok(t, "events", "arm", "--kind", "dialog", "main"), "subscription")
	clicked, err := cli.command(t.Context(), "page", "click", "--css", "#prompt", "main")
	if err != nil {
		t.Fatal(err)
	}
	if !clicked.OK && (clicked.Error == nil || clicked.Error.Code != "dialog_open") {
		t.Fatalf("prompt click: %+v", clicked.Error)
	}
	dialog := jsonField(t, cli.ok(t, "events", "next", dialogSub), "handle")
	current := jsonField(t, cli.ok(t, "dialog", "current", "main"), "handle")
	if current != dialog {
		t.Fatal("current dialog changed handle identity")
	}
	cli.ok(t, "dialog", "accept", dialog, "CLI Ada")
	cli.ok(t, "page", "wait-function", "main", `window.answer==='CLI Ada'`)
	cli.failed(t, "not_found", "dialog", "accept", dialog, "wrong")
	cli.ok(t, "chooser", "interception", "main", "on")
	chooserSub := jsonField(t, cli.ok(t, "events", "arm", "--kind", "fileChooser", "main"), "subscription")
	cli.ok(t, "page", "click", "--css", "#upload", "main")
	chooser := jsonField(t, cli.ok(t, "events", "next", chooserSub), "handle")
	file := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(file, []byte("CLI upload"), 0600); err != nil {
		t.Fatal(err)
	}
	cli.ok(t, "chooser", "set", chooser, file)
	if got := string(cli.ok(t, "page", "eval", "main", `document.querySelector('input').files[0].name`)); got != `"upload.txt"` {
		t.Fatal(got)
	}
	cli.ok(t, "page", "upload", "--css", "#upload", "main")
	if got := string(cli.ok(t, "page", "eval", "main", `document.querySelector('input').files.length`)); got != "0" {
		t.Fatal(got)
	}
	requests := jsonField(t, cli.ok(t, "events", "arm", "--kind", "request", "main"), "subscription")
	cli.ok(t, "page", "click", "--css", "#fetch", "main")
	if !strings.Contains(string(cli.ok(t, "events", "next", requests)), "/request") {
		t.Fatal("request event was not routed")
	}
	cli.ok(t, "page", "wait-idle", "--idle", "30ms", "main")
	dir := t.TempDir()
	cli.ok(t, "download", "enable", dir)
	downloadSub := jsonField(t, cli.ok(t, "events", "arm", "--kind", "download", "main"), "subscription")
	cli.ok(t, "page", "click", "--css", "#download", "main")
	download := jsonField(t, cli.ok(t, "events", "next", downloadSub), "handle")
	cli.ok(t, "download", "wait", download)
	saved := filepath.Join(t.TempDir(), "saved.txt")
	cli.ok(t, "download", "save", download, saved)
	cli.failed(t, "already_exists", "download", "save", download, saved)
	data, err := os.ReadFile(saved)
	if err != nil || string(data) != "CLI download 안녕" {
		t.Fatalf("CLI saved file: %q %v", data, err)
	}
	cli.ok(t, "events", "close", downloadSub)
	cli.ok(t, "stop")
	if got := chrome.pageIDs(t); !slices.Equal(got, baseline) {
		t.Fatalf("CLI stop leaked owned pages: %v", got)
	}
}

func TestCLIForegroundAuthenticationTimeoutAndShutdown(t *testing.T) {
	chrome := startChrome(t)
	cli := buildCLI(t)
	var log bytes.Buffer
	cmd := exec.Command(cli.binary, "--state", cli.state, "serve", "--endpoint", chrome.endpoint)
	hideWindow(cmd)
	cmd.Stdout = &log
	cmd.Stderr = &log
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	var exitErr error
	go func() { exitErr = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		select {
		case <-exited:
			return
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = cli.command(ctx, "stop")
		select {
		case <-exited:
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-exited
			t.Errorf("foreground CLI required termination: %s", log.String())
		}
	})
	var discovery struct {
		Address, Token, Instance string
		PID                      int
	}
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		data, err := os.ReadFile(cli.state)
		if err == nil && json.Unmarshal(data, &discovery) == nil && discovery.Address != "" {
			break
		}
		select {
		case <-exited:
			t.Fatalf("serve failed: %v %s", exitErr, log.String())
		case <-deadline.C:
			t.Fatal("serve never became ready")
		case <-tick.C:
		}
	}
	if !strings.HasPrefix(discovery.Address, "127.0.0.1:") || len(discovery.Token) != 64 || discovery.PID != cmd.Process.Pid {
		t.Fatalf("invalid local capability metadata: address=%s pid=%d", discovery.Address, discovery.PID)
	}
	for _, tc := range []struct {
		name, token, origin, host, body string
		status                          int
	}{
		{"missing token", "", "", "", `{"command":"status","timeoutMs":1000}`, 401},
		{"browser origin", discovery.Token, "https://example.test", "", `{"command":"status","timeoutMs":1000}`, 401},
		{"wrong host", discovery.Token, "", "example.test", `{"command":"status","timeoutMs":1000}`, 401},
		{"unknown fields", discovery.Token, "", "", `{"command":"status","timeoutMs":1000,"surprise":true}`, 400},
		{"valid", discovery.Token, "", "", `{"command":"status","timeoutMs":1000}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+discovery.Address+"/rpc", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.host != "" {
				req.Host = tc.host
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != tc.status || bytes.Contains(body, []byte(discovery.Token)) {
				t.Fatalf("RPC status=%d body=%s", res.StatusCode, body)
			}
		})
	}
	cli.ok(t, "page", "new", "--name", "main", `data:text/html,<button id="open" onclick="window.open('about:blank','_blank')">Open</button>`)
	sub := jsonField(t, cli.ok(t, "events", "arm", "--kind", "popup", "main"), "subscription")
	timed := cli.failed(t, "deadline", "--timeout", "60ms", "events", "next", sub)
	if timed.Error.MayHaveEffects {
		t.Fatal("waiting for an event was classified as browser input")
	}
	cli.ok(t, "page", "click", "--css", "#open", "main")
	cli.ok(t, "events", "next", sub) // The per-wait timeout did not close the subscription.
	unknown := cli.failed(t, "deadline", "--timeout", "100ms", "page", "eval", "main", `window.sent=(window.sent||0)+1;new Promise(()=>{})`)
	if !unknown.Error.MayHaveEffects {
		t.Fatal("timed-out evaluation hid possible effects")
	}
	if got := string(cli.ok(t, "page", "eval", "main", `window.sent`)); got != "1" {
		t.Fatalf("mutation was replayed or not sent: %s", got)
	}
	cli.ok(t, "stop")
	select {
	case <-exited:
		if exitErr != nil {
			t.Fatalf("serve exited unsuccessfully: %v %s", exitErr, log.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stop removed discovery but did not stop the foreground process")
	}
	cli.failed(t, "not_found", "status")
	cli.failed(t, "operation_failed", "start", "--endpoint", "http://127.0.0.1:1")
	if _, err := os.Stat(cli.state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed attachment leaked session reservation: %v", err)
	}
}
