package webbrowser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReaderPoolOfficialSites(t *testing.T) {
	if os.Getenv("Q_TEST_CDP_WEB") != "1" {
		t.Skip("opt-in real network smoke")
	}
	requireBrowser(t)
	pool := NewReaderPool()
	defer func() { _ = pool.Close() }()
	r := pool.NewReader()
	defer func() { _ = r.Close() }()
	for _, url := range []string{"https://mofa.go.kr/minister/wpge/m_20035/contents.do", "https://www.molit.go.kr/USR/WPGE0201/m_36399/DTL.jsp"} {
		start := time.Now()
		page, err := r.Read(t.Context(), url)
		if err != nil || !strings.Contains(page.Text, "장관") {
			t.Fatalf("%s => title=%s final=%s chars=%d err=%v", url, page.Title, page.URL, len(page.Text), err)
		}
		t.Logf("%s => title=%s final=%s chars=%d elapsed=%s", url, page.Title, page.URL, len(page.Text), time.Since(start))
	}
}

func TestReaderPoolReusesProcessAndIsolatesCookies(t *testing.T) {
	requireBrowser(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/cookie":
			if _, err := r.Cookie("visited"); err != nil {
				http.SetCookie(w, &http.Cookie{Name: "visited", Value: "yes", Path: "/"})
				http.Redirect(w, r, "/cookie", http.StatusTemporaryRedirect)
				return
			}
			_, _ = fmt.Fprint(w, "<main>cookie redirect resolved</main>")
		case "/check":
			if _, err := r.Cookie("visited"); err == nil {
				_, _ = fmt.Fprint(w, "<main>cookie present</main>")
			} else {
				_, _ = fmt.Fprint(w, "<main>cookie absent</main>")
			}
		case "/script":
			_, _ = fmt.Fprint(w, `<script>location.href='/final'</script>`)
		case "/final":
			_, _ = fmt.Fprint(w, "<main>rendered destination</main>")
		case "/missing":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, "<main>error response is not source evidence</main>")
		}
	}))
	defer server.Close()
	pool := NewReaderPool()
	defer func() { _ = pool.Close() }()
	a, b := pool.NewReader(), pool.NewReader()
	defer func() { _ = a.Close(); _ = b.Close() }()
	firstContext, cancelFirst := context.WithCancel(t.Context())
	defer cancelFirst()
	p, err := a.Read(firstContext, server.URL+"/cookie")
	if err != nil || !strings.Contains(p.Text, "cookie redirect resolved") {
		t.Fatalf("page=%+v err=%v", p, err)
	}
	process := pool.owner.browser
	cancelFirst()
	p, err = a.Read(t.Context(), server.URL+"/check")
	if err != nil || !strings.Contains(p.Text, "cookie present") {
		t.Fatalf("session cookie lost: %+v %v", p, err)
	}
	p, err = b.Read(t.Context(), server.URL+"/check")
	if err != nil || !strings.Contains(p.Text, "cookie absent") {
		t.Fatalf("cookie isolation failed: %+v %v", p, err)
	}
	if pool.owner.browser != process || a.browser.BrowserContextID == b.browser.BrowserContextID {
		t.Fatal("process not shared or context not isolated")
	}
	p, err = a.Read(t.Context(), server.URL+"/script")
	if err != nil || !strings.HasSuffix(p.URL, "/final") || !strings.Contains(p.Text, "rendered destination") {
		t.Fatalf("script navigation failed: %+v %v", p, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	c := pool.NewReader()
	defer func() { _ = c.Close() }()
	if _, err := c.Read(t.Context(), server.URL+"/check"); err != nil {
		t.Fatal(err)
	}
	if pool.owner.browser != process {
		t.Fatal("Chrome restarted between searches")
	}
	if _, err := c.Read(t.Context(), server.URL+"/missing"); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatal("error response accepted as source evidence", err)
	}
}

func TestReaderPoolAllowsConcurrentPageReads(t *testing.T) {
	requireBrowser(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/evidence" {
			w.WriteHeader(404)
			return
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		_, _ = fmt.Fprint(w, "<main>parallel source</main>")
	}))
	defer server.Close()
	pool := NewReaderPool()
	defer func() { _ = pool.Close() }()
	errorsCh := make(chan error, 2)
	for range 2 {
		go func() {
			r := pool.NewReader()
			defer func() { _ = r.Close() }()
			_, err := r.Read(ctx, server.URL+"/evidence")
			errorsCh <- err
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("page reads serialized", ctx.Err())
		}
	}
	// Stop blocking navigation without closing twice in the deferred cleanup.
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		select {
		case err := <-errorsCh:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestReaderAdmissionHonorsLimitChangesAndCancellation(t *testing.T) {
	p := NewReaderPool()
	defer func() { _ = p.Close() }()
	if err := p.SetLimit(1); err != nil {
		t.Fatal(err)
	}
	release, err := p.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := p.SetLimit(2); err != nil {
		t.Fatal(err)
	}
	second, err := p.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetLimit(1); err != nil {
		t.Fatal(err)
	}
	second()
	blocked, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.acquire(blocked); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lower limit not applied", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.acquire(t.Context()); err == nil {
		t.Fatal("closed pool admitted a read")
	}
}

func TestSharedReaderPoolFollowsRuntimeOwnership(t *testing.T) {
	a, p := NewWithSharedReaders()
	b, q := NewWithSharedReaders()
	if p != q {
		t.Fatal("runtime pools differ")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.SetLimit(3); err != nil {
		t.Fatal("sibling runtime lost browser pool", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.SetLimit(2); err == nil {
		t.Fatal("last runtime did not close pool")
	}
}
