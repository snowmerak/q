// Package systemoneserver routes the native System One API across configured providers.
package systemoneserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/systemoneconfig"
)

const maxRequestBytes = 65536

type Server struct {
	config  systemoneconfig.Config
	clients map[string]*systemone.Client
	auth    *systemoneconfig.Authenticator
}

func New(config systemoneconfig.Config) (*Server, error) {
	if config.ActiveGeneratedKeyCount() != 0 {
		return nil, errors.New("systemone: a master key is required for managed API keys")
	}
	return NewWithMasterKey(config, [32]byte{})
}

func NewWithMasterKey(config systemoneconfig.Config, masterKey [32]byte) (*Server, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	authenticator, err := systemoneconfig.NewAuthenticator(masterKey, config)
	if err != nil {
		return nil, err
	}
	server := &Server{config: config, clients: make(map[string]*systemone.Client, len(config.Providers)), auth: authenticator}
	for _, provider := range config.Providers {
		client, err := provider.NewClient()
		if err != nil {
			return nil, err
		}
		server.clients[provider.ID] = client
	}
	return server, nil
}

func (s *Server) ReloadAuthentication(config systemoneconfig.Config, masterKey [32]byte) error {
	return s.auth.ReloadWithMasterKey(masterKey, config)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("POST /v1/systemone", s.evaluate)
	return s.auth.OptionalHandler(mux)
}

func (s *Server) models(writer http.ResponseWriter, request *http.Request) {
	models := make([]systemone.Model, 0)
	var failures []error
	for _, provider := range s.config.Providers {
		result, err := s.clients[provider.ID].ListModels(request.Context())
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", provider.ID, err))
			continue
		}
		for _, model := range result.Models {
			model.Name = provider.ID + "/" + model.Name
			models = append(models, model)
		}
	}
	if len(failures) == len(s.config.Providers) {
		writeError(writer, http.StatusBadGateway, "model discovery failed for every provider")
		return
	}
	writeJSON(writer, http.StatusOK, systemone.ModelsResult{Models: models})
}

func (s *Server) evaluate(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxRequestBytes))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid or oversized request body")
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		writeError(writer, http.StatusBadRequest, "request must be a JSON object")
		return
	}
	var model string
	if err := json.Unmarshal(fields["model"], &model); err != nil || model == "" {
		writeError(writer, http.StatusBadRequest, "model is required")
		return
	}
	if err := systemone.ValidateIdempotencyKey(request.Header.Get("Idempotency-Key")); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	providerID, backendModel := s.routeModel(model)
	client := s.clients[providerID]
	if client == nil || backendModel == "" {
		writeError(writer, http.StatusBadRequest, "unknown System One provider or model")
		return
	}
	fields["model"], _ = json.Marshal(backendModel)
	forwardBody, err := json.Marshal(fields)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	result, err := client.EvaluateRaw(request.Context(), forwardBody, systemone.CallOptions{
		IdempotencyKey: request.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		var apiErr *systemone.APIError
		if errors.As(err, &apiErr) {
			copyHeaders(writer.Header(), apiErr.Header)
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(apiErr.StatusCode)
			_, _ = writer.Write(apiErr.Body)
			return
		}
		writeError(writer, http.StatusBadGateway, "System One provider request failed")
		return
	}
	copyHeaders(writer.Header(), result.Header)
	var response map[string]json.RawMessage
	if err := json.Unmarshal(result.Raw, &response); err != nil || response == nil {
		writeError(writer, http.StatusBadGateway, "provider returned an invalid response")
		return
	}
	response["model"], _ = json.Marshal(model)
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) routeModel(model string) (string, string) {
	providerID, backendModel, qualified := strings.Cut(model, "/")
	if qualified {
		return providerID, backendModel
	}
	provider, _, err := s.config.ResolveModel("")
	if err != nil {
		return "", ""
	}
	return provider.ID, model
}

func copyHeaders(destination, source http.Header) {
	for name, values := range source {
		canonical := http.CanonicalHeaderKey(name)
		if canonical != "X-Request-Id" && canonical != "Retry-After" &&
			!strings.HasPrefix(canonical, "X-System-One-") && !strings.HasPrefix(canonical, "X-Ratelimit-") {
			continue
		}
		for _, value := range values {
			destination.Add(canonical, value)
		}
	}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]string{"message": message}})
}
