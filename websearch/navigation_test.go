package websearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/webbrowser"
)

func TestRevisitChecksContentAcrossRedirects(t *testing.T) {
	body := "The lantern is blue."
	requests := 0
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/alias" {
			http.Redirect(w, req, "/article", http.StatusFound)
			return
		}
		requests++
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<main>"+body+"</main>")
	}))
	defer page.Close()
	session := NewBrowserSession()
	defer func() { _ = session.Close() }()
	r := run{session: session, hashes: map[string]string{}, pageBodies: map[string]string{}, visited: map[string]bool{}, blockedURLs: map[string]bool{}, blockedActions: map[string]bool{}, collected: map[string]bool{}, rejected: map[string]bool{}, engine: &Engine{}}
	if !r.load(t.Context(), page.URL+"/alias", false) {
		t.Fatal("first visit rejected")
	}
	digest := r.out.LastPage.ContentHash
	r.out.Evidence = []Evidence{{URL: page.URL + "/article", Text: body}}
	r.collected[passageKey(page.URL+"/article", body)] = true
	r.checkedEvidence = 1
	body = "The lantern\n  is blue."
	if r.load(t.Context(), page.URL+"/alias", false) || !r.blockedURLs[page.URL+"/alias"] || !r.blockedURLs[page.URL+"/article"] || requests != 2 || r.out.LastPage.ContentHash != digest || r.page.Text != "The lantern is blue." || len(r.out.Evidence) != 1 {
		t.Fatalf("unchanged revisit accepted: page=%+v blocked=%v requests=%d", r.page, r.blockedURLs, requests)
	}
	r.page = doc(page.URL+"/other", "Other page")
	r.page.Links = []webbrowser.Link{{URL: page.URL + "/alias", Text: "Old source"}}
	_, actions := r.actions()
	for _, action := range actions {
		if action.kind == "open" && action.value == page.URL+"/alias" {
			t.Fatal("unchanged source was offered again")
		}
	}
	body = "The lantern is green."
	if !r.load(t.Context(), page.URL+"/alias", false) || r.out.LastPage.ContentHash == digest || r.blockedURLs[page.URL+"/alias"] || r.blockedURLs[page.URL+"/article"] || requests != 3 || len(r.out.Evidence) != 0 || len(r.collected) != 0 || r.checkedEvidence != -1 {
		t.Fatalf("changed content rejected: page=%+v requests=%d", r.page, requests)
	}
}

func TestUnchangedBrowserReadsCannotRepeatWithFreshRefs(t *testing.T) {
	u := "https://example.com/"
	session := &fakeSession{pages: map[string]Document{u: doc(u, "Search the site.")}}
	session.rendered = doc(u, "Search the site.")
	session.rendered.TabID = "tab"
	session.rendered.Controls = []webbrowser.Element{{Ref: "first", Name: "Search", InputType: "search", Search: true}}
	session.submitted = session.rendered
	session.submitted.Controls = []webbrowser.Element{{Ref: "new-ref", Name: "Search", InputType: "search", Search: true}}
	base := sequence(t, "render", "read", "stop")
	choices := 0
	engine := Engine{Decision: decisionFunc(func(ctx context.Context, req systemone.Request, opts systemone.CallOptions) (*systemone.Result, error) {
		if question, ok := req.Questions["action"]; ok {
			choices++
			available := question.Criteria.(map[string]string)
			if choices == 3 && (available["read"] != "" || available["render"] != "" || available["fill_0_0"] == "") {
				t.Fatal("unchanged browser reads repeated or site search became unavailable")
			}
		}
		return base.Evaluate(ctx, req, opts)
	}), NewSession: func() Session { return session }}
	out, err := engine.Search(t.Context(), Input{Goal: "Find a missing fact", StartURL: u})
	if err != nil || out.Status != "incomplete" || out.Steps != 3 || out.Actions[0].Outcome != "unchanged" || out.Actions[1].Outcome != "unchanged" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestPageIdentityPreservesDocumentsAndDropsTracking(t *testing.T) {
	first := "https://www.google.com/search?q=lantern&sei=one&emsg=SG_REL"
	second := "https://www.google.com/search?q=lantern&sei=two&emsg=SG_REL"
	if pageURL(first) != pageURL(second) || pageURL(first) == pageURL("https://www.google.com/search?q=other") {
		t.Fatal("search tracking changed page identity or distinct queries merged")
	}
	u := "https://example.com/article?id=2&utm_source=test#heading"
	if pageURL(u) != "https://example.com/article?id=2" || strings.Contains(pageURL(u), "utm_source") {
		t.Fatal(pageURL(u))
	}
}
