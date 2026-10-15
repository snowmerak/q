package websearch

import (
	"context"
	"errors"

	"github.com/snowmerak/q/webbrowser"
)

// CDPSession provides rendered reading to the sequential research workflow.
// Browser process ownership stays with the process-shared ReaderPool.
type CDPSession struct{ reader *webbrowser.Reader }

func NewCDPSession(pool *webbrowser.ReaderPool) *CDPSession {
	return &CDPSession{reader: pool.NewReader()}
}

func (s *CDPSession) Page(ctx context.Context, target string, _ bool) (Document, error) {
	p, err := s.reader.Read(ctx, target)
	return Document{PageContent: p}, err
}

func (s *CDPSession) Close() error { return s.reader.Close() }
func (s *CDPSession) Read(context.Context) (Document, error) {
	return Document{}, errors.New("web_search: sequential CDP reading requires a target URL")
}
func (s *CDPSession) Fill(context.Context, string, string) (Document, error) {
	return Document{}, errors.New("web_search: form interaction is unavailable")
}
func (s *CDPSession) Submit(context.Context, string, bool) (Document, error) {
	return Document{}, errors.New("web_search: form interaction is unavailable")
}
