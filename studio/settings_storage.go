package studio

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/snowmerak/q/opensearchindex"
	"github.com/snowmerak/q/storageconfig"
	"github.com/snowmerak/q/workitems"
)

func makeStorageSettings(configPath string, cfg storageconfig.Config) storageSettings {
	cfg = cfg.Effective()
	return storageSettings{
		ConfigPath: configPath,
		Mode:       cfg.Mode,
		Remote: remoteStorageSettings{
			OpenSearch: openSearchStorageSettings{
				Enabled:              cfg.Remote.OpenSearch.Enabled,
				Endpoint:             cfg.Remote.OpenSearch.Endpoint,
				IndexPrefix:          cfg.Remote.OpenSearch.IndexPrefix,
				Refresh:              cfg.Remote.OpenSearch.Refresh,
				Username:             cfg.Remote.OpenSearch.Username,
				PasswordEnv:          cfg.Remote.OpenSearch.PasswordEnv,
				HasInlinePassword:    strings.TrimSpace(cfg.Remote.OpenSearch.Password) != "",
				BearerTokenEnv:       cfg.Remote.OpenSearch.BearerTokenEnv,
				HasInlineBearerToken: strings.TrimSpace(cfg.Remote.OpenSearch.BearerToken) != "",
				Targets: openSearchTargetSettings{
					Skills:          cfg.Remote.OpenSearch.Targets.Skills,
					Knowledge:       cfg.Remote.OpenSearch.Targets.Knowledge,
					WorkspaceMemory: cfg.Remote.OpenSearch.Targets.WorkspaceMemory,
				},
			},
			Postgres: postgresStorageSettings{
				Enabled:      cfg.Remote.Postgres.Enabled,
				DSNEnv:       cfg.Remote.Postgres.DSNEnv,
				HasInlineDSN: strings.TrimSpace(cfg.Remote.Postgres.DSN) != "",
			},
		},
	}
}

func mergeRemoteStorage(existing storageconfig.RemoteConfig, update remoteStorageUpdate) storageconfig.RemoteConfig {
	osCfg := existing.OpenSearch
	osCfg.Enabled = update.OpenSearch.Enabled
	osCfg.Endpoint = update.OpenSearch.Endpoint
	osCfg.IndexPrefix = update.OpenSearch.IndexPrefix
	osCfg.Refresh = update.OpenSearch.Refresh
	osCfg.Username = update.OpenSearch.Username
	osCfg.PasswordEnv = update.OpenSearch.PasswordEnv
	osCfg.Password = mergeSecret(osCfg.Password, update.OpenSearch.Password, update.OpenSearch.ClearPassword)
	osCfg.BearerTokenEnv = update.OpenSearch.BearerTokenEnv
	osCfg.BearerToken = mergeSecret(osCfg.BearerToken, update.OpenSearch.BearerToken, update.OpenSearch.ClearBearerToken)
	osCfg.Targets = storageconfig.OpenSearchTargets{
		Skills:          update.OpenSearch.Targets.Skills,
		Knowledge:       update.OpenSearch.Targets.Knowledge,
		WorkspaceMemory: update.OpenSearch.Targets.WorkspaceMemory,
	}

	pgCfg := existing.Postgres
	pgCfg.Enabled = update.Postgres.Enabled
	pgCfg.DSNEnv = update.Postgres.DSNEnv
	pgCfg.DSN = mergeSecret(pgCfg.DSN, update.Postgres.DSN, update.Postgres.ClearDSN)

	return storageconfig.RemoteConfig{
		OpenSearch: osCfg,
		Postgres:   pgCfg,
	}
}

func mergeSecret(existing, replacement string, clear bool) string {
	if clear {
		return ""
	}
	if strings.TrimSpace(replacement) != "" {
		return strings.TrimSpace(replacement)
	}
	return existing
}

func (service *settingsService) serveStorageUpdate(writer http.ResponseWriter, request *http.Request) {
	var update storageUpdateRequest
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	current, err := service.storage.LoadOrDefault()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	next := storageconfig.Config{
		Version: storageconfig.Version,
		Mode:    update.Mode,
		Remote:  mergeRemoteStorage(current.Remote, update.Remote),
	}.Effective()
	if next.Mode == storageconfig.ModeRemote && next.Remote.OpenSearch.Enabled && next.Remote.OpenSearch.Endpoint == "" {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("opensearch endpoint is required when OpenSearch is enabled in remote mode"))
		return
	}
	if next.Mode == storageconfig.ModeRemote && next.Remote.Postgres.Enabled && next.ResolvePostgresDSN() == "" {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("postgres DSN or DSN environment variable is required when PostgreSQL is enabled in remote mode"))
		return
	}
	if err := service.storage.Save(next); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveStorageTest(writer http.ResponseWriter, request *http.Request) {
	var req storageTestRequest
	if err := decodeSettingsRequest(writer, request, &req); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	target := strings.ToLower(strings.TrimSpace(req.Target))
	if target != "opensearch" && target != "postgres" {
		writeAPIError(writer, http.StatusBadRequest, errors.New("storage test target must be opensearch or postgres"))
		return
	}

	service.mu.Lock()
	current, err := service.storage.LoadOrDefault()
	service.mu.Unlock()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	candidate := storageconfig.Config{
		Version: storageconfig.Version,
		Mode:    storageconfig.ModeRemote,
		Remote:  mergeRemoteStorage(current.Remote, req.Remote),
	}.Effective()

	started := time.Now()
	testCtx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()

	var testErr error
	var okMsg string
	switch target {
	case "opensearch":
		client, clientErr := opensearchindex.NewClient(opensearchindex.Config{
			Endpoint:    candidate.Remote.OpenSearch.Endpoint,
			Username:    candidate.Remote.OpenSearch.Username,
			Password:    candidate.ResolveOpenSearchPassword(),
			BearerToken: candidate.ResolveOpenSearchBearerToken(),
			IndexPrefix: candidate.Remote.OpenSearch.IndexPrefix,
			Refresh:     candidate.Remote.OpenSearch.Refresh,
		})
		if clientErr != nil {
			testErr = clientErr
		} else {
			defer func() { _ = client.Close() }()
			testErr = client.Ping(testCtx)
			okMsg = "Connected to OpenSearch cluster"
		}
	case "postgres":
		dsn := candidate.ResolvePostgresDSN()
		if dsn == "" {
			testErr = errors.New("postgres DSN or DSN environment variable is empty")
		} else {
			testErr = workitems.PingPostgres(testCtx, dsn)
			okMsg = "Connected to PostgreSQL database"
		}
	}

	resp := storageTestResponse{
		OK:        testErr == nil,
		Target:    target,
		Message:   okMsg,
		LatencyMS: time.Since(started).Milliseconds(),
	}
	if testErr != nil {
		resp.Message = testErr.Error()
	}
	writeJSON(writer, http.StatusOK, resp)
}
