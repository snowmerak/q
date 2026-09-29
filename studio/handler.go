package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
)

const apiVersion = 1

type statusResponse struct {
	Version int    `json:"version"`
	Service string `json:"service"`
	Ready   bool   `json:"ready"`
}

// Server is the user-level Studio HTTP surface and its shared agent runtime.
// Individual sessions resolve and own their workspace roots.
type Server struct {
	http.Handler
	host     *app.SessionHost
	commits  *commitService
	sessions *sessionsService
}

// NewServer returns a Studio server whose runtime follows parent cancellation.
func NewServer(parent context.Context) (*Server, error) {
	store, err := config.DefaultStore()
	if err != nil {
		return nil, fmt.Errorf("locate Studio settings: %w", err)
	}
	host, err := app.NewSessionHost(parent, store)
	if err != nil {
		return nil, fmt.Errorf("open Studio session runtime: %w", err)
	}
	handler, commits, sessions, err := newHandlerRuntime(parent, store, host)
	if err != nil {
		_ = host.Close()
		return nil, err
	}
	return &Server{Handler: handler, host: host, commits: commits, sessions: sessions}, nil
}

// NewHandler is retained for embedders that only need an http.Handler.
// Command owners should prefer NewServer so they can close its runtime.
func NewHandler() (http.Handler, error) {
	return NewServer(context.Background())
}

func (server *Server) Close() error {
	if server == nil || server.host == nil {
		return nil
	}
	return errors.Join(server.sessions.Close(), server.commits.Close(), server.host.Close())
}

func newHandler(store config.Store) (http.Handler, error) {
	return newHandlerWithRunner(store, nil)
}

func newHandlerWithRunner(store config.Store, runner sessionRunner) (http.Handler, error) {
	handler, _, _, err := newHandlerRuntime(context.Background(), store, runner)
	return handler, err
}

func newHandlerRuntime(parent context.Context, store config.Store, runner sessionRunner) (http.Handler, *commitService, *sessionsService, error) {
	assets, err := frontend()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open embedded Studio frontend: %w", err)
	}
	spa, err := newSPAHandler(assets)
	if err != nil {
		return nil, nil, nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", serveStatus)
	var settingsRuntimes []settingsRuntime
	if runtime, ok := runner.(settingsRuntime); ok {
		settingsRuntimes = append(settingsRuntimes, runtime)
	}
	settings := newSettingsService(store, settingsRuntimes...)
	mux.HandleFunc("GET /api/v1/settings", settings.serveSnapshot)
	mux.HandleFunc("GET /api/v1/settings/models", settings.serveModelCatalog)
	mux.HandleFunc("PUT /api/v1/settings/models/{target}", settings.serveModelAssignmentUpdate)
	mux.HandleFunc("GET /api/v1/workspaces/models", settings.serveWorkspaceModels)
	mux.HandleFunc("PUT /api/v1/workspaces/models/{target}", settings.serveWorkspaceModelUpdate)
	mux.HandleFunc("DELETE /api/v1/workspaces/models/{target}", settings.serveWorkspaceModelUpdate)
	mux.HandleFunc("PUT /api/v1/settings/model-groups", settings.serveModelGroupUpsert)
	mux.HandleFunc("DELETE /api/v1/settings/model-groups/{group}", settings.serveModelGroupDelete)
	mux.HandleFunc("POST /api/v1/settings/roles", settings.serveCustomRoleCreate)
	mux.HandleFunc("DELETE /api/v1/settings/roles/{role}", settings.serveCustomRoleDelete)
	mux.HandleFunc("PUT /api/v1/settings/model-api-mode", settings.serveModelAPIModeUpdate)
	mux.HandleFunc("PUT /api/v1/settings/gateway/model-metadata", settings.serveModelMetadataUpdate)
	mux.HandleFunc("POST /api/v1/settings/gateway/providers", settings.serveProviderCreate)
	mux.HandleFunc("PUT /api/v1/settings/gateway/providers/{provider}", settings.serveProviderUpdate)
	mux.HandleFunc("DELETE /api/v1/settings/gateway/providers/{provider}", settings.serveProviderDelete)
	mux.HandleFunc("POST /api/v1/settings/gateway/api-keys", settings.serveGatewayAPIKeyCreate)
	mux.HandleFunc("DELETE /api/v1/settings/gateway/api-keys/{key}", settings.serveGatewayAPIKeyRevoke)
	mux.HandleFunc("GET /api/v1/settings/system-one/models", settings.serveSystemOneModelCatalog)
	mux.HandleFunc("PUT /api/v1/settings/system-one/models/{target}", settings.serveSystemOneModelAssignmentUpdate)
	mux.HandleFunc("POST /api/v1/settings/system-one/providers", settings.serveSystemOneProviderCreate)
	mux.HandleFunc("PUT /api/v1/settings/system-one/providers/{provider}", settings.serveSystemOneProviderUpdate)
	mux.HandleFunc("DELETE /api/v1/settings/system-one/providers/{provider}", settings.serveSystemOneProviderDelete)
	mux.HandleFunc("POST /api/v1/settings/system-one/api-keys", settings.serveSystemOneAPIKeyCreate)
	mux.HandleFunc("DELETE /api/v1/settings/system-one/api-keys/{key}", settings.serveSystemOneAPIKeyRevoke)
	mux.HandleFunc("PUT /api/v1/settings/runtime", settings.serveRuntimeUpdate)
	mux.HandleFunc("GET /api/v1/workspaces/loom", settings.serveLoomStatus)
	mux.HandleFunc("POST /api/v1/workspaces/loom/collect", settings.serveLoomCollect)
	mux.HandleFunc("PUT /api/v1/settings/services/{service}", settings.serveServiceUpdate)
	integrations := newIntegrationService(store, runner, &settings.mu)
	mux.HandleFunc("/api/v1/integrations/mcp", integrations.serveMCP)
	mux.HandleFunc("/api/v1/workspaces/lsp", integrations.serveLSP)
	mux.HandleFunc("POST /api/v1/workspaces/lsp/discover", integrations.serveLSPDiscover)
	mux.HandleFunc("/api/v1/workspaces/skills", integrations.serveSkills)
	mux.HandleFunc("POST /api/v1/workspaces/skills/reindex", integrations.serveSkillReindex)
	mux.HandleFunc("/api/v1/workspaces/skills/{skill}", integrations.serveSkillItem)
	mux.HandleFunc("/api/v1/workspaces/agents", integrations.serveAgents)
	mux.HandleFunc("POST /api/v1/workspaces/agents/probe", integrations.serveAgentProbe)
	mux.HandleFunc("POST /api/v1/workspaces/agents/profiles", integrations.serveProfiles)
	mux.HandleFunc("PUT /api/v1/workspaces/agents/profiles", integrations.serveProfiles)
	mux.HandleFunc("DELETE /api/v1/workspaces/agents/profiles/{profile}", integrations.serveProfileDelete)
	mux.HandleFunc("/api/v1/settings/subagents", integrations.serveAgents)
	mux.HandleFunc("POST /api/v1/settings/subagents/probe", integrations.serveAgentProbe)
	mux.HandleFunc("POST /api/v1/settings/subagents/profiles", integrations.serveProfiles)
	mux.HandleFunc("PUT /api/v1/settings/subagents/profiles", integrations.serveProfiles)
	mux.HandleFunc("DELETE /api/v1/settings/subagents/profiles/{profile}", integrations.serveProfileDelete)
	mux.HandleFunc("/api/v1/workspaces/ignore", integrations.serveIgnore)
	commits := newCommitService(parent, store)
	mux.HandleFunc("GET /api/v1/workspaces/changes", commits.serveChanges)
	mux.HandleFunc("GET /api/v1/workspaces/changes/file", commits.serveChangeDetail)
	mux.HandleFunc("/api/v1/workspaces/commits", commits.serveCommitCollection)
	mux.HandleFunc("GET /api/v1/workspaces/commits/{commit}", commits.serveCommitDetail)
	mux.HandleFunc("DELETE /api/v1/workspaces/commits/{commit}", commits.serveCommitDetail)
	mux.HandleFunc("PUT /api/v1/workspaces/commits/{commit}/proposals/{index}", commits.serveCommitProposal)
	mux.HandleFunc("POST /api/v1/workspaces/commits/{commit}/regenerate", commits.serveCommitRegenerate)
	mux.HandleFunc("POST /api/v1/workspaces/commits/{commit}/execute", commits.serveCommitExecute)
	sessions := newSessionsService(parent, runner, store.Dir)
	operations := newOperationsService(store, runner, sessions)
	mux.HandleFunc("GET /api/v1/operations", operations.serveSnapshot)
	mux.HandleFunc("GET /api/v1/directories", sessions.serveDirectoryListing)
	mux.HandleFunc("/api/v1/registered-sessions", sessions.serveRegisteredCollection)
	mux.HandleFunc("DELETE /api/v1/registered-sessions/{registration}", sessions.serveRegisteredItem)
	mux.HandleFunc("/api/v1/sessions", sessions.serveCollection)
	mux.HandleFunc("GET /api/v1/sessions/{session}", sessions.serveDetail)
	mux.HandleFunc("DELETE /api/v1/sessions/{session}", sessions.serveDetail)
	mux.HandleFunc("POST /api/v1/sessions/{session}/clear", sessions.serveClear)
	mux.HandleFunc("POST /api/v1/sessions/{session}/compact", sessions.serveCompact)
	mux.HandleFunc("POST /api/v1/sessions/{session}/messages", sessions.serveMessage)
	mux.HandleFunc("GET /api/v1/sessions/{session}/runs/latest", sessions.serveRunLatest)
	mux.HandleFunc("GET /api/v1/sessions/{session}/runs/{run}", sessions.serveRunDetail)
	mux.HandleFunc("GET /api/v1/sessions/{session}/runs/{run}/events", sessions.serveRunEvents)
	mux.HandleFunc("POST /api/v1/sessions/{session}/runs/{run}/commands", sessions.serveRunCommand)
	mux.HandleFunc("GET /api/v1/sessions/{session}/delegations", sessions.serveDelegations)
	mux.HandleFunc("DELETE /api/v1/sessions/{session}/delegations", sessions.serveDeleteDelegation)
	mux.HandleFunc("GET /api/v1/workspaces/learning", sessions.serveLearning)
	mux.HandleFunc("PUT /api/v1/workspaces/learning", sessions.serveLearning)
	mux.Handle("/api/", http.NotFoundHandler())
	mux.Handle("/", spa)
	return securityHeaders(mux), commits, sessions, nil
}

func serveStatus(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(statusResponse{Version: apiVersion, Service: "studio", Ready: true})
}

type spaHandler struct {
	assets    fs.FS
	files     http.Handler
	indexHTML []byte
}

func newSPAHandler(assets fs.FS) (http.Handler, error) {
	indexHTML, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read embedded Studio index: %w", err)
	}
	return &spaHandler{assets: assets, files: http.FileServer(http.FS(assets)), indexHTML: indexHTML}, nil
}

func (handler *spaHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+request.URL.Path), "/")
	if name == "." || name == "" {
		handler.serveIndex(writer, request)
		return
	}
	info, err := fs.Stat(handler.assets, name)
	if err == nil && !info.IsDir() {
		if strings.HasPrefix(name, "assets/") {
			writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			writer.Header().Set("Cache-Control", "no-cache")
		}
		handler.files.ServeHTTP(writer, request)
		return
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if path.Ext(name) != "" || !acceptsHTML(request.Header.Get("Accept")) {
		http.NotFound(writer, request)
		return
	}
	handler.serveIndex(writer, request)
}

func (handler *spaHandler) serveIndex(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(handler.indexHTML)))
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write(handler.indexHTML)
}

func acceptsHTML(accept string) bool {
	return accept == "" || strings.Contains(accept, "text/html") || strings.Contains(accept, "*/*")
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(writer, request)
	})
}
