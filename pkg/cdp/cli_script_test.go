package qdriver_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func scriptFile(t *testing.T, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflow.js")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLIJavaScriptBrowserWorkflow(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is needed for script runner tests")
	}
	chrome := startChrome(t)
	base := buildCLI(t)
	f := namedCLI(t, base.binary, t.TempDir(), "javascript")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/popup" {
			_, _ = fmt.Fprint(w, "<!doctype html><title>Script popup</title>")
			return
		}
		_, _ = fmt.Fprint(w, `<!doctype html><title>Script workflow</title><label for="name">Name</label><input id="name"><button id="load" onclick="clicks++;setTimeout(()=>{document.querySelector('#list').innerHTML=Array.from({length:5},(_,i)=>'<a href=/broadcast/'+i+'>Broadcast '+i+'</a>').join('')},100)">Load broadcasts</button><button id="popup" onclick="window.open('/popup')">Open popup</button><section id="list"></section><script>window.clicks=0</script>`)
	}))
	defer server.Close()
	path := scriptFile(t, `
const page = await session.newPage('main', session.args[0]);
await page.fill({role:'textbox', name:'Name'}, '안녕하세요 QWERTY!');
const observed = await page.snapshot({scope:'full_page'});
const target = observed.nodes.find(n => n.role === 'button' && n.name === 'Load broadcasts');
await page.evaluate(() => document.title);
let stale;
try { await page.click({ref:target.ref}); } catch(e) { stale = e.code; }
await page.click({role:'button',name:'Load broadcasts'});
await page.waitForSelector('#list a:first-child');
await page.waitForFunction(count => document.querySelectorAll('#list a').length === count, 5);
const values = await page.evaluate(() => ({text:document.querySelector('input').value,clicks,links:[...document.querySelectorAll('#list a')].map(a=>({title:a.textContent,url:a.href}))}));
return {session:session.name,stale,...values};`)
	result := f.ok(t, "--endpoint", chrome.endpoint, "run", path, server.URL)
	var got struct {
		Session, Stale, Text string
		Clicks               int
		Links                []struct{ Title, URL string }
	}
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Session != "javascript" || got.Stale != "stale_ref" || got.Text != "안녕하세요 QWERTY!" || got.Clicks != 1 || len(got.Links) != 5 || got.Links[4].URL != server.URL+"/broadcast/4" {
		t.Fatalf("JavaScript browser workflow failed: %s", result)
	}
	if got := string(f.ok(t, "page", "eval", "main", "clicks")); got != "1" {
		t.Fatalf("script completion lost session or replayed input: %s", got)
	}
	popup := scriptFile(t, `
const page = session.page('main');
const {subscription} = await session.command('events','arm','--kind','popup',page.name);
try {
  await page.click('#popup');
  const event = await session.command('events','next',subscription);
  await session.command('popup','adopt','--name','details',event.handle);
  await session.page('details').waitForFunction(() => document.title === 'Script popup');
  const title = await session.page('details').title();
  await session.page('details').close();
  return title;
} finally { await session.command('events','close',subscription); }`)
	if got := string(f.ok(t, "run", popup)); got != `"Script popup"` {
		t.Fatalf("script event/handle workflow failed: %s", got)
	}
	// A thrown script error must preserve earlier mutations and leave the
	// session usable. No rollback/replay is implied by a failed workflow.
	failure := f.failed(t, "script_error", "run", scriptFile(t, `await session.page('main').fill('#name','한글'); throw new Error('after edit');`))
	if !failure.Error.MayHaveEffects {
		t.Fatal("script failure erased earlier mutation effects")
	}
	var retained string
	if data := f.ok(t, "page", "eval", "main", "document.querySelector('input').value"); json.Unmarshal(data, &retained) != nil || retained != "한글" {
		t.Fatalf("failed script edit was lost: %s", data)
	}
	// Use the CLI's own deadline so the structured error and Node termination
	// are observed instead of only killing the parent from a test.
	command := exec.CommandContext(t.Context(), f.binary, "--data-dir", f.dataDir, "--session", f.session, "--timeout", "400ms", "run", scriptFile(t, `await session.page('main').waitForSelector('#never-exists')`))
	hideWindow(command)
	output, runErr := command.CombinedOutput()
	var reply cliReply
	if err := json.Unmarshal(output, &reply); err != nil || runErr == nil || reply.Error == nil || reply.Error.Code != "deadline" {
		t.Fatalf("script deadline: %s %v %v", output, runErr, err)
	}
	if got := string(f.ok(t, "page", "title", "main")); !strings.Contains(got, "Script workflow") {
		t.Fatalf("canceled runner broke the session: %s", got)
	}
}
