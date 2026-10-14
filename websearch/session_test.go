package websearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/webbrowser"
)

func TestStaticFetchRemovesNoiseAndResolvesLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<title>Fixture</title><nav>Navigation junk</nav><script>Dangerous instructions</script><main><p>Read-only needs sidecars AND writable directory.</p><a href="/detail#topic">Detail</a><a href="javascript:evil()">Unsafe</a></main>`)
	}))
	defer server.Close()
	s := NewBrowserSession()
	defer func() { _ = s.Close() }()
	p, err := s.Page(t.Context(), server.URL, false)
	if err != nil || !strings.Contains(p.Text, "sidecars AND writable directory") || strings.Contains(p.Text, "Dangerous") || strings.Contains(p.Text, "Navigation junk") || p.Title != "Fixture" || len(p.Links) != 1 || p.Links[0].URL != server.URL+"/detail" {
		t.Fatalf("page=%+v err=%v", p, err)
	}
	if s.tabID != "" {
		t.Fatal("static fetch started browser")
	}
}

func TestMainContentRetainsNavigationDestinations(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<title>Department</title><header>Global header</header><nav>Navigation junk<a href="/minister/greeting">Minister greeting</a><a href="/minister/profile">Biography</a></nav><main><h1>Minister activity</h1><p>On 14 October, Minister Example attended the meeting.</p><a href="/release">Press release</a></main><aside>Unrelated site directory</aside><footer>Global footer</footer>`)
	}))
	defer page.Close()
	p, err := fetch(t.Context(), page.Client(), page.URL)
	if err != nil || !strings.Contains(p.Text, "Minister Example") || strings.Contains(p.Text, "Navigation junk") || strings.Contains(p.Text, "Global header") || strings.Contains(p.Text, "Unrelated site directory") || len(p.Links) != 3 {
		t.Fatalf("page=%+v err=%v", p, err)
	}
	if p.Links[0].URL != page.URL+"/release" || p.Links[1].URL != page.URL+"/minister/greeting" || p.Links[2].URL != page.URL+"/minister/profile" {
		t.Fatal(p.Links)
	}
}

func TestOrganizationImageAlternativeTextIsSourceText(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<main><h1>Organization chart</h1><p>Updated 2 October.</p><img src="/chart.png" alt="Ministry of Education; Ministry of Science and ICT."></main>`)
	}))
	defer page.Close()
	p, err := fetch(t.Context(), page.Client(), page.URL)
	if err != nil || !strings.Contains(p.Text, "Updated 2 October") || !strings.Contains(p.Text, "Ministry of Education; Ministry of Science and ICT.") {
		t.Fatalf("page=%+v err=%v", p, err)
	}
}

func TestLiveOrganizationAlternativeText(t *testing.T) {
	if os.Getenv("Q_TEST_WEB_SOURCE") != "1" {
		t.Skip("set Q_TEST_WEB_SOURCE=1 to fetch the public organization chart")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	p, err := fetch(ctx, &http.Client{Timeout: 20 * time.Second}, "https://org.go.kr/orgnzt/chart/viewEng.do")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Text, "The President") || !strings.Contains(p.Text, "Ministry of Education") {
		t.Fatal("organization chart alternative text missing")
	}
	t.Logf("source_bytes=%d source_links=%d organization_alt_text=true", len(p.Text), len(p.Links))
}

func TestLiveEducationPageExtraction(t *testing.T) {
	if os.Getenv("Q_TEST_WEB_SOURCE") != "1" {
		t.Skip("set Q_TEST_WEB_SOURCE=1 to fetch the public education page")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	p, err := fetch(ctx, &http.Client{Timeout: 20 * time.Second}, "https://moe.go.kr/mnstrInfo.do?m=01&s=mnstr")
	if err != nil {
		t.Fatal(err)
	}
	profile, greeting := false, false
	for _, link := range p.Links {
		profile = profile || strings.Contains(link.URL, "/mnstrHist.do")
		greeting = greeting || strings.Contains(link.URL, "/mnstrInsa.do")
	}
	t.Logf("source_bytes=%d source_links=%d profile=%t greeting=%t", len(p.Text), len(p.Links), profile, greeting)
	if !profile || !greeting || p.Text == "" {
		t.Fatal("public minister navigation or body missing")
	}
}

func TestHTTPStatusAndSizeLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/challenge":
			w.WriteHeader(http.StatusAccepted)
		case "/large":
			_, _ = fmt.Fprint(w, strings.Repeat("x", (2<<20)+1))
		case "/pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = fmt.Fprint(w, "pdf")
		}
	}))
	defer server.Close()
	for _, path := range []string{"/challenge", "/large", "/pdf"} {
		if _, err := fetch(t.Context(), server.Client(), server.URL+path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}

func TestChunksRemainAvailableAndKeepUnicodeValid(t *testing.T) {
	text := strings.Repeat("중요한 조건과 예외를 보존해야 합니다. ", 1000) + " Late qualification: readonly needs writable directory."
	c := chunks(text, "writable directory")
	if len(c) < 7 || !strings.Contains(c[0], "writable directory") {
		t.Fatal("late qualifier lost or chunks discarded")
	}
}

func TestRenderedSearchRedirectLinksBecomePublisherDestinations(t *testing.T) {
	links := rankedLinks([]webbrowser.Link{{Text: "Official docs", URL: "https://duckduckgo.com/l/?uddg=https%3A%2F%2Fpkg.go.dev%2Fcontext"}}, "context")
	if len(links) != 1 || links[0].URL != "https://pkg.go.dev/context" || !publisherAllowed([]string{"go.dev"}, links[0].URL) {
		t.Fatalf("links=%+v", links)
	}
}
