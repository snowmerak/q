package websearch

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
