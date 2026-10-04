package studio

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"reflect"
	"slices"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/systemoneconfig"
	"github.com/snowmerak/q/usagelog"
	"github.com/snowmerak/q/workspacememory"
)

const maximumTransferSize = 4 << 20

// A bundle contains only portable, selected global settings. IDs identify merge
// units within each section; missing units always leave destination data intact.
type settingsBundle struct {
	Format   string                                `json:"format"`
	Version  int                                   `json:"version"`
	Scope    string                                `json:"scope"`
	Sections map[string]map[string]json.RawMessage `json:"sections"`
}

type transferChange struct {
	Section string          `json:"section"`
	ID      string          `json:"id"`
	Action  string          `json:"action"`
	Current json.RawMessage `json:"current,omitempty"`
}

type transferPreview struct {
	Changes []transferChange `json:"changes"`
}

type transferImportResponse struct {
	Settings settingsSnapshot `json:"settings"`
	Warning  string           `json:"warning,omitempty"`
}

type transferState struct {
	main       config.Config
	configured bool
	providers  gateway.Config
	gateway    gatewayconfig.Config
	systemOne  systemoneconfig.Config
	library    library.Config
	memory     workspacememory.Config
	usage      usagelog.Config
	mcp        mcpconfig.Config
	profiles   map[string]subagent.ProfileEntry
}

type transferDefaultModel struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	ContextWindow   int64  `json:"context_window,omitempty"`
}

type transferLocalService struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	ProbeHost string `json:"probe_host,omitempty"`
}

type transferEmbedding struct {
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
}

type transferRole struct {
	Model           string `json:"model,omitempty"`
	Group           string `json:"group,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type transferLoom struct {
	MaximumArtifactMiB int            `json:"maximum_artifact_mib"`
	MaximumStoreMiB    int            `json:"maximum_store_mib"`
	GC                 transferLoomGC `json:"gc"`
}

type transferLoomGC struct {
	Disabled     bool    `json:"disabled"`
	TriggerRatio float64 `json:"trigger_ratio"`
	TargetRatio  float64 `json:"target_ratio"`
	GraceHours   int     `json:"grace_hours"`
}

func (service *settingsService) loadTransferState() (transferState, error) {
	var state transferState
	var err error
	state.main, err = service.main.Load()
	state.configured = err == nil
	if errors.Is(err, config.ErrNotFound) {
		state.main, err = config.Default(), nil
		state.main.UseManagedGateway()
	}
	if err != nil {
		return state, err
	}
	if state.providers, err = service.loadProviderConfig(); err != nil {
		return state, err
	}
	if state.gateway, err = service.gateway.LoadOrDefault(); err != nil {
		return state, err
	}
	if state.systemOne, err = service.systemOne.LoadOrDefault(); err != nil {
		return state, err
	}
	if state.library, err = service.library.LoadOrDefault(); err != nil {
		return state, err
	}
	if state.memory, err = (workspacememory.ConfigStore{Dir: service.main.Dir}).LoadOrDefault(); err != nil {
		return state, err
	}
	if state.usage, err = (usagelog.ConfigStore{Dir: service.main.Dir}).LoadOrDefault(); err != nil {
		return state, err
	}
	if state.mcp, err = service.mcp.LoadOrDefault(); err != nil {
		return state, err
	}
	state.profiles = make(map[string]subagent.ProfileEntry)
	for _, entry := range profileStore(service.main, "").List() {
		if entry.Err != nil {
			return state, fmt.Errorf("read global subagent profile: %w", entry.Err)
		}
		state.profiles[entry.Profile.Name] = entry
	}
	return state, nil
}

func (state transferState) bundle() (settingsBundle, error) {
	bundle := settingsBundle{Format: "q-settings", Version: 1, Scope: "global", Sections: make(map[string]map[string]json.RawMessage)}
	add := func(section, id string, value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if bundle.Sections[section] == nil {
			bundle.Sections[section] = make(map[string]json.RawMessage)
		}
		bundle.Sections[section][id] = data
		return nil
	}
	// Every value below is JSON-encodable; keep errors explicit if a future
	// provider metadata type adds an unsupported value.
	var errs []error
	put := func(section, id string, value any) { errs = append(errs, add(section, id, value)) }
	if state.configured {
		put("models", "default", transferDefaultModel{Model: state.main.Provider.Model, ReasoningEffort: state.main.Provider.ReasoningEffort, ContextWindow: state.main.Provider.ContextWindow})
	}
	put("models", "embedding", transferEmbedding(state.main.Embedding))
	for _, role := range state.main.NativeRoles() {
		assignment, found := state.main.Agents.Roles[role]
		if !found {
			for savedRole, saved := range state.main.Agents.Roles {
				if config.CanonicalAgentRole(savedRole) == role {
					assignment = saved
					break
				}
			}
		}
		put("models", "role/"+role, transferRole{assignment.Model, assignment.Group, assignment.ReasoningEffort})
	}
	for name, group := range state.main.ModelGroups {
		candidates := make([]modelCandidateSettings, 0, len(group.Candidates))
		for _, candidate := range group.Candidates {
			timeout := ""
			if candidate.Timeout > 0 {
				timeout = candidate.Timeout.String()
			}
			candidates = append(candidates, modelCandidateSettings{Model: candidate.Model, ReasoningEffort: candidate.ReasoningEffort, Timeout: timeout})
		}
		put("models", "group/"+name, candidates)
	}
	for model, mode := range state.main.ModelAPIModes {
		put("models", "api-mode/"+model, mode)
	}
	for _, provider := range state.providers.Providers {
		put("providers", "provider/"+provider.ID, portableProvider(provider))
	}
	put("system-one", "default", state.systemOne.DefaultModel)
	for _, provider := range state.systemOne.Providers {
		put("system-one", "provider/"+provider.ID, provider)
	}
	for role, model := range state.systemOne.RoleModels {
		put("system-one", "role/"+role, model)
	}
	put("runtime", "parallel-agents", state.main.EffectiveAgents().MaxParallel)
	put("runtime", "context", contextSettings(state.main.EffectiveContext()))
	loom := state.main.EffectiveLoom()
	put("runtime", "loom", transferLoom{loom.MaximumArtifactMiB, loom.MaximumStoreMiB, transferLoomGC(loom.GC)})
	put("runtime", "system-prompt", state.main.Provider.SystemPrompt)
	put("services", "gateway", serviceUpdate{Host: state.gateway.Server.Host, Port: state.gateway.Server.Port})
	put("services", "system-one", serviceUpdate{Host: state.systemOne.Server.Host, Port: state.systemOne.Server.Port})
	put("services", "library", transferLocalService{state.library.Host, state.library.Port, state.library.ProbeHost})
	put("services", "workspace-memory", transferLocalService{state.memory.Host, state.memory.Port, state.memory.ProbeHost})
	put("services", "usage", transferLocalService{state.usage.Host, state.usage.Port, state.usage.ProbeHost})
	for id, connection := range state.main.Agents.Connections {
		connection.Env = nil
		put("subagents", "connection/"+id, connection)
	}
	for _, role := range config.ExternalAgentRoles() {
		put("subagents", "binding/"+role, state.main.Agents.Roles[role].Agent)
	}
	for name, entry := range state.profiles {
		put("subagents", "profile/"+name, entry.Profile)
	}
	for id, server := range state.mcp.Servers {
		put("integrations", "mcp-server/"+id, server)
	}
	for role, servers := range state.mcp.Roles {
		put("integrations", "mcp-role/"+role, servers)
	}
	for id, server := range state.main.LSP.Servers {
		put("integrations", "lsp-server/"+id, server)
	}
	for language, server := range state.main.LSP.Languages {
		put("integrations", "lsp-language/"+language, server)
	}
	return bundle, errors.Join(errs...)
}

// These opaque maps can contain credentials. Their local values are retained
// on import, along with inline keys, ACP env values, and cache passwords.
func portableProvider(provider gateway.ProviderConfig) gateway.ProviderConfig {
	provider.ChatGPT = gateway.ChatGPTConfig{}
	provider.APIKey = ""
	provider.Headers = nil
	provider.Body = nil
	provider.Codex.Environment = nil
	provider.Codex.ThreadStart = nil
	provider.Codex.ConversationCache.Redis.Password = ""
	return provider
}

func (service *settingsService) serveTransferExport(writer http.ResponseWriter, _ *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	state, err := service.loadTransferState()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	bundle, err := state.bundle()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, bundle)
}

func decodeTransferBundle(writer http.ResponseWriter, request *http.Request) (settingsBundle, error) {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumTransferSize)
	var bundle settingsBundle
	err := decodeTransferJSON(request.Body, &bundle)
	if err != nil {
		return bundle, err
	}
	if bundle.Format != "q-settings" || bundle.Version != 1 || bundle.Scope != "global" {
		return bundle, errors.New("choose a Q settings file with format q-settings, version 1, and global scope")
	}
	for section, items := range bundle.Sections {
		if !slices.Contains([]string{"models", "providers", "system-one", "runtime", "services", "subagents", "integrations"}, section) || items == nil {
			return bundle, fmt.Errorf("unsupported settings section %q", section)
		}
	}
	return bundle, nil
}

func decodeTransferJSON(reader io.Reader, output any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("expected one JSON value")
	}
	return nil
}

func transferValue[T any](data json.RawMessage) (T, error) {
	var value T
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return value, errors.New("setting must not be null")
	}
	err := decodeTransferJSON(bytes.NewReader(data), &value)
	return value, err
}

func (service *settingsService) serveTransferPreview(writer http.ResponseWriter, request *http.Request) {
	service.serveTransfer(writer, request, false)
}

func (service *settingsService) serveTransferImport(writer http.ResponseWriter, request *http.Request) {
	service.serveTransfer(writer, request, true)
}

func (service *settingsService) serveTransfer(writer http.ResponseWriter, request *http.Request, apply bool) {
	bundle, err := decodeTransferBundle(writer, request)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	state, err := service.loadTransferState()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	before, err := state.bundle()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	preview := transferPreview{Changes: make([]transferChange, 0)}
	sections := slices.Sorted(maps.Keys(bundle.Sections))
	for _, section := range sections {
		for _, id := range slices.Sorted(maps.Keys(bundle.Sections[section])) {
			data := bundle.Sections[section][id]
			if err := state.merge(section, id, data); err != nil {
				writeAPIError(writer, http.StatusUnprocessableEntity, fmt.Errorf("%s / %s: %w", section, id, err))
				return
			}
			action := "add"
			if previous, found := before.Sections[section][id]; found {
				action = "replace"
				var oldValue, newValue any
				_ = json.Unmarshal(previous, &oldValue)
				_ = json.Unmarshal(data, &newValue)
				if reflect.DeepEqual(oldValue, newValue) {
					action = "unchanged"
				}
			}
			preview.Changes = append(preview.Changes, transferChange{Section: section, ID: id, Action: action, Current: before.Sections[section][id]})
		}
	}
	if len(preview.Changes) == 0 {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("select at least one setting"))
		return
	}
	if err := state.validate(request.Context(), bundle, service.main.Dir); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if !apply {
		writeJSON(writer, http.StatusOK, preview)
		return
	}
	if err := service.saveTransfer(request, state, bundle); err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	response := transferImportResponse{}
	if _, ok := bundle.Sections["models"]["embedding"]; ok {
		if runtime, ok := service.runtime.(embeddingRuntime); ok {
			if err := runtime.SyncEmbeddings(request.Context(), "", state.main); err != nil {
				response.Warning = fmt.Sprintf("Settings imported, but embedding reindexing failed: %v", err)
			}
		}
	}
	response.Settings, err = service.snapshot()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}
