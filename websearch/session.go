package websearch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/snowmerak/q/webbrowser"
	"golang.org/x/net/html"
)

// BrowserSession first reads static documents with Go's HTTP client. Rendering
// and search form interactions lazily start one isolated browser tab.
type BrowserSession struct {
	HTTP    *http.Client
	browser *webbrowser.Runtime
	tabID   string
}

func NewBrowserSession() *BrowserSession {
	return &BrowserSession{HTTP: &http.Client{Timeout: 25 * time.Second}, browser: webbrowser.NewWithIdleTimeout(searchTimeout + time.Minute)}
}

func (s *BrowserSession) Close() error { return s.browser.Close() }

func (s *BrowserSession) Page(ctx context.Context, target string, render bool) (Document, error) {
	if !validURL(target) {
		return Document{}, errors.New("web_search: invalid HTTP(S) URL")
	}
	if !render {
		p, err := fetch(ctx, s.HTTP, target)
		if err == nil && strings.TrimSpace(p.Text) != "" {
			return Document{PageContent: p}, nil
		}
	}
	var p webbrowser.PageContent
	var err error
	if s.tabID == "" {
		p, err = s.browser.Open(ctx, webbrowser.OpenInput{URL: target})
		if err == nil {
			s.tabID = p.TabID
		}
	} else {
		p, err = s.browser.Navigate(ctx, s.tabID, target)
	}
	if err != nil {
		return Document{}, err
	}
	return s.observe(ctx)
}

func (s *BrowserSession) observe(ctx context.Context) (Document, error) {
	var p webbrowser.PageContent
	var snap webbrowser.Snapshot
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		p, err = s.browser.Read(ctx, webbrowser.TabInput{TabID: s.tabID})
		if err == nil {
			snap, err = s.browser.Snapshot(ctx, webbrowser.TabInput{TabID: s.tabID})
		}
		if err == nil && p.URL == snap.URL {
			return Document{PageContent: p, Controls: snap.Elements}, nil
		}
		if err == nil {
			err = errors.New("web_search: page navigated during observation")
		}
		if ctx.Err() != nil {
			return Document{}, ctx.Err()
		}
		t := time.NewTimer(150 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return Document{}, ctx.Err()
		case <-t.C:
		}
	}
	return Document{}, err
}

func (s *BrowserSession) Read(ctx context.Context) (Document, error) { return s.observe(ctx) }

func (s *BrowserSession) Fill(ctx context.Context, ref, text string) (Document, error) {
	if _, err := s.browser.FillSearch(ctx, webbrowser.FillInput{TabID: s.tabID, Ref: ref, Text: text}); err != nil {
		return Document{}, err
	}
	return s.observe(ctx)
}

func (s *BrowserSession) Submit(ctx context.Context, ref string, button bool) (Document, error) {
	if _, err := s.browser.SubmitSearch(ctx, webbrowser.ClickInput{TabID: s.tabID, Ref: ref}, button); err != nil {
		return Document{}, err
	}
	return s.observe(ctx)
}

func fetch(ctx context.Context, h *http.Client, target string) (webbrowser.PageContent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return webbrowser.PageContent{}, err
	}
	req.Header.Set("User-Agent", "Q-WebSearch/0.1")
	res, err := h.Do(req)
	if err != nil {
		return webbrowser.PageContent{}, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return webbrowser.PageContent{}, fmt.Errorf("web_search: HTTP %d for %s", res.StatusCode, target)
	}
	if !validURL(res.Request.URL.String()) {
		return webbrowser.PageContent{}, errors.New("web_search: invalid redirect URL")
	}
	contentType := res.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(contentType, "text/html") && !strings.Contains(contentType, "text/plain") {
		return webbrowser.PageContent{}, fmt.Errorf("web_search: unsupported content type %s", clip(contentType, 100))
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 2<<20+1))
	if err != nil {
		return webbrowser.PageContent{}, err
	}
	if len(data) > 2<<20 {
		return webbrowser.PageContent{}, errors.New("web_search: page exceeds 2 MiB")
	}
	p := webbrowser.PageContent{URL: res.Request.URL.String(), Links: []webbrowser.Link{}}
	if strings.Contains(contentType, "text/plain") {
		p.Text = clip(string(data), 250000)
		p.Truncated = len(data) > len(p.Text)
		return p, nil
	}
	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return p, err
	}
	return extractDocument(doc, res.Request.URL), nil
}
