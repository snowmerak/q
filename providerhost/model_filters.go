package providerhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
)

// ProviderModelFilter limits catalog discovery, without blocking requests to
// models already selected in existing sessions or assignments.
type ProviderModelFilter map[string]*regexp.Regexp

// NewProviderModelFilter maps saved provider IDs to their Gateway prefixes.
// Patterns match the upstream ID, including any upstream vendor prefix.
func NewProviderModelFilter(routes gateway.Config, patterns map[string]string) (ProviderModelFilter, error) {
	filter := make(ProviderModelFilter)
	for _, provider := range routes.Providers {
		pattern := patterns[provider.ID]
		if pattern == "" {
			continue
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("provider %q model filter: %w", provider.ID, err)
		}
		prefix := provider.Prefix
		if prefix == "" {
			prefix = provider.ID
		}
		filter[prefix] = compiled
	}
	return filter, nil
}

// Allows reports whether a Gateway model ID is visible in the catalog.
func (filter ProviderModelFilter) Allows(id string) bool {
	prefix, upstream, found := strings.Cut(id, "/")
	pattern := filter[prefix]
	return !found || pattern == nil || pattern.MatchString(upstream)
}

// ProviderModelFilterHandler reloads filters on catalog reads. It wraps the raw
// Gateway before model groups so group availability uses the filtered catalog.
func ProviderModelFilterHandler(inner http.Handler, routes gateway.Config, store config.Store) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/models" && !strings.HasPrefix(request.URL.Path, "/v1/models/") {
			inner.ServeHTTP(writer, request)
			return
		}
		value, err := store.Load()
		if errors.Is(err, config.ErrNotFound) {
			inner.ServeHTTP(writer, request)
			return
		}
		if err != nil {
			writeGroupError(writer, http.StatusInternalServerError, err)
			return
		}
		filter, err := NewProviderModelFilter(routes, value.ProviderModelFilters)
		if err != nil {
			writeGroupError(writer, http.StatusInternalServerError, err)
			return
		}
		if len(filter) == 0 {
			inner.ServeHTTP(writer, request)
			return
		}
		if request.URL.Path != "/v1/models" {
			if !filter.Allows(strings.TrimPrefix(request.URL.Path, "/v1/models/")) {
				writeGroupError(writer, http.StatusNotFound, errors.New("model is excluded by the provider model filter"))
				return
			}
			inner.ServeHTTP(writer, request)
			return
		}
		recorded := httptest.NewRecorder()
		inner.ServeHTTP(recorded, request)
		if recorded.Code != http.StatusOK {
			copyRecordedResponse(writer, recorded)
			return
		}
		// Preserve metadata and any upstream extension fields verbatim.
		var envelope map[string]json.RawMessage
		var models []json.RawMessage
		err = json.Unmarshal(recorded.Body.Bytes(), &envelope)
		if err == nil {
			err = json.Unmarshal(envelope["data"], &models)
		}
		if err != nil {
			writeGroupError(writer, http.StatusBadGateway, fmt.Errorf("decode Gateway models for filtering: %w", err))
			return
		}
		visible := make([]json.RawMessage, 0, len(models))
		for _, raw := range models {
			var model struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &model); err != nil {
				writeGroupError(writer, http.StatusBadGateway, fmt.Errorf("decode Gateway model for filtering: %w", err))
				return
			}
			if filter.Allows(model.ID) {
				visible = append(visible, raw)
			}
		}
		envelope["data"], _ = json.Marshal(visible)
		copyHeader(writer.Header(), recorded.Header())
		writer.Header().Del("Content-Length")
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(envelope)
	})
}
