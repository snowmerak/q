package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is needed for script runner tests")
	}
}

func TestScriptPureExecutionAndValidation(t *testing.T) {
	requireNode(t)
	options := appOptions{Session: "script", State: filepath.Join(t.TempDir(), "absent.json"), Timeout: 3 * time.Second}
	var logs bytes.Buffer
	result, err := runScript(t.Context(), options, []string{"-", "한글"}, strings.NewReader(`console.log('trace'); await new Promise(r=>setTimeout(r,10)); return {name:session.name,arg:session.args[0],fs:typeof require('node:fs').readFileSync};`), &logs)
	if err != nil || string(result) != `{"name":"script","arg":"한글","fs":"function"}` || strings.TrimSpace(logs.String()) != "trace" {
		t.Fatalf("pure script: %s %v logs=%q", result, err, logs.String())
	}
	if _, err := os.Stat(options.State); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pure script created browser state: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "helper.cjs"), []byte(`console.log('module trace');module.exports=27;`), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "workflow.js")
	if err := os.WriteFile(path, []byte(`return require('./helper.cjs');`), 0600); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	result, err = runScript(t.Context(), options, []string{path}, strings.NewReader(""), &logs)
	if err != nil || string(result) != "27" || strings.TrimSpace(logs.String()) != "module trace" {
		t.Fatalf("relative require/module logs: %s %v %q", result, err, logs.String())
	}
	for _, source := range []string{"throw new Error('test failure')", "throw null", "return (", "return 1n", "const x={};x.x=x;return x"} {
		_, err := runScript(t.Context(), options, []string{"-"}, strings.NewReader(source), &logs)
		var detail *commandError
		if !errors.As(err, &detail) || detail.Code != "script_error" || detail.MayHaveEffects {
			t.Fatalf("script error contract: %q %+v %v", source, detail, err)
		}
	}
	result, err = runScript(t.Context(), options, []string{"-"}, strings.NewReader(`try { await session.command('stop'); } catch(e) { return e.code; }`), &logs)
	if err != nil || string(result) != `"invalid_argument"` {
		t.Fatalf("session lifecycle escaped runner: %s %v", result, err)
	}
	_, err = runScript(t.Context(), options, []string{"--node", filepath.Join(t.TempDir(), "absent-node"), "-"}, strings.NewReader("return 1"), &logs)
	var missing *commandError
	if !errors.As(err, &missing) || missing.Code != "node_not_found" {
		t.Fatalf("missing Node diagnostic: %v", err)
	}
}

func TestScriptDeadlineKillsBusyRunner(t *testing.T) {
	requireNode(t)
	start := time.Now()
	_, err := runScript(t.Context(), appOptions{Timeout: 250 * time.Millisecond}, []string{"-"}, strings.NewReader("while(true) {}"), &bytes.Buffer{})
	var detail *commandError
	if !errors.As(err, &detail) || detail.Code != "deadline" || detail.MayHaveEffects || time.Since(start) > 3*time.Second {
		t.Fatalf("busy runner did not honor deadline: %+v %v (%s)", detail, err, time.Since(start))
	}
}

func TestScriptMalformedOutputReapsRunner(t *testing.T) {
	requireNode(t)
	start := time.Now()
	_, err := runScript(t.Context(), appOptions{Timeout: 3 * time.Second}, []string{"-"}, strings.NewReader(`process.stdout.write('invalid\n'+'x'.repeat(100000));return 1;`), &bytes.Buffer{})
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("corrupted protocol stalled runner cleanup: %v (%s)", err, time.Since(start))
	}
}

func TestScriptRPCFailureAndEarlierEffects(t *testing.T) {
	requireNode(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
			t.Error("runner lost session authentication")
		}
		_ = json.NewEncoder(w).Encode(response{Error: &commandError{Code: "not_actionable", Message: "blocked", MayHaveEffects: true}, Result: map[string]bool{"InputStarted": true, "InputCompleted": false}})
	}))
	defer server.Close()
	state := filepath.Join(t.TempDir(), "runtime.json")
	data, err := json.Marshal(discovery{Version: protocolVersion, Address: strings.TrimPrefix(server.URL, "http://"), Token: strings.Repeat("a", 64), Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, data, 0600); err != nil {
		t.Fatal(err)
	}
	options := appOptions{State: state, Timeout: 3 * time.Second}
	var logs bytes.Buffer
	source := `try { await session.page('main').click('#target'); } catch(e) { return {code:e.code,effects:e.mayHaveEffects,started:e.result.InputStarted}; }`
	result, err := runScript(t.Context(), options, []string{"-"}, strings.NewReader(source), &logs)
	if err != nil || string(result) != `{"code":"not_actionable","effects":true,"started":true}` {
		t.Fatalf("catchable driver error: %s %v", result, err)
	}
	result, err = runScript(t.Context(), options, []string{"-"}, strings.NewReader(`await session.page('main').click('#target')`), &logs)
	var detail *commandError
	if !errors.As(err, &detail) || detail.Code != "not_actionable" || !detail.MayHaveEffects || string(result) != `{"InputCompleted":false,"InputStarted":true}` {
		t.Fatalf("uncaught driver error: %s %+v %v", result, detail, err)
	}
	_, err = runScript(t.Context(), options, []string{"-"}, strings.NewReader(`try { await session.page('main').click('#target'); } catch {} throw new Error('later');`), &logs)
	if !errors.As(err, &detail) || detail.Code != "script_error" || !detail.MayHaveEffects {
		t.Fatalf("earlier mutation effects were lost: %+v %v", detail, err)
	}
}

func TestScriptMainJSONAndInputLimit(t *testing.T) {
	requireNode(t)
	var out, logs bytes.Buffer
	code := Main(context.Background(), []string{"--data-dir", t.TempDir(), "run", "-"}, strings.NewReader(`console.log('trace');return 42`), &out, &logs)
	if code != 0 || strings.TrimSpace(out.String()) != `{"ok":true,"result":42}` || strings.TrimSpace(logs.String()) != "trace" {
		t.Fatalf("stdout contract: code=%d out=%q logs=%q", code, out.String(), logs.String())
	}
	if _, err := readScript("-", strings.NewReader(strings.Repeat("x", (1<<20)+1))); err == nil {
		t.Fatal("script size limit ignored")
	}
}
