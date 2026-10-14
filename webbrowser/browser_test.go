package webbrowser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/launcher"
)

func TestInvalidRequestsDoNotStartBrowser(t *testing.T) {
	r := New()
	t.Cleanup(func() { _ = r.Close() })
	for _, value := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "https:///missing-host", "https://user:pass@example.com"} {
		if _, err := r.Open(t.Context(), OpenInput{URL: value}); err == nil {
			t.Fatalf("accepted URL %q", value)
		}
	}
	if _, err := r.Read(t.Context(), TabInput{TabID: "missing"}); err == nil {
		t.Fatal("unknown tab accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Open(ctx, OpenInput{URL: "https://example.com"}); err == nil {
		t.Fatal("cancelled call accepted")
	}
	if r.browser != nil || r.launcher != nil || r.idle != nil {
		t.Fatal("invalid calls launched a browser or armed an idle timer")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Open(t.Context(), OpenInput{URL: "https://example.com"}); err == nil {
		t.Fatal("closed runtime restarted")
	}
}

func TestInvalidBrowserPath(t *testing.T) {
	t.Setenv("Q_BROWSER_PATH", t.TempDir()+"/missing-browser")
	r := New()
	t.Cleanup(func() { _ = r.Close() })
	if _, err := r.Open(t.Context(), OpenInput{URL: "https://example.com"}); err == nil || !strings.Contains(err.Error(), "Q_BROWSER_PATH") {
		t.Fatalf("error = %v", err)
	}
	if r.browser != nil {
		t.Fatal("invalid path started a browser")
	}
}

func requireBrowser(t *testing.T) {
	t.Helper()
	if os.Getenv("Q_BROWSER_PATH") == "" {
		if _, found := launcher.LookPath(); !found {
			t.Skip("install Chrome/Chromium or set Q_BROWSER_PATH to run real browser tests")
		}
	}
}

func TestMalformedLinkDoesNotDiscardPageContent(t *testing.T) {
	requireBrowser(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<title>Documentation</title><main>Valid cancellation evidence.<img alt="Organization chart: Example Ministry."></main><a href="http://[">Malformed link</a><a href="/valid">Valid link</a><a href="javascript:void(0)">Script link</a>`)
	}))
	defer server.Close()
	runtime := New()
	t.Cleanup(func() { _ = runtime.Close() })
	page, err := runtime.Open(t.Context(), OpenInput{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "Documentation" || !strings.Contains(page.Text, "Valid cancellation evidence.") || !strings.Contains(page.Text, "Organization chart: Example Ministry.") || len(page.Links) != 1 || page.Links[0].URL != server.URL+"/valid" {
		t.Fatalf("malformed link discarded valid content: %+v", page)
	}
	if page, err := runtime.Read(t.Context(), TabInput{TabID: page.TabID}); err != nil || len(page.Links) != 1 || !strings.Contains(page.Text, "Organization chart: Example Ministry.") {
		t.Fatalf("read = %+v, %v", page, err)
	}
}

func TestResearchSearchControlsAndLiveFormValidation(t *testing.T) {
	requireBrowser(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if req.Method != "GET" {
			t.Errorf("unexpected method %s", req.Method)
		}
		if req.URL.Query().Get("q") != "" {
			_, _ = fmt.Fprint(w, "<main>Found deadline evidence</main>")
			return
		}
		_, _ = fmt.Fprint(w, `<form id="search" action="/results"><input type="search" name="q" aria-label="Search"><button>Search</button></form><form method="post"><input name="q" aria-label="Post search"></form><input type="password" aria-label="Password">`)
	}))
	defer server.Close()
	r := New()
	defer func() { _ = r.Close() }()
	p, err := r.Open(t.Context(), OpenInput{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := r.Snapshot(t.Context(), TabInput{TabID: p.TabID})
	if err != nil {
		t.Fatal(err)
	}
	var ref string
	for _, el := range snap.Elements {
		if el.Name == "Search" && el.InputType == "search" {
			if !el.Search {
				t.Fatal("search input unrecognized")
			}
			ref = el.Ref
		}
		if el.Name == "Password" || el.Name == "Post search" {
			if el.Search {
				t.Fatalf("unsafe control eligible: %+v", el)
			}
		}
	}
	if ref == "" {
		t.Fatal("missing search input")
	}
	if _, err := r.FillSearch(t.Context(), FillInput{TabID: p.TabID, Ref: ref, Text: "deadline"}); err != nil {
		t.Fatal(err)
	}
	snap, err = r.Snapshot(t.Context(), TabInput{TabID: p.TabID})
	if err != nil {
		t.Fatal(err)
	}
	for _, el := range snap.Elements {
		if el.InputType == "search" {
			ref = el.Ref
			if el.Value != "deadline" {
				t.Fatalf("value=%q", el.Value)
			}
		}
	}
	// The DOM can change after a snapshot. Revalidate the current form rather
	// than trusting the cached Search flag supplied to System One.
	tab, _ := r.getTab(p.TabID)
	if _, err := tab.page.Eval(`() => document.getElementById('search').method='post'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SubmitSearch(t.Context(), ClickInput{TabID: p.TabID, Ref: ref}, false); err == nil {
		t.Fatal("POST form accepted after DOM change")
	}
	if _, err := tab.page.Eval(`() => document.getElementById('search').method='get'`); err != nil {
		t.Fatal(err)
	}
	snap, err = r.Snapshot(t.Context(), TabInput{TabID: p.TabID})
	if err != nil {
		t.Fatal(err)
	}
	for _, el := range snap.Elements {
		if el.InputType == "search" {
			ref = el.Ref
		}
	}
	if _, err := r.SubmitSearch(t.Context(), ClickInput{TabID: p.TabID, Ref: ref}, false); err != nil {
		t.Fatal(err)
	}
	p, err = r.Read(t.Context(), TabInput{TabID: p.TabID})
	if err != nil || !strings.Contains(p.Text, "Found deadline evidence") {
		t.Fatalf("page=%+v err=%v", p, err)
	}
}

func TestBrowserRenderedContentInteractionsAndCleanup(t *testing.T) {
	requireBrowser(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if req.URL.Path == "/large" {
			_, _ = fmt.Fprintf(w, "<main>%s</main>", strings.Repeat("가", 25000))
			for index := range 110 {
				_, _ = fmt.Fprintf(w, `<a href="/target/%d">Link %d</a>`, index, index)
			}
			return
		}
		_, _ = fmt.Fprint(w, `<title>Browser fixture</title><nav>Navigation noise</nav><main>
<p id="result">Initial</p><label for="query">Search phrase</label><input id="query" value="old">
<button onclick="document.getElementById('result').innerText = document.getElementById('query').value">Apply</button>
<button style="display:none">Hidden</button><a href="/next">Next page</a></main>
<script>document.getElementById('result').innerText = 'Rendered by JavaScript';</script>`)
	}))
	defer server.Close()
	r := New()
	t.Cleanup(func() { _ = r.Close() })
	if r.browser != nil {
		t.Fatal("New launched Chrome")
	}
	// Cancelling the first call after it completes must not kill CDP or the tab.
	ctx, cancel := context.WithCancel(t.Context())
	opened, err := r.Open(ctx, OpenInput{URL: server.URL})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if opened.Title != "Browser fixture" || !strings.Contains(opened.Text, "Rendered by JavaScript") || strings.Contains(opened.Text, "Navigation noise") || len(opened.Links) != 1 {
		t.Fatalf("opened = %#v", opened)
	}
	pid, profile := r.launcher.PID(), r.launcher.Get("user-data-dir")
	read, err := r.Read(t.Context(), TabInput{TabID: opened.TabID})
	if err != nil || read.Title != opened.Title || r.launcher.PID() != pid {
		t.Fatalf("browser was not reused: read=%#v err=%v", read, err)
	}
	snapshot, err := r.Snapshot(t.Context(), TabInput{TabID: opened.TabID})
	if err != nil {
		t.Fatal(err)
	}
	for _, el := range snapshot.Elements {
		if el.Name == "Hidden" {
			t.Fatal("snapshot exposed hidden element")
		}
	}
	field := findRef(t, snapshot, "Search phrase")
	button := findRef(t, snapshot, "Apply")
	if _, err := r.Fill(t.Context(), FillInput{TabID: opened.TabID, Ref: field, Text: "replacement"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Click(t.Context(), ClickInput{TabID: opened.TabID, Ref: button}); err == nil {
		t.Fatal("interaction reused old snapshot ref")
	}
	snapshot, err = r.Snapshot(t.Context(), TabInput{TabID: opened.TabID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Click(t.Context(), ClickInput{TabID: opened.TabID, Ref: findRef(t, snapshot, "Apply")}); err != nil {
		t.Fatal(err)
	}
	read, err = r.Read(t.Context(), TabInput{TabID: opened.TabID})
	if err != nil || !strings.Contains(read.Text, "replacement") || strings.Contains(read.Text, "oldreplacement") {
		t.Fatalf("form result = %#v, err = %v", read, err)
	}
	snapshot, err = r.Snapshot(t.Context(), TabInput{TabID: opened.TabID})
	if err != nil {
		t.Fatal(err)
	}
	button = findRef(t, snapshot, "Apply")
	// A document replacement at the same URL must invalidate references too.
	if err := r.tabs[opened.TabID].page.Context(t.Context()).Navigate(server.URL); err != nil {
		t.Fatal(err)
	}
	if err := r.tabs[opened.TabID].page.Context(t.Context()).WaitLoad(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Click(t.Context(), ClickInput{TabID: opened.TabID, Ref: button}); err == nil {
		t.Fatal("reference from replaced document accepted")
	}
	large, err := r.Open(t.Context(), OpenInput{URL: server.URL + "/large"})
	if err != nil || !large.Truncated || len([]rune(large.Text)) != 24000 || len(large.Links) != 100 {
		t.Fatalf("large read: text=%d links=%d truncated=%v err=%v", len([]rune(large.Text)), len(large.Links), large.Truncated, err)
	}
	largeSnapshot, err := r.Snapshot(t.Context(), TabInput{TabID: large.TabID})
	if err != nil || !largeSnapshot.Truncated || len(largeSnapshot.Elements) != 100 {
		t.Fatalf("large snapshot: elements=%d truncated=%v err=%v", len(largeSnapshot.Elements), largeSnapshot.Truncated, err)
	}
	if _, err := r.CloseTab(t.Context(), TabInput{TabID: large.TabID}); err != nil {
		t.Fatal(err)
	}
	if r.browser == nil {
		t.Fatal("closing one tab stopped the other")
	}
	if _, err := r.CloseTab(t.Context(), TabInput{TabID: opened.TabID}); err != nil {
		t.Fatal(err)
	}
	if r.browser != nil || r.launcher != nil || r.idle != nil {
		t.Fatal("last tab did not stop the browser")
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("temporary profile was not removed: %v", err)
	}
	if _, err := r.Read(t.Context(), TabInput{TabID: opened.TabID}); err == nil {
		t.Fatal("closed tab remained accessible")
	}
}

func findRef(t *testing.T, snapshot Snapshot, name string) string {
	t.Helper()
	for _, el := range snapshot.Elements {
		if el.Name == name {
			return el.Ref
		}
	}
	t.Fatalf("element %q missing from %#v", name, snapshot)
	return ""
}

func TestBrowserIdleExpiryAndRestart(t *testing.T) {
	requireBrowser(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "<main>Idle fixture</main>")
	}))
	defer server.Close()
	r := New()
	r.idleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { _ = r.Close() })
	opened, err := r.Open(t.Context(), OpenInput{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.mu.Lock()
		inactive := r.browser == nil
		r.mu.Unlock()
		if inactive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle browser was not stopped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := r.Read(t.Context(), TabInput{TabID: opened.TabID}); err == nil {
		t.Fatal("idle tab did not expire")
	}
	// Keep the restarted browser alive until Close tests runtime disposal.
	r.mu.Lock()
	r.idleTimeout = time.Minute
	r.mu.Unlock()
	reopened, err := r.Open(t.Context(), OpenInput{URL: server.URL})
	if err != nil || reopened.TabID == opened.TabID {
		t.Fatalf("reopen = %#v, err = %v", reopened, err)
	}
	profile := r.launcher.Get("user-data-dir")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("runtime close left its profile: %v", err)
	}
}
