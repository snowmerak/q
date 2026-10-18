package app

import (
	"errors"
	"net/http"
	"net/url"
)

func managedGatewayConnection(runtime providerRuntime) (string, string) {
	if source, ok := runtime.(interface{ Connection() (string, string) }); ok {
		return source.Connection()
	}
	return runtime.Endpoint(), runtime.APIKey()
}

// A session and its background embedders may outlive a Gateway generation.
// Resolve the address and matching key for every physical HTTP attempt, so a
// retry never remains pinned to a child that has already been replaced.
type managedGatewayTransport struct {
	runtime providerRuntime
}

func (transport managedGatewayTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	endpoint, key := managedGatewayConnection(transport.runtime)
	target, err := url.Parse(endpoint)
	if err != nil || target.Host == "" || key == "" {
		return nil, errors.New("internal LLM Gateway connection is unavailable")
	}
	attempt := request.Clone(request.Context())
	attempt.URL.Scheme = target.Scheme
	attempt.URL.Host = target.Host
	attempt.Host = target.Host
	attempt.Header.Set("Authorization", "Bearer "+key)
	return http.DefaultTransport.RoundTrip(attempt)
}
