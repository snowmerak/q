package studio

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gatewayconfig"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/systemoneconfig"
	qtools "github.com/snowmerak/q/tools"
)

func (service *settingsService) serveRuntimeUpdate(writer http.ResponseWriter, request *http.Request) {
	var update runtimeUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if errors.Is(err, config.ErrNotFound) {
		writeAPIError(writer, http.StatusConflict, errors.New("main q configuration is not initialized"))
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	value.Agents.MaxParallel = update.MaxParallel
	value.Context = config.ContextConfig{
		Window: update.Context.Window, TriggerRatio: update.Context.TriggerRatio,
		TargetRatio: update.Context.TargetRatio, RecentRatio: update.Context.RecentRatio,
	}
	value.Loom = config.LoomConfig{
		MaximumArtifactMiB: update.Loom.MaximumArtifactMiB,
		MaximumStoreMiB:    update.Loom.MaximumStoreMiB,
		GC: config.LoomGCConfig{
			Disabled: update.Loom.GCDisabled, TriggerRatio: update.Loom.GCTriggerRatio,
			TargetRatio: update.Loom.GCTargetRatio, GraceHours: update.Loom.GCGraceHours,
		},
	}
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveLoomStatus(writer http.ResponseWriter, request *http.Request) {
	root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	runtime, err := service.openLoomRuntime(request.Context(), root)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = runtime.Close() }()
	stats, err := runtime.LoomStats(request.Context())
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, loomStatusResponse{WorkspaceRoot: root, Stats: stats})
}

func (service *settingsService) serveLoomCollect(writer http.ResponseWriter, request *http.Request) {
	var input loomOperationRequest
	if err := decodeSettingsRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	runtime, err := service.openLoomRuntime(request.Context(), root)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = runtime.Close() }()
	result, err := runtime.CollectLoom(request.Context(), input.DryRun)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	stats, err := runtime.LoomStats(request.Context())
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, loomStatusResponse{WorkspaceRoot: root, Stats: stats, Result: &result})
}

func (service *settingsService) openLoomRuntime(ctx context.Context, root string) (*qtools.Runtime, error) {
	service.mu.Lock()
	value, err := service.main.Load()
	service.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return qtools.NewRuntimeWithArchiveAndLoomOptions(ctx, root, nil, value.LoomStoreOptions(nil))
}

func (service *settingsService) serveServiceUpdate(writer http.ResponseWriter, request *http.Request) {
	var update serviceUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	update.Host = strings.TrimSpace(update.Host)
	service.mu.Lock()
	defer service.mu.Unlock()
	var err error
	switch request.PathValue("service") {
	case "gateway":
		var value gatewayconfig.Config
		value, err = service.gateway.LoadOrDefault()
		if err == nil {
			value.Server = gatewayconfig.ServerConfig{Host: update.Host, Port: update.Port}
			err = service.gateway.Save(value)
		}
	case "system-one":
		var value systemoneconfig.Config
		value, err = service.systemOne.LoadOrDefault()
		if err == nil {
			value.Server.Host, value.Server.Port = update.Host, update.Port
			err = service.systemOne.Save(value)
		}
	case "library":
		var value qlibrary.Config
		value, err = service.library.LoadOrDefault()
		if err == nil {
			value.Host, value.Port = update.Host, update.Port
			err = service.library.Save(value)
		}
	default:
		writeAPIError(writer, http.StatusNotFound, errors.New("unknown service settings"))
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}
