package tools

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/systemoneconfig"
	"github.com/snowmerak/q/tools/builtin"
	"github.com/snowmerak/q/webbrowser"
	"github.com/snowmerak/q/websearch"
)

// WithWebSearch substitutes a search service, including in linked checkouts.
// The service owns per-call resources and must support concurrent searches.
func WithWebSearch(service builtin.WebSearcher) RuntimeOption {
	return func(options *runtimeOptions) { options.webSearch = service }
}

type configuredWebSearch struct {
	dir     string
	readers *webbrowser.ReaderPool
}

func (s *configuredWebSearch) settings() (string, systemoneconfig.Config, error) {
	dir := s.dir
	if dir == "" {
		store, err := config.DefaultStore()
		if err != nil {
			return "", systemoneconfig.Config{}, err
		}
		dir = store.Dir
	}
	value, err := (systemoneconfig.Store{Dir: dir}).LoadOrDefault()
	if err != nil {
		return dir, value, err
	}
	if !value.WebSearchConfigured() {
		return dir, value, errors.New("web_search: configure both navigation and completion System One models")
	}
	return dir, value, nil
}

// Available checks configuration locally without contacting model providers.
func (s *configuredWebSearch) Available() bool {
	_, _, err := s.settings()
	return err == nil
}

func webSearchAvailable(service builtin.WebSearcher) bool {
	if service == nil {
		return false
	}
	if availability, ok := service.(interface{ Available() bool }); ok {
		return availability.Available()
	}
	return true
}

func webSearchTool(name string) bool {
	return name == "browser_search" || name == "web_search" || name == "web_search_batch"
}

func (s *configuredWebSearch) Search(ctx context.Context, in websearch.Input) (websearch.Output, error) {
	dir, soConfig, err := s.settings()
	if err != nil {
		return websearch.Output{}, err
	}
	provider, model, err := soConfig.ResolveModel(systemoneconfig.RoleWebSearch)
	if err != nil {
		return websearch.Output{}, err
	}
	// Decisions inherit the search/caller context, including model-server queueing.
	decision, err := systemone.New(systemone.Config{BaseURL: provider.BaseURL(), APIKey: provider.ResolveAPIKey(), HTTPClient: &http.Client{}})
	if err != nil {
		return websearch.Output{}, err
	}
	engine := websearch.Engine{Decision: decision, DecisionModel: model, Discovery: websearch.ScourSearcher{}}
	main, err := (config.Store{Dir: dir}).Load()
	if errors.Is(err, config.ErrNotFound) {
		main, err = config.Default(), nil
	}
	if err != nil {
		return websearch.Output{}, err
	}
	pool := s.readers
	if pool == nil {
		pool = webbrowser.NewReaderPool()
		defer func() { _ = pool.Close() }()
	}
	if err := pool.SetLimit(main.WebSearch.Effective().MaxConcurrentReads); err != nil {
		return websearch.Output{}, err
	}
	engine.NewSession = func() websearch.Session { return websearch.NewCDPSession(pool) }
	if soConfig.RoleModels[systemoneconfig.RoleWebSearchFinish] != "" {
		finishProvider, finishModel, err := soConfig.ResolveModel(systemoneconfig.RoleWebSearchFinish)
		if err != nil {
			return websearch.Output{}, err
		}
		finishDecision, err := systemone.New(systemone.Config{BaseURL: finishProvider.BaseURL(), APIKey: finishProvider.ResolveAPIKey(), HTTPClient: &http.Client{Timeout: 30*time.Minute + 5*time.Second}})
		if err != nil {
			return websearch.Output{}, err
		}
		engine.CoverageDecision, engine.CoverageModel = finishDecision, finishModel
	}
	return engine.Search(ctx, in)
}
